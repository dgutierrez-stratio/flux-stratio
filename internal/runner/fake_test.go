package runner

import (
	"context"
	"errors"
	"testing"
)

func TestFake_ReturnsConfiguredResponse(t *testing.T) {
	f := &Fake{Responses: map[string]FakeResponse{
		"flux": {Stdout: []byte("flux version 2.9.2\n")},
	}}
	stdout, stderr, err := f.Run(context.Background(), "flux", "--version")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if string(stdout) != "flux version 2.9.2\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestFake_RecordsCalls(t *testing.T) {
	f := &Fake{Responses: map[string]FakeResponse{"flux": {}}}
	_, _, _ = f.Run(context.Background(), "flux", "build", "kustomization", "--dry-run")

	if len(f.Calls) != 1 {
		t.Fatalf("len(Calls) = %d, want 1", len(f.Calls))
	}
	if f.Calls[0].Name != "flux" || len(f.Calls[0].Args) != 3 {
		t.Errorf("Calls[0] = %+v, unexpected", f.Calls[0])
	}
}

func TestFake_UnconfiguredBinaryErrors(t *testing.T) {
	f := &Fake{}
	_, _, err := f.Run(context.Background(), "helm")
	if err == nil {
		t.Fatal("Run for an unconfigured binary: got nil error, want non-nil")
	}
}

func TestFake_PropagatesConfiguredError(t *testing.T) {
	wantErr := errors.New("boom")
	f := &Fake{Responses: map[string]FakeResponse{"kubectl": {Err: wantErr}}}
	_, _, err := f.Run(context.Background(), "kubectl")
	if !errors.Is(err, wantErr) {
		t.Errorf("Run error = %v, want %v", err, wantErr)
	}
}
