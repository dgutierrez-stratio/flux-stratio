package reporequire

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate_AllPresent(t *testing.T) {
	base := t.TempDir()
	for _, dir := range Dirs {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := Validate(base); err != nil {
		t.Errorf("Validate returned error: %v", err)
	}
}

func TestValidate_EmptyBase(t *testing.T) {
	if err := Validate(""); err == nil || !strings.Contains(err.Error(), "--base") {
		t.Errorf("Validate(\"\") = %v, want an error mentioning --base", err)
	}
}

func TestValidate_MissingDirsNamed(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "keos-apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Validate(base)
	if err == nil {
		t.Fatal("Validate with 3 of 4 dirs missing: got nil error, want non-nil")
	}
	for _, want := range []string{"keos-use-cases", "keos-fleet", "keos-system-services"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "keos-apps,") {
		t.Errorf("error %q should not list keos-apps as missing", err)
	}
}
