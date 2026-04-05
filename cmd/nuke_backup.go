package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type nukeBackupS3API interface {
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

type nukeBackupDynamoAPI interface {
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
}

type runtimeStateBackupPlan struct {
	BucketName         string
	BucketVersionCount int
	DeleteMarkerCount  int
	TableName          string
	TableItemCount     int
}

func (p runtimeStateBackupPlan) hasData() bool {
	return p.BucketVersionCount > 0 || p.DeleteMarkerCount > 0 || p.TableItemCount > 0
}

func (p runtimeStateBackupPlan) summaryLines() []string {
	return []string{
		fmt.Sprintf("S3 bucket %s: %d object version(s), %d delete marker(s)", p.BucketName, p.BucketVersionCount, p.DeleteMarkerCount),
		fmt.Sprintf("DynamoDB table %s: %d item(s)", p.TableName, p.TableItemCount),
	}
}

var (
	newNukeBackupS3ClientFn            = func(cfg sdkaws.Config) nukeBackupS3API { return s3.NewFromConfig(cfg) }
	newNukeBackupDynamoClientFn        = func(cfg sdkaws.Config) nukeBackupDynamoAPI { return dynamodb.NewFromConfig(cfg) }
	inspectRuntimeStateStoresForNukeFn = inspectRuntimeStateStoresForNuke
	backupRuntimeStateStoresForNukeFn  = backupRuntimeStateStoresForNuke
	defaultRuntimeBackupDirForNukeFn   = defaultRuntimeBackupDirForNuke
)

func runtimeStateBucketName(org string) string {
	return org + "-tf-state-runtime"
}

func runtimeLockTableName(org string) string {
	return org + "-tf-locks-runtime"
}

func defaultRuntimeBackupDirForNuke(root, env string) string {
	return filepath.Join(root, ".backups", "nuke", env, time.Now().UTC().Format("20060102T150405Z"), "platform-org")
}

func inspectRuntimeStateStoresForNuke(ctx context.Context, cfg sdkaws.Config, org string) (runtimeStateBackupPlan, error) {
	bucket := runtimeStateBucketName(org)
	table := runtimeLockTableName(org)
	plan := runtimeStateBackupPlan{BucketName: bucket, TableName: table}

	s3Client := newNukeBackupS3ClientFn(cfg)
	if err := countBucketVersions(ctx, s3Client, &plan); err != nil {
		return runtimeStateBackupPlan{}, err
	}

	dynamoClient := newNukeBackupDynamoClientFn(cfg)
	if err := countTableItems(ctx, dynamoClient, &plan); err != nil {
		return runtimeStateBackupPlan{}, err
	}

	return plan, nil
}

func countBucketVersions(ctx context.Context, client nukeBackupS3API, plan *runtimeStateBackupPlan) error {
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: sdkaws.String(plan.BucketName)})
	if err != nil {
		if isS3BucketMissing(err) {
			return nil
		}
		return fmt.Errorf("check bucket %s: %w", plan.BucketName, err)
	}

	var keyMarker, versionMarker *string
	for {
		out, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket:          sdkaws.String(plan.BucketName),
			KeyMarker:       keyMarker,
			VersionIdMarker: versionMarker,
		})
		if err != nil {
			return fmt.Errorf("list bucket versions %s: %w", plan.BucketName, err)
		}
		plan.BucketVersionCount += len(out.Versions)
		plan.DeleteMarkerCount += len(out.DeleteMarkers)
		if !sdkaws.ToBool(out.IsTruncated) {
			break
		}
		keyMarker = out.NextKeyMarker
		versionMarker = out.NextVersionIdMarker
	}
	return nil
}

func countTableItems(ctx context.Context, client nukeBackupDynamoAPI, plan *runtimeStateBackupPlan) error {
	_, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: sdkaws.String(plan.TableName)})
	if err != nil {
		var notFound *dbtypes.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("describe table %s: %w", plan.TableName, err)
	}

	var startKey map[string]dbtypes.AttributeValue
	for {
		out, err := client.Scan(ctx, &dynamodb.ScanInput{
			TableName:         sdkaws.String(plan.TableName),
			ExclusiveStartKey: startKey,
			Select:            dbtypes.SelectCount,
		})
		if err != nil {
			return fmt.Errorf("scan table %s: %w", plan.TableName, err)
		}
		plan.TableItemCount += int(out.Count)
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		startKey = out.LastEvaluatedKey
	}
	return nil
}

func backupRuntimeStateStoresForNuke(ctx context.Context, cfg sdkaws.Config, org, dir string, plan runtimeStateBackupPlan) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	manifest := map[string]any{
		"created_at":           time.Now().UTC().Format(time.RFC3339),
		"org":                  org,
		"env":                  d.env,
		"account_id":           d.accountID,
		"bucket":               plan.BucketName,
		"bucket_version_count": plan.BucketVersionCount,
		"delete_marker_count":  plan.DeleteMarkerCount,
		"table":                plan.TableName,
		"table_item_count":     plan.TableItemCount,
	}

	s3Client := newNukeBackupS3ClientFn(cfg)
	if plan.BucketVersionCount > 0 || plan.DeleteMarkerCount > 0 {
		s3Meta, err := backupBucketVersions(ctx, s3Client, plan.BucketName, filepath.Join(dir, "s3", plan.BucketName))
		if err != nil {
			return err
		}
		manifest["s3_objects"] = s3Meta
	}

	dynamoClient := newNukeBackupDynamoClientFn(cfg)
	if plan.TableItemCount > 0 {
		tablePath := filepath.Join(dir, "dynamodb", plan.TableName+".json")
		if err := backupDynamoTable(ctx, dynamoClient, plan.TableName, tablePath); err != nil {
			return err
		}
		manifest["dynamodb_backup"] = tablePath
	}

	return writeJSONFile(filepath.Join(dir, "manifest.json"), manifest)
}

func backupBucketVersions(ctx context.Context, client nukeBackupS3API, bucket, dir string) ([]map[string]any, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	var keyMarker, versionMarker *string
	index := 0
	var metadata []map[string]any
	for {
		out, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket:          sdkaws.String(bucket),
			KeyMarker:       keyMarker,
			VersionIdMarker: versionMarker,
		})
		if err != nil {
			return nil, fmt.Errorf("list bucket versions %s: %w", bucket, err)
		}
		for _, version := range out.Versions {
			index++
			name := fmt.Sprintf("object-%06d.bin", index)
			target := filepath.Join(dir, name)
			if err := downloadBucketVersion(ctx, client, bucket, sdkaws.ToString(version.Key), sdkaws.ToString(version.VersionId), target); err != nil {
				return nil, err
			}
			metadata = append(metadata, map[string]any{
				"file":       name,
				"key":        sdkaws.ToString(version.Key),
				"version_id": sdkaws.ToString(version.VersionId),
				"size":       version.Size,
				"is_latest":  sdkaws.ToBool(version.IsLatest),
			})
		}
		for _, marker := range out.DeleteMarkers {
			metadata = append(metadata, map[string]any{
				"delete_marker": true,
				"key":           sdkaws.ToString(marker.Key),
				"version_id":    sdkaws.ToString(marker.VersionId),
				"is_latest":     sdkaws.ToBool(marker.IsLatest),
			})
		}
		if !sdkaws.ToBool(out.IsTruncated) {
			break
		}
		keyMarker = out.NextKeyMarker
		versionMarker = out.NextVersionIdMarker
	}
	return metadata, nil
}

func downloadBucketVersion(ctx context.Context, client nukeBackupS3API, bucket, key, versionID, target string) (retErr error) {
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket:    sdkaws.String(bucket),
		Key:       sdkaws.String(key),
		VersionId: sdkaws.String(versionID),
	})
	if err != nil {
		return fmt.Errorf("download s3://%s/%s?versionId=%s: %w", bucket, key, versionID, err)
	}
	defer func() { _ = out.Body.Close() }()

	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	//nolint:gosec // target is derived from internal configuration (sequential index), not user input
	file, err := os.Create(target)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := file.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
	}()
	if _, err := io.Copy(file, out.Body); err != nil {
		return err
	}
	return nil
}

func backupDynamoTable(ctx context.Context, client nukeBackupDynamoAPI, table, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	var startKey map[string]dbtypes.AttributeValue
	var items []map[string]any
	for {
		out, err := client.Scan(ctx, &dynamodb.ScanInput{
			TableName:         sdkaws.String(table),
			ExclusiveStartKey: startKey,
		})
		if err != nil {
			return fmt.Errorf("scan table %s: %w", table, err)
		}
		for _, raw := range out.Items {
			var decoded map[string]any
			if err := attributevalue.UnmarshalMap(raw, &decoded); err != nil {
				return fmt.Errorf("decode table %s item: %w", table, err)
			}
			items = append(items, decoded)
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		startKey = out.LastEvaluatedKey
	}
	return writeJSONFile(target, items)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	//nolint:gosec // backup files are written to an operator-specified local directory
	return os.WriteFile(path, data, 0o600)
}

func isS3BucketMissing(err error) bool {
	var notFound *s3types.NotFound
	return errors.As(err, &notFound)
}
