package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteYAMLFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	if err := writeYAMLFile(dir, "cr.yaml", map[string]any{"kind": "PgCluster"}); err != nil {
		t.Fatalf("writeYAMLFile returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "cr.yaml"))
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(data) != "kind: PgCluster\n" {
		t.Errorf("content = %q, want %q", data, "kind: PgCluster\n")
	}
}

func TestWriteEnvFile_SortedKeyValueLines(t *testing.T) {
	dir := t.TempDir()
	if err := writeEnvFile(dir, "env-vars.env", map[string]string{"B": "2", "A": "1"}); err != nil {
		t.Fatalf("writeEnvFile returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "env-vars.env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "A=1\nB=2\n" {
		t.Errorf("content = %q, want %q", data, "A=1\nB=2\n")
	}
}

func TestWriteFile_CreatesNestedDirectories(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := writeFile(dir, "x.txt", []byte("hi")); err != nil {
		t.Fatalf("writeFile returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); err != nil {
		t.Errorf("file not created: %v", err)
	}
}
