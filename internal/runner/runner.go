// Package runner isolates every external process flux-stratio spawns
// (flux-operator, flux, helm, kubectl) behind a small interface, so
// internal/render and internal/prepare can be tested with no binaries
// installed on the machine, and provides Preflight to check for all four
// once, up front, with an actionable error.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// SubprocessTimeout bounds every external process Exec runs, so a hung
// helm or flux-operator can't block a migration forever.
const SubprocessTimeout = 10 * time.Minute

// WithDefaultTimeout returns ctx bounded by d, unless ctx already carries
// an earlier deadline of its own.
func WithDefaultTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= d {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// Runner runs an external command and returns its captured stdout and
// stderr separately — never merged. The Python client captured a
// subprocess's stdout and stderr together (`stderr=STDOUT` in
// cmd_steps.py's execute_app_migration_task) and then parsed the combined
// stream as a patch YAML document, so any unsuppressed diagnostic line
// corrupted it. Every caller here gets the two streams apart and decides
// for itself what to do with stderr.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// Exec is the production Runner: it invokes real subprocesses via os/exec.
type Exec struct{}

// Run implements Runner.
func (Exec) Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := WithDefaultTimeout(ctx, SubprocessTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name/args come from this plugin's own fixed call sites, never user input
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	runErr := cmd.Run()
	stdout, stderr = outBuf.Bytes(), errBuf.Bytes()
	if runErr != nil {
		return stdout, stderr, fmt.Errorf("running %q: %w", strings.TrimSpace(name+" "+strings.Join(args, " ")), runErr)
	}
	return stdout, stderr, nil
}
