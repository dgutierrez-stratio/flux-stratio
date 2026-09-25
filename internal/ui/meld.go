package ui

import (
	"context"
	"fmt"
	"os"

	"github.com/Stratio/flux-stratio/internal/runner"
)

// Meld writes left and right to two temporary files — named after
// leftLabel/rightLabel, so meld's window identifies each side by what it
// actually is (e.g. "rendered" vs "live", or "backup" vs "live") instead
// of a generic "before"/"after", which means something different
// depending on which comparison the caller is running and reads backwards
// for a pre-migration diff (the rendered GitOps state isn't
// chronologically "before" anything; it's what live becomes after
// migrating) — and opens them in meld (https://meldmerge.org/) instead of
// printing a unified diff, for a change large or nested enough that a
// terminal diff is hard to read. It blocks until the operator closes the
// meld window. Doctor reports meld's availability as an optional,
// non-blocking check; Meld itself still surfaces a clear error if it
// isn't on PATH.
func Meld(ctx context.Context, r runner.Runner, leftLabel, left, rightLabel, right string) error {
	leftFile, err := writeTempDiffFile(leftLabel+"-*.yaml", left)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(leftFile) }()

	rightFile, err := writeTempDiffFile(rightLabel+"-*.yaml", right)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(rightFile) }()

	if _, stderr, err := r.Run(ctx, "meld", leftFile, rightFile); err != nil {
		return fmt.Errorf("running meld: %w (stderr: %s)", err, stderr)
	}
	return nil
}

func writeTempDiffFile(pattern, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("creating a temporary file for meld: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(content); err != nil {
		return "", fmt.Errorf("writing %s: %w", f.Name(), err)
	}
	return f.Name(), nil
}
