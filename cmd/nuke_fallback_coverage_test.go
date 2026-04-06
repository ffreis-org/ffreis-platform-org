package cmd

import (
	"path/filepath"
	"testing"
)

func TestParseBackendConfigFileReadsKeyValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backend.hcl")
	writeFile(t, path, `
# This is a comment
bucket = "my-state-bucket"
dynamodb_table = "my-lock-table"
key = "prod/terraform.tfstate"
`)
	got, err := parseBackendConfigFile(path)
	if err != nil {
		t.Fatalf("parseBackendConfigFile: %v", err)
	}
	if got["bucket"] != "my-state-bucket" {
		t.Errorf("bucket: %q", got["bucket"])
	}
	if got["dynamodb_table"] != "my-lock-table" {
		t.Errorf("dynamodb_table: %q", got["dynamodb_table"])
	}
	if got["key"] != "prod/terraform.tfstate" {
		t.Errorf("key: %q", got["key"])
	}
}

func TestParseBackendConfigFileIgnoresCommentLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backend.hcl")
	writeFile(t, path, `
# comment line
// another comment
bucket = "bucket-name"
`)
	got, err := parseBackendConfigFile(path)
	if err != nil {
		t.Fatalf("parseBackendConfigFile: %v", err)
	}
	if len(got) != 1 || got["bucket"] != "bucket-name" {
		t.Errorf("unexpected map: %v", got)
	}
}

func TestParseBackendConfigFileIgnoresLinesWithoutSeparator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backend.hcl")
	writeFile(t, path, `
no_separator_line
bucket = "ok"
`)
	got, err := parseBackendConfigFile(path)
	if err != nil {
		t.Fatalf("parseBackendConfigFile: %v", err)
	}
	// Only "bucket" should be parsed; the no-separator line is skipped.
	if _, ok := got["no_separator_line"]; ok {
		t.Error("line without separator should not be parsed")
	}
	if got["bucket"] != "ok" {
		t.Errorf("bucket: %q", got["bucket"])
	}
}

func TestParseBackendConfigFileReturnsErrorForMissingFile(t *testing.T) {
	_, err := parseBackendConfigFile("/no/such/file.hcl")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadBackendStateConfigForNukeReturnsConfig(t *testing.T) {
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	// Write a backend.local.hcl in the stack dir.
	writeFile(t, filepath.Join(stack, "backend.local.hcl"), `
bucket = "state-bucket"
dynamodb_table = "lock-table"
`)
	// Write a backend.hcl in the env dir.
	writeFile(t, filepath.Join(root, envsDirName, testEnv, "backend.hcl"), `
key = "prod/terraform.tfstate"
`)

	cfg, err := loadBackendStateConfigForNuke(root, testEnv)
	if err != nil {
		t.Fatalf("loadBackendStateConfigForNuke: %v", err)
	}
	if cfg.BucketName != "state-bucket" {
		t.Errorf("BucketName: %q", cfg.BucketName)
	}
	if cfg.TableName != "lock-table" {
		t.Errorf("TableName: %q", cfg.TableName)
	}
	if cfg.StateKey != "prod/terraform.tfstate" {
		t.Errorf("StateKey: %q", cfg.StateKey)
	}
}

func TestLoadBackendStateConfigForNukeReturnsErrorWhenIncomplete(t *testing.T) {
	root := t.TempDir()
	stack := initRepoLayout(t, root, testEnv)
	// backend.local.hcl present but missing dynamodb_table.
	writeFile(t, filepath.Join(stack, "backend.local.hcl"), `bucket = "only-bucket"`)
	writeFile(t, filepath.Join(root, envsDirName, testEnv, "backend.hcl"), `key = "prod/terraform.tfstate"`)

	_, err := loadBackendStateConfigForNuke(root, testEnv)
	if err == nil {
		t.Fatal("expected error for incomplete backend config")
	}
}
