package cmd

import (
	"context"
	"errors"
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

// --- mock types ---

type mockNukeResetDynamoAPI struct {
	describeFn func(ctx context.Context, input *dynamodb.DescribeTableInput, opts ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	scanFn     func(ctx context.Context, input *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	deleteFn   func(ctx context.Context, input *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

func (m *mockNukeResetDynamoAPI) DescribeTable(ctx context.Context, input *dynamodb.DescribeTableInput, opts ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	return m.describeFn(ctx, input, opts...)
}

func (m *mockNukeResetDynamoAPI) Scan(ctx context.Context, input *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return m.scanFn(ctx, input, opts...)
}

func (m *mockNukeResetDynamoAPI) DeleteItem(ctx context.Context, input *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, input, opts...)
	}
	return &dynamodb.DeleteItemOutput{}, nil
}

type mockNukeResetS3API struct {
	headFn   func(ctx context.Context, input *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	listFn   func(ctx context.Context, input *s3.ListObjectVersionsInput, opts ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	getFn    func(ctx context.Context, input *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	deleteFn func(ctx context.Context, input *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

func (m *mockNukeResetS3API) HeadBucket(ctx context.Context, input *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return m.headFn(ctx, input, opts...)
}

func (m *mockNukeResetS3API) ListObjectVersions(ctx context.Context, input *s3.ListObjectVersionsInput, opts ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	if m.listFn != nil {
		return m.listFn(ctx, input, opts...)
	}
	return &s3.ListObjectVersionsOutput{}, nil
}

func (m *mockNukeResetS3API) GetObject(ctx context.Context, input *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if m.getFn != nil {
		return m.getFn(ctx, input, opts...)
	}
	return &s3.GetObjectOutput{}, nil
}

func (m *mockNukeResetS3API) DeleteObject(ctx context.Context, input *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, input, opts...)
	}
	return &s3.DeleteObjectOutput{}, nil
}

// --- removeLocalTerraformArtifacts ---

func TestRemoveLocalTerraformArtifactsRemovesExistingDirAndFile(t *testing.T) {
	dir := t.TempDir()
	tfDir := filepath.Join(dir, ".terraform")
	lockFile := filepath.Join(dir, ".terraform.tfstate.lock.info")

	if err := os.MkdirAll(tfDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(lockFile, []byte("lock"), 0o644); err != nil {
		t.Fatalf("writefile: %v", err)
	}

	if err := removeLocalTerraformArtifacts(dir); err != nil {
		t.Fatalf("removeLocalTerraformArtifacts: %v", err)
	}

	if _, err := os.Stat(tfDir); !os.IsNotExist(err) {
		t.Error(".terraform dir should have been removed")
	}
	if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
		t.Error(".terraform.tfstate.lock.info should have been removed")
	}
}

func TestRemoveLocalTerraformArtifactsIdempotentOnMissingDir(t *testing.T) {
	dir := t.TempDir()
	// Neither .terraform nor the lock file exist — os.RemoveAll is idempotent.
	if err := removeLocalTerraformArtifacts(dir); err != nil {
		t.Fatalf("removeLocalTerraformArtifacts on non-existent targets: %v", err)
	}
}

// --- findMatchingLockItems ---

func TestFindMatchingLockItemsTableNotFound(t *testing.T) {
	mock := &mockNukeResetDynamoAPI{
		describeFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return nil, &dbtypes.ResourceNotFoundException{Message: sdkaws.String("not found")}
		},
	}
	items, err := findMatchingLockItems(context.Background(), mock, "tbl", "bucket", "key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items != nil {
		t.Fatalf("expected nil items, got %v", items)
	}
}

func TestFindMatchingLockItemsDescribeError(t *testing.T) {
	mock := &mockNukeResetDynamoAPI{
		describeFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return nil, errors.New("access denied")
		},
	}
	_, err := findMatchingLockItems(context.Background(), mock, "tbl", "bucket", "key")
	if err == nil {
		t.Fatal("expected error from describe")
	}
}

func TestFindMatchingLockItemsReturnsMatchingItems(t *testing.T) {
	stateKey := "prod/terraform.tfstate"
	bucket := "state-bucket"
	matchingItem := map[string]dbtypes.AttributeValue{
		"LockID": &dbtypes.AttributeValueMemberS{Value: bucket + "/" + stateKey},
	}
	nonMatchingItem := map[string]dbtypes.AttributeValue{
		"LockID": &dbtypes.AttributeValueMemberS{Value: "other-bucket/other-key"},
	}

	mock := &mockNukeResetDynamoAPI{
		describeFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{
				Items: []map[string]dbtypes.AttributeValue{matchingItem, nonMatchingItem},
			}, nil
		},
	}

	items, err := findMatchingLockItems(context.Background(), mock, "tbl", bucket, stateKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 matching item, got %d", len(items))
	}
}

func TestFindMatchingLockItemsScanError(t *testing.T) {
	mock := &mockNukeResetDynamoAPI{
		describeFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return nil, errors.New("scan failed")
		},
	}
	_, err := findMatchingLockItems(context.Background(), mock, "tbl", "bucket", "key")
	if err == nil || !strings.Contains(err.Error(), "scan failed") {
		t.Fatalf("expected scan error, got: %v", err)
	}
}

func TestFindMatchingLockItemsPaginates(t *testing.T) {
	stateKey := "prod/terraform.tfstate"
	bucket := "state-bucket"
	item := map[string]dbtypes.AttributeValue{
		"LockID": &dbtypes.AttributeValueMemberS{Value: bucket + "/" + stateKey},
	}
	paginationKey := map[string]dbtypes.AttributeValue{
		"LockID": &dbtypes.AttributeValueMemberS{Value: "page-token"},
	}

	calls := 0
	mock := &mockNukeResetDynamoAPI{
		describeFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, input *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			calls++
			if calls == 1 {
				return &dynamodb.ScanOutput{
					Items:            []map[string]dbtypes.AttributeValue{item},
					LastEvaluatedKey: paginationKey,
				}, nil
			}
			return &dynamodb.ScanOutput{
				Items: []map[string]dbtypes.AttributeValue{item},
			}, nil
		},
	}

	items, err := findMatchingLockItems(context.Background(), mock, "tbl", bucket, stateKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items across pages, got %d", len(items))
	}
}

// --- backupLockItems ---

func TestBackupLockItemsWritesJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock-items.json")

	items := []map[string]dbtypes.AttributeValue{
		{"LockID": &dbtypes.AttributeValueMemberS{Value: "mylock"}},
	}

	if err := backupLockItems(items, path); err != nil {
		t.Fatalf("backupLockItems: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "mylock") {
		t.Errorf("expected lock ID in output, got: %s", string(data))
	}
}

func TestBackupLockItemsWritesEmptySlice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")

	if err := backupLockItems([]map[string]dbtypes.AttributeValue{}, path); err != nil {
		t.Fatalf("backupLockItems with empty slice: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := strings.TrimSpace(string(data))
	if content != "[]" {
		t.Errorf("expected empty JSON array, got: %s", content)
	}
}

// --- nukeCleanupTargetLess: same rank, different type ---

func TestNukeCleanupTargetLessSameRankDifferentType(t *testing.T) {
	// Two unknown resource types that both have rank=20 but different type strings.
	a := auditResource{resourceType: "alpha/resource", name: "same-name"}
	b := auditResource{resourceType: "zeta/resource", name: "same-name"}
	if !nukeCleanupTargetLess(a, b) {
		t.Fatal("'alpha/resource' should sort before 'zeta/resource' with same name and rank")
	}
	if nukeCleanupTargetLess(b, a) {
		t.Fatal("'zeta/resource' should not sort before 'alpha/resource'")
	}
}

// --- checkStateBackendExists ---

func TestCheckStateBackendExistsReturnsTrueOnSuccess(t *testing.T) {
	old := newNukeBackendResetS3ClientFn
	t.Cleanup(func() { newNukeBackendResetS3ClientFn = old })
	newNukeBackendResetS3ClientFn = func(_ sdkaws.Config) nukeBackendResetS3API {
		return &mockNukeResetS3API{
			headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
				return &s3.HeadBucketOutput{}, nil
			},
		}
	}

	exists, err := checkStateBackendExists(context.Background(), sdkaws.Config{}, "myorg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true when HeadBucket succeeds")
	}
}

func TestCheckStateBackendExistsReturnsFalseOnNoSuchBucket(t *testing.T) {
	old := newNukeBackendResetS3ClientFn
	t.Cleanup(func() { newNukeBackendResetS3ClientFn = old })
	newNukeBackendResetS3ClientFn = func(_ sdkaws.Config) nukeBackendResetS3API {
		return &mockNukeResetS3API{
			headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
				return nil, &s3types.NoSuchBucket{}
			},
		}
	}

	exists, err := checkStateBackendExists(context.Background(), sdkaws.Config{}, "myorg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false when NoSuchBucket is returned")
	}
}

func TestCheckStateBackendExistsReturnsErrorOnOtherError(t *testing.T) {
	old := newNukeBackendResetS3ClientFn
	t.Cleanup(func() { newNukeBackendResetS3ClientFn = old })
	newNukeBackendResetS3ClientFn = func(_ sdkaws.Config) nukeBackendResetS3API {
		return &mockNukeResetS3API{
			headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
				return nil, errors.New("network error")
			},
		}
	}

	exists, err := checkStateBackendExists(context.Background(), sdkaws.Config{}, "myorg")
	if err == nil {
		t.Fatal("expected error on unexpected HeadBucket failure")
	}
	if exists {
		t.Fatal("expected exists=false on error")
	}
}
