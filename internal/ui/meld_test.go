package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readingFakeRunner reads its two file-path arguments immediately (as the
// real meld process would, before Meld's caller can clean them up) and
// records their content for the test to assert on afterward.
type readingFakeRunner struct {
	calledWith    []string
	leftContents  string
	rightContents string
	err           error
}

func (r *readingFakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	r.calledWith = append([]string{name}, args...)
	if r.err != nil {
		return nil, nil, r.err
	}
	left, err := os.ReadFile(args[0])
	if err != nil {
		return nil, nil, err
	}
	right, err := os.ReadFile(args[1])
	if err != nil {
		return nil, nil, err
	}
	r.leftContents, r.rightContents = string(left), string(right)
	return nil, nil, nil
}

func TestMeld_WritesTempFilesAndRunsMeld(t *testing.T) {
	r := &readingFakeRunner{}
	if err := Meld(context.Background(), r, "desired", "desired-content\n", "live", "live-content\n"); err != nil {
		t.Fatalf("Meld returned error: %v", err)
	}

	if len(r.calledWith) != 3 || r.calledWith[0] != "meld" {
		t.Fatalf("calledWith = %v, want [meld <left> <right>]", r.calledWith)
	}
	if r.leftContents != "desired-content\n" {
		t.Errorf("left file content = %q", r.leftContents)
	}
	if r.rightContents != "live-content\n" {
		t.Errorf("right file content = %q", r.rightContents)
	}

	leftFile, rightFile := r.calledWith[1], r.calledWith[2]
	if !strings.HasPrefix(filepath.Base(leftFile), "desired-") {
		t.Errorf("left temp file = %q, want it named after the desired label", leftFile)
	}
	if !strings.HasPrefix(filepath.Base(rightFile), "live-") {
		t.Errorf("right temp file = %q, want it named after the live label", rightFile)
	}
	if _, err := os.Stat(leftFile); !os.IsNotExist(err) {
		t.Errorf("left temp file was not removed after Meld returned")
	}
	if _, err := os.Stat(rightFile); !os.IsNotExist(err) {
		t.Errorf("right temp file was not removed after Meld returned")
	}
}

func TestMeld_RunnerErrorPropagates(t *testing.T) {
	r := &readingFakeRunner{err: context.DeadlineExceeded}
	if err := Meld(context.Background(), r, "left", "a", "right", "b"); err == nil {
		t.Fatal("Meld with a failing runner: got nil error, want non-nil")
	}
}
