package runner

import (
	"context"
	"testing"
)

func TestExec_Run_Success(t *testing.T) {
	stdout, stderr, err := (Exec{}).Run(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := string(stdout); got != "hello\n" {
		t.Errorf("stdout = %q, want %q", got, "hello\n")
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestExec_Run_CapturesStderrSeparately(t *testing.T) {
	// sh writes to stderr and exits non-zero; both streams must come back
	// distinctly, never merged (that is the whole point of this package).
	stdout, stderr, err := (Exec{}).Run(context.Background(), "sh", "-c", "echo out; echo err >&2; exit 3")
	if err == nil {
		t.Fatal("Run with a failing command: got nil error, want non-nil")
	}
	if got := string(stdout); got != "out\n" {
		t.Errorf("stdout = %q, want %q", got, "out\n")
	}
	if got := string(stderr); got != "err\n" {
		t.Errorf("stderr = %q, want %q", got, "err\n")
	}
}

func TestExec_Run_BinaryNotFound(t *testing.T) {
	_, _, err := (Exec{}).Run(context.Background(), "flux-stratio-definitely-not-a-real-binary")
	if err == nil {
		t.Fatal("Run with a nonexistent binary: got nil error, want non-nil")
	}
}
