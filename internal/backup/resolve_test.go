package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMarker(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBaseline_ExactBackupDirUsedAsIs(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "cr.yaml")

	got, err := ResolveBaseline(dir, "psql")
	if err != nil {
		t.Fatalf("ResolveBaseline returned error: %v", err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestResolveBaseline_AppRootPicksLatestTimestamp(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), "psql")
	older := filepath.Join(appDir, "2026-01-01T00-00-00Z")
	newer := filepath.Join(appDir, "2026-06-01T00-00-00Z")
	writeMarker(t, older, "cr.yaml")
	writeMarker(t, newer, "cr.yaml")

	got, err := ResolveBaseline(appDir, "psql")
	if err != nil {
		t.Fatalf("ResolveBaseline returned error: %v", err)
	}
	if got != newer {
		t.Errorf("got %q, want %q (the more recent one)", got, newer)
	}
}

func TestResolveBaseline_BackupsRootFindsAppSubdir(t *testing.T) {
	backupsRoot := t.TempDir()
	appDir := filepath.Join(backupsRoot, "psql")
	only := filepath.Join(appDir, "2026-03-04T10-30-00Z")
	writeMarker(t, only, "env-vars.env")

	got, err := ResolveBaseline(backupsRoot, "psql")
	if err != nil {
		t.Fatalf("ResolveBaseline returned error: %v", err)
	}
	if got != only {
		t.Errorf("got %q, want %q", got, only)
	}
}

func TestResolveBaseline_IgnoresNonTimestampSubdirs(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), "psql")
	valid := filepath.Join(appDir, "2026-01-01T00-00-00Z")
	writeMarker(t, valid, "cr.yaml")
	// A stray directory that doesn't match the timestamp format must
	// never be picked, even though it sorts after the valid one.
	writeMarker(t, filepath.Join(appDir, "zz-not-a-timestamp"), "cr.yaml")

	got, err := ResolveBaseline(appDir, "psql")
	if err != nil {
		t.Fatalf("ResolveBaseline returned error: %v", err)
	}
	if got != valid {
		t.Errorf("got %q, want %q", got, valid)
	}
}

func TestResolveBaseline_NothingFoundErrors(t *testing.T) {
	if _, err := ResolveBaseline(t.TempDir(), "psql"); err == nil {
		t.Fatal("ResolveBaseline with nothing present: got nil error, want non-nil")
	}
}
