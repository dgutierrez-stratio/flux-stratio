package runner

import (
	"context"
	"testing"
	"time"
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

// TestExec_CancelledContextStopsTheProcess: a cancelled command (Ctrl-C)
// never waits for its subprocess to finish on its own.
func TestExec_CancelledContextStopsTheProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, _, err := (Exec{}).Run(ctx, "sleep", "10"); err == nil {
		t.Fatal("Run with a cancelled context: got nil error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %s, want it stopped right away", elapsed)
	}
}

func TestWithDefaultTimeout(t *testing.T) {
	ctx, cancel := WithDefaultTimeout(context.Background(), time.Minute)
	defer cancel()
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Minute {
		t.Errorf("no deadline, or a later one than the default: %v, %v", deadline, ok)
	}

	early, cancelEarly := context.WithTimeout(context.Background(), time.Second)
	defer cancelEarly()
	ctx, cancel = WithDefaultTimeout(early, time.Minute)
	defer cancel()
	if deadline, _ := ctx.Deadline(); time.Until(deadline) > time.Second {
		t.Errorf("an earlier deadline of the caller's was extended to %v", deadline)
	}
}
