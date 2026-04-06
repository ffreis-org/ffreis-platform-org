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
	testExpectedNonEmptyTargetDir = "expected non-empty targetDir"
	testNukeFallbackBucketName    = "my-bucket"
	testNukeFallbackTableName     = "my-table"
	testNukeFallbackDeleteFailed  = "delete failed"
	testNukeFallbackLockID        = "lock-1"
	testNukeFallbackStateKey      = "state.tfstate"
)

// --- listStateObjectVersions ---

func TestListStateObjectVersionsBucketMissing(t *testing.T) {
	t.Parallel()
	mock := &mockNukeResetS3API{
		headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return nil, &s3types.NotFound{}
		},
	}
	versions, markers, err := listStateObjectVersions(context.Background(), mock, "missing-bucket", testNukeFallbackStateKey)
	if err != nil {
		t.Fatalf("expected nil error for missing bucket, got: %v", err)
	}
	if versions != nil || markers != nil {
		t.Fatalf("expected nil slices for missing bucket, got versions=%v markers=%v", versions, markers)
	}
}

func TestListStateObjectVersionsHeadBucketError(t *testing.T) {
	t.Parallel()
	mock := &mockNukeResetS3API{
		headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return nil, errors.New("access denied")
		},
	}
	_, _, err := listStateObjectVersions(context.Background(), mock, testNukeFallbackBucketName, testNukeFallbackStateKey)
	if err == nil {
		t.Fatal("expected error from HeadBucket failure")
	}
}

func TestListStateObjectVersionsReturnsMatchingVersionsAndMarkers(t *testing.T) {
	t.Parallel()
	stateKey := "prod/terraform.tfstate"
	mock := &mockNukeResetS3API{
		headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		listFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			return &s3.ListObjectVersionsOutput{
				Versions: []s3types.ObjectVersion{
					{Key: sdkaws.String(stateKey), VersionId: sdkaws.String("v1")},
					{Key: sdkaws.String("other-key"), VersionId: sdkaws.String("v-other")},
				},
				DeleteMarkers: []s3types.DeleteMarkerEntry{
					{Key: sdkaws.String(stateKey), VersionId: sdkaws.String("m1")},
					{Key: sdkaws.String("other-key"), VersionId: sdkaws.String("m-other")},
				},
				IsTruncated: sdkaws.Bool(false),
			}, nil
		},
	}
	versions, markers, err := listStateObjectVersions(context.Background(), mock, testNukeFallbackBucketName, stateKey)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected 1 matching version, got %d", len(versions))
	}
	if len(markers) != 1 {
		t.Fatalf("expected 1 matching marker, got %d", len(markers))
	}
}

func TestListStateObjectVersionsPaginates(t *testing.T) {
	t.Parallel()
	stateKey := "prod/terraform.tfstate"
	calls := 0
	mock := &mockNukeResetS3API{
		headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		listFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			calls++
			if calls == 1 {
				return &s3.ListObjectVersionsOutput{
					Versions:            []s3types.ObjectVersion{{Key: sdkaws.String(stateKey), VersionId: sdkaws.String("v1")}},
					IsTruncated:         sdkaws.Bool(true),
					NextKeyMarker:       sdkaws.String(stateKey),
					NextVersionIdMarker: sdkaws.String("v1"),
				}, nil
			}
			return &s3.ListObjectVersionsOutput{
				Versions:    []s3types.ObjectVersion{{Key: sdkaws.String(stateKey), VersionId: sdkaws.String("v2")}},
				IsTruncated: sdkaws.Bool(false),
			}, nil
		},
	}
	versions, _, err := listStateObjectVersions(context.Background(), mock, testNukeFallbackBucketName, stateKey)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 list calls, got %d", calls)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions across pages, got %d", len(versions))
	}
}

func TestListStateObjectVersionsListError(t *testing.T) {
	t.Parallel()
	mock := &mockNukeResetS3API{
		headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		listFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			return nil, errors.New("list failed")
		},
	}
	_, _, err := listStateObjectVersions(context.Background(), mock, testNukeFallbackBucketName, testNukeFallbackStateKey)
	if err == nil {
		t.Fatal("expected error from ListObjectVersions failure")
	}
}

// --- deleteStateVersionsAndMarkers ---

func TestDeleteStateVersionsAndMarkersEmptyLists(t *testing.T) {
	t.Parallel()
	mock := &mockNukeResetS3API{
		headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
	}
	cfg := nukeBackendStateConfig{BucketName: "bucket", TableName: "table", StateKey: "key"}
	deletedVersions, deletedMarkers, err := deleteStateVersionsAndMarkers(context.Background(), mock, cfg, nil, nil)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if deletedVersions != 0 || deletedMarkers != 0 {
		t.Fatalf("expected (0, 0), got (%d, %d)", deletedVersions, deletedMarkers)
	}
}

func TestDeleteStateVersionsAndMarkersDeletesVersionsAndMarkers(t *testing.T) {
	t.Parallel()
	deleteCalls := 0
	mock := &mockNukeResetS3API{
		headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		deleteFn: func(_ context.Context, _ *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
			deleteCalls++
			return &s3.DeleteObjectOutput{}, nil
		},
	}
	cfg := nukeBackendStateConfig{BucketName: "bucket", TableName: "table", StateKey: testNukeFallbackStateKey}
	versions := []s3types.ObjectVersion{
		{Key: sdkaws.String(testNukeFallbackStateKey), VersionId: sdkaws.String("v1")},
		{Key: sdkaws.String(testNukeFallbackStateKey), VersionId: sdkaws.String("v2")},
	}
	markers := []s3types.DeleteMarkerEntry{
		{Key: sdkaws.String(testNukeFallbackStateKey), VersionId: sdkaws.String("m1")},
	}
	deletedVersions, deletedMarkers, err := deleteStateVersionsAndMarkers(context.Background(), mock, cfg, versions, markers)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if deletedVersions != 2 {
		t.Fatalf("expected 2 deleted versions, got %d", deletedVersions)
	}
	if deletedMarkers != 1 {
		t.Fatalf("expected 1 deleted marker, got %d", deletedMarkers)
	}
	if deleteCalls != 3 {
		t.Fatalf("expected 3 delete calls, got %d", deleteCalls)
	}
}

func TestDeleteStateVersionsAndMarkersVersionDeleteError(t *testing.T) {
	t.Parallel()
	mock := &mockNukeResetS3API{
		headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		deleteFn: func(_ context.Context, _ *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
			return nil, errors.New(testNukeFallbackDeleteFailed)
		},
	}
	cfg := nukeBackendStateConfig{BucketName: "bucket", TableName: "table", StateKey: testNukeFallbackStateKey}
	versions := []s3types.ObjectVersion{
		{Key: sdkaws.String(testNukeFallbackStateKey), VersionId: sdkaws.String("v1")},
	}
	_, _, err := deleteStateVersionsAndMarkers(context.Background(), mock, cfg, versions, nil)
	if err == nil {
		t.Fatal("expected error from version delete failure")
	}
	if !strings.Contains(err.Error(), "v1") {
		t.Fatalf("expected version ID in error, got: %v", err)
	}
}

func TestDeleteStateVersionsAndMarkersMarkerDeleteError(t *testing.T) {
	t.Parallel()
	mock := &mockNukeResetS3API{
		headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		deleteFn: func(_ context.Context, _ *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
			return nil, errors.New(testNukeFallbackDeleteFailed)
		},
	}
	cfg := nukeBackendStateConfig{BucketName: "bucket", TableName: "table", StateKey: testNukeFallbackStateKey}
	markers := []s3types.DeleteMarkerEntry{
		{Key: sdkaws.String(testNukeFallbackStateKey), VersionId: sdkaws.String("m1")},
	}
	_, _, err := deleteStateVersionsAndMarkers(context.Background(), mock, cfg, nil, markers)
	if err == nil {
		t.Fatal("expected error from marker delete failure")
	}
	if !strings.Contains(err.Error(), "m1") {
		t.Fatalf("expected marker ID in error, got: %v", err)
	}
}

// --- deleteLockRows ---

func TestDeleteLockRowsEmptyItems(t *testing.T) {
	t.Parallel()
	mock := &mockNukeResetDynamoAPI{
		describeFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{}, nil
		},
	}
	deleted, err := deleteLockRows(context.Background(), mock, testNukeFallbackTableName, nil)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if deleted != 0 {
		t.Fatalf("expected 0 deleted, got %d", deleted)
	}
}

func TestDeleteLockRowsDeletesItemsWithLockID(t *testing.T) {
	t.Parallel()
	deleteCalls := 0
	mock := &mockNukeResetDynamoAPI{
		describeFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{}, nil
		},
		deleteFn: func(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
			deleteCalls++
			return &dynamodb.DeleteItemOutput{}, nil
		},
	}
	items := []map[string]dbtypes.AttributeValue{
		{"LockID": &dbtypes.AttributeValueMemberS{Value: testNukeFallbackLockID}},
		{"LockID": &dbtypes.AttributeValueMemberS{Value: "lock-2"}},
	}
	deleted, err := deleteLockRows(context.Background(), mock, testNukeFallbackTableName, items)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if deleted != 2 {
		t.Fatalf("expected 2 deleted, got %d", deleted)
	}
	if deleteCalls != 2 {
		t.Fatalf("expected 2 delete calls, got %d", deleteCalls)
	}
}

func TestDeleteLockRowsSkipsItemsWithNoLockID(t *testing.T) {
	t.Parallel()
	deleteCalls := 0
	mock := &mockNukeResetDynamoAPI{
		describeFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{}, nil
		},
		deleteFn: func(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
			deleteCalls++
			return &dynamodb.DeleteItemOutput{}, nil
		},
	}
	items := []map[string]dbtypes.AttributeValue{
		{"OtherField": &dbtypes.AttributeValueMemberS{Value: "val"}},
		{"LockID": &dbtypes.AttributeValueMemberS{Value: testNukeFallbackLockID}},
	}
	deleted, err := deleteLockRows(context.Background(), mock, testNukeFallbackTableName, items)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted (skipping item without LockID), got %d", deleted)
	}
	if deleteCalls != 1 {
		t.Fatalf("expected 1 delete call, got %d", deleteCalls)
	}
}

func TestDeleteLockRowsDeleteError(t *testing.T) {
	t.Parallel()
	mock := &mockNukeResetDynamoAPI{
		describeFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{}, nil
		},
		deleteFn: func(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
			return nil, errors.New(testNukeFallbackDeleteFailed)
		},
	}
	items := []map[string]dbtypes.AttributeValue{
		{"LockID": &dbtypes.AttributeValueMemberS{Value: testNukeFallbackLockID}},
	}
	_, err := deleteLockRows(context.Background(), mock, testNukeFallbackTableName, items)
	if err == nil {
		t.Fatal("expected error from DeleteItem failure")
	}
}

func TestDeleteLockRowsNotFoundTreatedAsDeleted(t *testing.T) {
	t.Parallel()
	mock := &mockNukeResetDynamoAPI{
		describeFn: func(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
			return &dynamodb.DescribeTableOutput{}, nil
		},
		scanFn: func(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
			return &dynamodb.ScanOutput{}, nil
		},
		deleteFn: func(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
			return nil, errors.New("resource not found")
		},
	}
	items := []map[string]dbtypes.AttributeValue{
		{"LockID": &dbtypes.AttributeValueMemberS{Value: testNukeFallbackLockID}},
	}
	deleted, err := deleteLockRows(context.Background(), mock, testNukeFallbackTableName, items)
	if err != nil {
		t.Fatalf("not-found should be treated as deleted, got: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted for not-found case, got %d", deleted)
	}
}

// --- backupBackendStateData ---

func TestBackupBackendStateDataEmptyInputReturnsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := nukeBackendStateConfig{BucketName: "bucket", TableName: "table", StateKey: testNukeFallbackStateKey}
	targetDir, err := backupBackendStateData(context.Background(), nil, cfg, nil, nil, nil, dir)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if targetDir != "" {
		t.Fatalf("expected empty targetDir for empty input, got: %s", targetDir)
	}
}

func TestBackupBackendStateDataWithStateVersionsCreatesBackup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateKey := "platform-org/terraform.tfstate"
	s3Mock := &mockNukeResetS3API{
		headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		listFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			return &s3.ListObjectVersionsOutput{
				Versions: []s3types.ObjectVersion{
					{Key: sdkaws.String(stateKey), VersionId: sdkaws.String("v1"), IsLatest: sdkaws.Bool(true), Size: sdkaws.Int64(128)},
				},
				IsTruncated: sdkaws.Bool(false),
			}, nil
		},
		getFn: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body: io.NopCloser(bytes.NewBufferString(`{"version":4}`)),
			}, nil
		},
	}
	cfg := nukeBackendStateConfig{BucketName: testNukeFallbackBucketName, TableName: "lock-table", StateKey: stateKey}
	stateVersions := []s3types.ObjectVersion{
		{Key: sdkaws.String(stateKey), VersionId: sdkaws.String("v1")},
	}
	targetDir, err := backupBackendStateData(context.Background(), s3Mock, cfg, stateVersions, nil, nil, dir)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if targetDir == "" {
		t.Fatal(testExpectedNonEmptyTargetDir)
	}
	if _, statErr := os.Stat(targetDir); statErr != nil {
		t.Fatalf("backup dir not created: %v", statErr)
	}
	// manifest.json should be written
	manifestPath := filepath.Join(targetDir, "s3", "manifest.json")
	if _, statErr := os.Stat(manifestPath); statErr != nil {
		t.Fatalf("s3 manifest not created: %v", statErr)
	}
}

func TestBackupBackendStateDataWithLockItemsCreatesBackup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := nukeBackendStateConfig{BucketName: "bucket", TableName: "table", StateKey: testNukeFallbackStateKey}
	lockItems := []map[string]dbtypes.AttributeValue{
		{"LockID": &dbtypes.AttributeValueMemberS{Value: "lock-123"}},
	}
	targetDir, err := backupBackendStateData(context.Background(), nil, cfg, nil, nil, lockItems, dir)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if targetDir == "" {
		t.Fatal(testExpectedNonEmptyTargetDir)
	}
	lockBackupPath := filepath.Join(targetDir, "dynamodb", "lock-items.json")
	data, readErr := os.ReadFile(lockBackupPath)
	if readErr != nil {
		t.Fatalf("lock items backup not created: %v", readErr)
	}
	if !strings.Contains(string(data), "lock-123") {
		t.Fatalf("expected lock ID in backup, got: %s", string(data))
	}
}

func TestBackupBackendStateDataWithDeleteMarkers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateKey := "platform-org/terraform.tfstate"
	s3Mock := &mockNukeResetS3API{
		headFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
		listFn: func(_ context.Context, _ *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
			return &s3.ListObjectVersionsOutput{
				DeleteMarkers: []s3types.DeleteMarkerEntry{
					{Key: sdkaws.String(stateKey), VersionId: sdkaws.String("m1"), IsLatest: sdkaws.Bool(true)},
				},
				IsTruncated: sdkaws.Bool(false),
			}, nil
		},
	}
	cfg := nukeBackendStateConfig{BucketName: testNukeFallbackBucketName, TableName: "lock-table", StateKey: stateKey}
	deleteMarkers := []s3types.DeleteMarkerEntry{
		{Key: sdkaws.String(stateKey), VersionId: sdkaws.String("m1")},
	}
	targetDir, err := backupBackendStateData(context.Background(), s3Mock, cfg, nil, deleteMarkers, nil, dir)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if targetDir == "" {
		t.Fatal(testExpectedNonEmptyTargetDir)
	}
	manifestPath := filepath.Join(targetDir, "s3", "manifest.json")
	if _, statErr := os.Stat(manifestPath); statErr != nil {
		t.Fatalf("s3 manifest not created: %v", statErr)
	}
}
