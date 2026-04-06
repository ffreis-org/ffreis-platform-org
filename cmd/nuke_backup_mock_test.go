package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	testNukeBackupBucketName           = "my-bucket"
	testNukeBackupStateKey             = "state.tfstate"
	testNukeBackupTableName            = "my-table"
	testNukeBackupTableFile            = "table.json"
	testNukeBackupFileNotCreatedErrorf = "file not created: %v"
)

// --- mock types ---

type mockNukeBackupS3 struct {
	headBucketFn         func(ctx context.Context, input *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	listObjectVersionsFn func(ctx context.Context, input *s3.ListObjectVersionsInput, opts ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	getObjectFn          func(ctx context.Context, input *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

func (m *mockNukeBackupS3) HeadBucket(ctx context.Context, input *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return m.headBucketFn(ctx, input, opts...)
}

func (m *mockNukeBackupS3) ListObjectVersions(ctx context.Context, input *s3.ListObjectVersionsInput, opts ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	return m.listObjectVersionsFn(ctx, input, opts...)
}

func (m *mockNukeBackupS3) GetObject(ctx context.Context, input *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return m.getObjectFn(ctx, input, opts...)
}

type mockNukeBackupDynamo struct {
	describeTableFn func(ctx context.Context, input *dynamodb.DescribeTableInput, opts ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	scanFn          func(ctx context.Context, input *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
}

func (m *mockNukeBackupDynamo) DescribeTable(ctx context.Context, input *dynamodb.DescribeTableInput, opts ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	return m.describeTableFn(ctx, input, opts...)
}

func (m *mockNukeBackupDynamo) Scan(ctx context.Context, input *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return m.scanFn(ctx, input, opts...)
}

// withNukeBackupMocks installs mock constructors for the duration of the test.
func withNukeBackupMocks(t *testing.T, s3mock nukeBackupS3API, dynamoMock nukeBackupDynamoAPI) {
	t.Helper()
	oldS3 := newNukeBackupS3ClientFn
	oldDynamo := newNukeBackupDynamoClientFn
	newNukeBackupS3ClientFn = func(_ sdkaws.Config) nukeBackupS3API { return s3mock }
	newNukeBackupDynamoClientFn = func(_ sdkaws.Config) nukeBackupDynamoAPI { return dynamoMock }
	t.Cleanup(func() {
		newNukeBackupS3ClientFn = oldS3
		newNukeBackupDynamoClientFn = oldDynamo
	})
}

// --- countBucketVersions ---

func TestCountBucketVersionsBucketMissing(t *testing.T) {
	t.Parallel()
	mock := &mockNukeBackupS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return nil, &s3types.NotFound{}
		},
	}
	plan := runtimeStateBackupPlan{BucketName: "missing-bucket"}
	if err := countBucketVersions(context.Background(), mock, &plan); err != nil {
		t.Fatalf("expected nil for missing bucket, got: %v", err)
	}
	if plan.BucketVersionCount != 0 || plan.DeleteMarkerCount != 0 {
		t.Fatalf("expected zero counts, got versions=%d markers=%d", plan.BucketVersionCount, plan.DeleteMarkerCount)
	}
}

func TestCountBucketVersionsHeadBucketError(t *testing.T) {
	t.Parallel()
	mock := &mockNukeBackupS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return nil, errors.New("access denied")
		},
	}
	plan := runtimeStateBackupPlan{BucketName: testNukeBackupBucketName}
	if err := countBucketVersions(context.Background(), mock, &plan); err == nil {
		t.Fatal("expected error for HeadBucket failure")
	}
}

func TestCountBucketVersionsCountsVersionsAndMarkers(t *testing.T) {
	t.Parallel()
	mock := &mockNukeBackupS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		listObjectVersionsFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			return &s3.ListObjectVersionsOutput{
				Versions: []s3types.ObjectVersion{
					{Key: sdkaws.String("a"), VersionId: sdkaws.String("v1")},
					{Key: sdkaws.String("b"), VersionId: sdkaws.String("v2")},
					{Key: sdkaws.String("c"), VersionId: sdkaws.String("v3")},
				},
				DeleteMarkers: []s3types.DeleteMarkerEntry{
					{Key: sdkaws.String("a"), VersionId: sdkaws.String("m1")},
				},
				IsTruncated: sdkaws.Bool(false),
			}, nil
		},
	}
	plan := runtimeStateBackupPlan{BucketName: testNukeBackupBucketName}
	if err := countBucketVersions(context.Background(), mock, &plan); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if plan.BucketVersionCount != 3 {
		t.Fatalf("expected 3 versions, got %d", plan.BucketVersionCount)
	}
	if plan.DeleteMarkerCount != 1 {
		t.Fatalf("expected 1 delete marker, got %d", plan.DeleteMarkerCount)
	}
}

func TestCountBucketVersionsListError(t *testing.T) {
	t.Parallel()
	mock := &mockNukeBackupS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		listObjectVersionsFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			return nil, errors.New("list failed")
		},
	}
	plan := runtimeStateBackupPlan{BucketName: testNukeBackupBucketName}
	if err := countBucketVersions(context.Background(), mock, &plan); err == nil {
		t.Fatal("expected error from ListObjectVersions failure")
	}
}

func TestCountBucketVersionsPaginates(t *testing.T) {
	t.Parallel()
	calls := 0
	mock := &mockNukeBackupS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		listObjectVersionsFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			calls++
			if calls == 1 {
				return &s3.ListObjectVersionsOutput{
					Versions:            []s3types.ObjectVersion{{Key: sdkaws.String("k"), VersionId: sdkaws.String("v1")}},
					DeleteMarkers:       []s3types.DeleteMarkerEntry{{Key: sdkaws.String("k"), VersionId: sdkaws.String("m1")}},
					IsTruncated:         sdkaws.Bool(true),
					NextKeyMarker:       sdkaws.String("k"),
					NextVersionIdMarker: sdkaws.String("v1"),
				}, nil
			}
			return &s3.ListObjectVersionsOutput{
				Versions:      []s3types.ObjectVersion{{Key: sdkaws.String("k2"), VersionId: sdkaws.String("v2")}},
				DeleteMarkers: []s3types.DeleteMarkerEntry{{Key: sdkaws.String("k2"), VersionId: sdkaws.String("m2")}},
				IsTruncated:   sdkaws.Bool(false),
			}, nil
		},
	}
	plan := runtimeStateBackupPlan{BucketName: testNukeBackupBucketName}
	if err := countBucketVersions(context.Background(), mock, &plan); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 list calls for pagination, got %d", calls)
	}
	if plan.BucketVersionCount != 2 {
		t.Fatalf("expected 2 versions total, got %d", plan.BucketVersionCount)
	}
	if plan.DeleteMarkerCount != 2 {
		t.Fatalf("expected 2 delete markers total, got %d", plan.DeleteMarkerCount)
	}
}

// --- countTableItems ---

func TestCountTableItemsTableNotFound(t *testing.T) {
	t.Parallel()
	mock := &mockNukeBackupDynamo{
		describeTableFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return nil, &dbtypes.ResourceNotFoundException{Message: sdkaws.String("not found")}
		},
	}
	plan := runtimeStateBackupPlan{TableName: "missing-table"}
	if err := countTableItems(context.Background(), mock, &plan); err != nil {
		t.Fatalf("expected nil for ResourceNotFoundException, got: %v", err)
	}
	if plan.TableItemCount != 0 {
		t.Fatalf("expected zero item count, got %d", plan.TableItemCount)
	}
}

func TestCountTableItemsDescribeError(t *testing.T) {
	t.Parallel()
	mock := &mockNukeBackupDynamo{
		describeTableFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return nil, errors.New("access denied")
		},
	}
	plan := runtimeStateBackupPlan{TableName: testNukeBackupTableName}
	if err := countTableItems(context.Background(), mock, &plan); err == nil {
		t.Fatal("expected error for DescribeTable failure")
	}
}

func TestCountTableItemsCountsItems(t *testing.T) {
	t.Parallel()
	mock := &mockNukeBackupDynamo{
		describeTableFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{Count: 7}, nil
		},
	}
	plan := runtimeStateBackupPlan{TableName: testNukeBackupTableName}
	if err := countTableItems(context.Background(), mock, &plan); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if plan.TableItemCount != 7 {
		t.Fatalf("expected item count 7, got %d", plan.TableItemCount)
	}
}

func TestCountTableItemsScanError(t *testing.T) {
	t.Parallel()
	mock := &mockNukeBackupDynamo{
		describeTableFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return nil, errors.New("scan failed")
		},
	}
	plan := runtimeStateBackupPlan{TableName: testNukeBackupTableName}
	if err := countTableItems(context.Background(), mock, &plan); err == nil {
		t.Fatal("expected error from Scan failure")
	}
}

func TestCountTableItemsPaginates(t *testing.T) {
	t.Parallel()
	calls := 0
	pageKey := map[string]dbtypes.AttributeValue{
		"LockID": &dbtypes.AttributeValueMemberS{Value: "page-token"},
	}
	mock := &mockNukeBackupDynamo{
		describeTableFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			calls++
			if calls == 1 {
				return &dynamodb.ScanOutput{Count: 5, LastEvaluatedKey: pageKey}, nil
			}
			return &dynamodb.ScanOutput{Count: 3}, nil
		},
	}
	plan := runtimeStateBackupPlan{TableName: testNukeBackupTableName}
	if err := countTableItems(context.Background(), mock, &plan); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 scan calls for pagination, got %d", calls)
	}
	if plan.TableItemCount != 8 {
		t.Fatalf("expected 8 total items, got %d", plan.TableItemCount)
	}
}

// --- inspectRuntimeStateStoresForNuke ---

func TestInspectRuntimeStateStoresForNukeReturnsPopulatedPlan(t *testing.T) {
	s3mock := &mockNukeBackupS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		listObjectVersionsFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			return &s3.ListObjectVersionsOutput{
				Versions:    []s3types.ObjectVersion{{Key: sdkaws.String("k"), VersionId: sdkaws.String("v1")}},
				IsTruncated: sdkaws.Bool(false),
			}, nil
		},
	}
	dynamoMock := &mockNukeBackupDynamo{
		describeTableFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{Count: 2}, nil
		},
	}
	withNukeBackupMocks(t, s3mock, dynamoMock)
	plan, err := inspectRuntimeStateStoresForNuke(context.Background(), sdkaws.Config{}, "myorg")
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if plan.BucketVersionCount != 1 {
		t.Fatalf("expected 1 version, got %d", plan.BucketVersionCount)
	}
	if plan.TableItemCount != 2 {
		t.Fatalf("expected 2 table items, got %d", plan.TableItemCount)
	}
	if plan.BucketName != runtimeStateBucketName("myorg") {
		t.Fatalf("unexpected bucket name: %s", plan.BucketName)
	}
}

func TestInspectRuntimeStateStoresForNukeS3Error(t *testing.T) {
	s3mock := &mockNukeBackupS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return nil, errors.New("s3 failure")
		},
	}
	withNukeBackupMocks(t, s3mock, nil)
	_, err := inspectRuntimeStateStoresForNuke(context.Background(), sdkaws.Config{}, "myorg")
	if err == nil {
		t.Fatal("expected error from S3 failure")
	}
}

func TestInspectRuntimeStateStoresForNukeDynamoError(t *testing.T) {
	s3mock := &mockNukeBackupS3{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return nil, &s3types.NotFound{}
		},
	}
	dynamoMock := &mockNukeBackupDynamo{
		describeTableFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return nil, errors.New("dynamo failure")
		},
	}
	withNukeBackupMocks(t, s3mock, dynamoMock)
	_, err := inspectRuntimeStateStoresForNuke(context.Background(), sdkaws.Config{}, "myorg")
	if err == nil {
		t.Fatal("expected error from DynamoDB failure")
	}
}

// --- downloadBucketVersion ---

func TestDownloadBucketVersionSuccessCreatesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "object-000001.bin")
	content := `{"terraform":"state"}`
	mock := &mockNukeBackupS3{
		getObjectFn: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body: io.NopCloser(bytes.NewBufferString(content)),
			}, nil
		},
	}
	if err := downloadBucketVersion(context.Background(), mock, testNukeBackupBucketName, testNukeBackupStateKey, "v1", target); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf(testNukeBackupFileNotCreatedErrorf, err)
	}
	if string(data) != content {
		t.Fatalf("file content mismatch: got %q, want %q", string(data), content)
	}
}

func TestDownloadBucketVersionGetObjectError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "object-000001.bin")
	mock := &mockNukeBackupS3{
		getObjectFn: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return nil, errors.New("get object failed")
		},
	}
	if err := downloadBucketVersion(context.Background(), mock, testNukeBackupBucketName, "key", "v1", target); err == nil {
		t.Fatal("expected error from GetObject failure")
	}
}

// --- backupBucketVersions ---

func TestBackupBucketVersionsListError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mock := &mockNukeBackupS3{
		listObjectVersionsFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			return nil, errors.New("list failed")
		},
	}
	_, err := backupBucketVersions(context.Background(), mock, testNukeBackupBucketName, dir)
	if err == nil {
		t.Fatal("expected error from list failure")
	}
}

func TestBackupBucketVersionsDownloadsVersionsAndReturnsMetadata(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mock := &mockNukeBackupS3{
		listObjectVersionsFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			return &s3.ListObjectVersionsOutput{
				Versions: []s3types.ObjectVersion{
					{Key: sdkaws.String(testNukeBackupStateKey), VersionId: sdkaws.String("v1"), IsLatest: sdkaws.Bool(true), Size: sdkaws.Int64(42)},
				},
				IsTruncated: sdkaws.Bool(false),
			}, nil
		},
		getObjectFn: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body: io.NopCloser(bytes.NewBufferString(`{"data":"state"}`)),
			}, nil
		},
	}
	metadata, err := backupBucketVersions(context.Background(), mock, testNukeBackupBucketName, dir)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if len(metadata) != 1 {
		t.Fatalf("expected 1 metadata entry, got %d", len(metadata))
	}
	if metadata[0]["key"] != testNukeBackupStateKey {
		t.Fatalf("unexpected key in metadata: %v", metadata[0]["key"])
	}
	if metadata[0]["version_id"] != "v1" {
		t.Fatalf("unexpected version_id in metadata: %v", metadata[0]["version_id"])
	}
	if _, ok := metadata[0]["file"]; !ok {
		t.Fatal("expected 'file' field in metadata")
	}
}

func TestBackupBucketVersionsGetObjectError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mock := &mockNukeBackupS3{
		listObjectVersionsFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			return &s3.ListObjectVersionsOutput{
				Versions:    []s3types.ObjectVersion{{Key: sdkaws.String("k"), VersionId: sdkaws.String("v1")}},
				IsTruncated: sdkaws.Bool(false),
			}, nil
		},
		getObjectFn: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return nil, errors.New("get object failed")
		},
	}
	_, err := backupBucketVersions(context.Background(), mock, testNukeBackupBucketName, dir)
	if err == nil {
		t.Fatal("expected error from GetObject failure")
	}
}

func TestBackupBucketVersionsPaginates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	calls := 0
	mock := &mockNukeBackupS3{
		listObjectVersionsFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			calls++
			if calls == 1 {
				return &s3.ListObjectVersionsOutput{
					Versions:            []s3types.ObjectVersion{{Key: sdkaws.String("k"), VersionId: sdkaws.String("v1")}},
					IsTruncated:         sdkaws.Bool(true),
					NextKeyMarker:       sdkaws.String("k"),
					NextVersionIdMarker: sdkaws.String("v1"),
				}, nil
			}
			return &s3.ListObjectVersionsOutput{
				Versions:    []s3types.ObjectVersion{{Key: sdkaws.String("k2"), VersionId: sdkaws.String("v2")}},
				IsTruncated: sdkaws.Bool(false),
			}, nil
		},
		getObjectFn: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body: io.NopCloser(bytes.NewBufferString("content")),
			}, nil
		},
	}
	metadata, err := backupBucketVersions(context.Background(), mock, testNukeBackupBucketName, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 list calls, got %d", calls)
	}
	if len(metadata) != 2 {
		t.Fatalf("expected 2 metadata entries, got %d", len(metadata))
	}
}

func TestBackupBucketVersionsIncludesDeleteMarkers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mock := &mockNukeBackupS3{
		listObjectVersionsFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			return &s3.ListObjectVersionsOutput{
				Versions: []s3types.ObjectVersion{
					{Key: sdkaws.String("k"), VersionId: sdkaws.String("v1")},
				},
				DeleteMarkers: []s3types.DeleteMarkerEntry{
					{Key: sdkaws.String("k"), VersionId: sdkaws.String("m1"), IsLatest: sdkaws.Bool(true)},
				},
				IsTruncated: sdkaws.Bool(false),
			}, nil
		},
		getObjectFn: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body: io.NopCloser(bytes.NewBufferString("data")),
			}, nil
		},
	}
	metadata, err := backupBucketVersions(context.Background(), mock, testNukeBackupBucketName, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metadata) != 2 {
		t.Fatalf("expected 2 metadata entries (1 version + 1 marker), got %d", len(metadata))
	}
	markerEntry := metadata[1]
	if markerEntry["delete_marker"] != true {
		t.Fatalf("expected delete_marker=true in second entry, got: %v", markerEntry)
	}
}

// --- backupDynamoTable ---

func TestBackupDynamoTableScanError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, testNukeBackupTableFile)
	mock := &mockNukeBackupDynamo{
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return nil, errors.New("scan failed")
		},
	}
	if err := backupDynamoTable(context.Background(), mock, testNukeBackupTableName, target); err == nil {
		t.Fatal("expected error from Scan failure")
	}
}

func TestBackupDynamoTableWritesItems(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, testNukeBackupTableFile)
	mock := &mockNukeBackupDynamo{
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{
				Items: []map[string]dbtypes.AttributeValue{
					{"LockID": &dbtypes.AttributeValueMemberS{Value: "lock-id-1"}},
					{"LockID": &dbtypes.AttributeValueMemberS{Value: "lock-id-2"}},
				},
			}, nil
		},
	}
	if err := backupDynamoTable(context.Background(), mock, testNukeBackupTableName, target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf(testNukeBackupFileNotCreatedErrorf, err)
	}
	content := string(data)
	if !strings.Contains(content, "lock-id-1") || !strings.Contains(content, "lock-id-2") {
		t.Fatalf("expected both lock IDs in JSON, got: %s", content)
	}
}

func TestBackupDynamoTablePaginates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, testNukeBackupTableFile)
	calls := 0
	pageKey := map[string]dbtypes.AttributeValue{
		"LockID": &dbtypes.AttributeValueMemberS{Value: "page-key"},
	}
	mock := &mockNukeBackupDynamo{
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			calls++
			if calls == 1 {
				return &dynamodb.ScanOutput{
					Items: []map[string]dbtypes.AttributeValue{
						{"LockID": &dbtypes.AttributeValueMemberS{Value: "page1-item"}},
					},
					LastEvaluatedKey: pageKey,
				}, nil
			}
			return &dynamodb.ScanOutput{
				Items: []map[string]dbtypes.AttributeValue{
					{"LockID": &dbtypes.AttributeValueMemberS{Value: "page2-item"}},
				},
			}, nil
		},
	}
	if err := backupDynamoTable(context.Background(), mock, testNukeBackupTableName, target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 scan calls, got %d", calls)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf(testNukeBackupFileNotCreatedErrorf, err)
	}
	content := string(data)
	if !strings.Contains(content, "page1-item") || !strings.Contains(content, "page2-item") {
		t.Fatalf("expected items from both pages in JSON, got: %s", content)
	}
}
