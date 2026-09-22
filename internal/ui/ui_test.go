package ui

import (
	"bytes"
	"testing"
)

func TestPatch_WritesUnmodified(t *testing.T) {
	var buf bytes.Buffer
	patch := []byte("patches:\n- patch: |\n    foo: bar\n  target:\n    kind: HelmRelease\n")

	if err := Patch(&buf, patch); err != nil {
		t.Fatalf("Patch returned error: %v", err)
	}
	if got := buf.String(); got != string(patch) {
		t.Errorf("Patch wrote %q, want %q", got, string(patch))
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWriteFailed }

var errWriteFailed = errTest("write failed")

type errTest string

func (e errTest) Error() string { return string(e) }

func TestPatch_PropagatesWriteError(t *testing.T) {
	if err := Patch(failingWriter{}, []byte("x")); err == nil {
		t.Error("Patch with a failing writer: got nil error, want non-nil")
	}
}
