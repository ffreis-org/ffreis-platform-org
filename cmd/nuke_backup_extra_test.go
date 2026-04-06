package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// --- runtimeStateBackupPlan.hasData ---

func TestRuntimeStateBackupPlanHasDataFalseWhenAllZero(t *testing.T) {
	plan := runtimeStateBackupPlan{}
	if plan.hasData() {
		t.Fatal("hasData() should be false when all counts are 0")
	}
}

func TestRuntimeStateBackupPlanHasDataTrueForBucketVersionCount(t *testing.T) {
	plan := runtimeStateBackupPlan{BucketVersionCount: 1}
	if !plan.hasData() {
		t.Fatal("hasData() should be true when BucketVersionCount > 0")
	}
}

func TestRuntimeStateBackupPlanHasDataTrueForDeleteMarkerCount(t *testing.T) {
	plan := runtimeStateBackupPlan{DeleteMarkerCount: 1}
	if !plan.hasData() {
		t.Fatal("hasData() should be true when DeleteMarkerCount > 0")
	}
}

func TestRuntimeStateBackupPlanHasDataTrueForTableItemCount(t *testing.T) {
	plan := runtimeStateBackupPlan{TableItemCount: 1}
	if !plan.hasData() {
		t.Fatal("hasData() should be true when TableItemCount > 0")
	}
}

// --- runtimeStateBackupPlan.summaryLines ---

func TestRuntimeStateBackupPlanSummaryLinesReturnsTwoLines(t *testing.T) {
	plan := runtimeStateBackupPlan{
		BucketName:         "my-bucket",
		BucketVersionCount: 3,
		DeleteMarkerCount:  2,
		TableName:          "my-table",
		TableItemCount:     5,
	}
	lines := plan.summaryLines()
	if len(lines) != 2 {
		t.Fatalf("summaryLines() returned %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "my-bucket") || !strings.Contains(lines[0], "3") || !strings.Contains(lines[0], "2") {
		t.Errorf("bucket summary line unexpected: %q", lines[0])
	}
	if !strings.Contains(lines[1], "my-table") || !strings.Contains(lines[1], "5") {
		t.Errorf("table summary line unexpected: %q", lines[1])
	}
}

func TestRuntimeStateBackupPlanSummaryLinesZeroCounts(t *testing.T) {
	plan := runtimeStateBackupPlan{BucketName: "b", TableName: "t"}
	lines := plan.summaryLines()
	if len(lines) != 2 {
		t.Fatalf("summaryLines() returned %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "0") {
		t.Errorf("expected zero counts in bucket line: %q", lines[0])
	}
}

// --- writeJSONFile ---

func TestWriteJSONFileCreatesFileWithValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	value := map[string]any{"key": "value", "num": 42}

	if err := writeJSONFile(path, value); err != nil {
		t.Fatalf("writeJSONFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `"key"`) || !strings.Contains(content, `"value"`) {
		t.Errorf("JSON content unexpected: %q", content)
	}
	// Must end with newline.
	if !strings.HasSuffix(content, "\n") {
		t.Errorf("expected trailing newline, got: %q", content)
	}
}

func TestWriteJSONFileReturnsErrorForBadPath(t *testing.T) {
	err := writeJSONFile("/no/such/directory/out.json", map[string]any{})
	if err == nil {
		t.Fatal("expected error writing to non-existent directory")
	}
}

func TestWriteJSONFileWritesSlice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "list.json")
	if err := writeJSONFile(path, []string{"a", "b"}); err != nil {
		t.Fatalf("writeJSONFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), `"a"`) {
		t.Errorf("unexpected content: %s", string(data))
	}
}

// --- isS3BucketMissing ---

func TestIsS3BucketMissingReturnsTrueForNotFound(t *testing.T) {
	err := &s3types.NotFound{}
	if !isS3BucketMissing(err) {
		t.Fatal("isS3BucketMissing should return true for *s3types.NotFound")
	}
}

func TestIsS3BucketMissingReturnsFalseForOtherError(t *testing.T) {
	if isS3BucketMissing(errors.New("access denied")) {
		t.Fatal("isS3BucketMissing should return false for non-NotFound errors")
	}
}

func TestIsS3BucketMissingReturnsFalseForNil(t *testing.T) {
	if isS3BucketMissing(nil) {
		t.Fatal("isS3BucketMissing should return false for nil")
	}
}
