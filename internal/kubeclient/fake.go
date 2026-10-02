package kubeclient

import (
	"context"
	"io"
	"strings"
)

// FakeExecCall records one invocation made through a FakeExecer.
type FakeExecCall struct {
	Namespace, Pod, Container string
	Command                   []string
	Stdin                     string
}

// FakeExecResponse is the canned result a FakeExecer returns.
type FakeExecResponse struct {
	Stdout, Stderr string
	Err            error
}

// FakeExecer is an Execer for tests: it returns a canned FakeExecResponse
// and records every call it receives, so a test can assert both on the
// exit path and on exactly what would have been executed, with no real
// cluster reachable. Mirrors runner.Fake.
type FakeExecer struct {
	Response FakeExecResponse
	Calls    []FakeExecCall
}

// Exec implements Execer.
func (f *FakeExecer) Exec(_ context.Context, namespace, pod, container string, command []string, stdin io.Reader) (string, string, error) {
	var sb strings.Builder
	if stdin != nil {
		_, _ = io.Copy(&sb, stdin)
	}
	f.Calls = append(f.Calls, FakeExecCall{Namespace: namespace, Pod: pod, Container: container, Command: command, Stdin: sb.String()})
	return f.Response.Stdout, f.Response.Stderr, f.Response.Err
}
