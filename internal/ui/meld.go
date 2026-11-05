package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// The sides a meld comparison names its files after. Every caller takes its
// labels from here, so a side is called the same wherever it is shown.
const (
	LabelDesired = "desired state" // what the tenant file renders
	LabelLive    = "live cluster"  // the live cluster, in a desired-state comparison
	LabelBackup  = "backup"        // a stored backup, on either kind of comparison
	LabelLiveNow = "live now"      // the live cluster, in a drift comparison against a backup
)

// A Comparison is one resource worth comparing on its own — a workload's
// environment variables, a workload's spec — as opposed to the single text
// a unified diff makes of all of them.
type Comparison struct {
	// Env marks the comparison of environment variables: those come first
	// in the window, whatever else is compared after them.
	Env bool
	// Name says what is compared ("env dg-hdfs-agent",
	// "Deployment/dg-hdfs-agent spec"): it names the temporary files, so it
	// is what the tab in meld is called.
	Name        string
	Left, Right string
}

// Differs reports whether the two sides are not the same.
func (c Comparison) Differs() bool { return c.Left != c.Right }

// OpenMeld opens meld on a comparison. When it is made of several resources
// (items) they open as one window with a tab each — every one of them, see
// TabsToOpen; with one resource or none, left and right open as a single
// comparison, as before.
func OpenMeld(ctx context.Context, r runner.Runner, leftLabel, left, rightLabel, right string, items []Comparison) error {
	if len(items) < 2 {
		return Meld(ctx, r, leftLabel, left, rightLabel, right)
	}
	return MeldTabs(ctx, r, leftLabel, rightLabel, TabsToOpen(items))
}

// identicalSuffix marks, in its tab's name, a resource whose two sides are
// the same.
const identicalSuffix = " (identical)"

// TabsToOpen puts every resource in the window, in this order: the
// environment variables first, then everything else; within each of those,
// the resources that differ first and the identical ones after, each marked
// as such in its name; otherwise as given. A resource left out would look,
// in the window, like a comparison that was lost; showing it as identical
// says it was made and found nothing.
func TabsToOpen(items []Comparison) []Comparison {
	out := make([]Comparison, 0, len(items))
	for _, env := range []bool{true, false} {
		var same []Comparison
		for _, c := range items {
			if c.Env != env {
				continue
			}
			if c.Differs() {
				out = append(out, c)
				continue
			}
			c.Name += identicalSuffix
			same = append(same, c)
		}
		out = append(out, same...)
	}
	return out
}

// MeldTabs opens items in one meld window, a tab per comparison (meld takes
// --diff once per tab). Each side's file is named "<label>-<resource>.yaml",
// as the single comparison's always were, so meld's tab ("backup-env web.yaml
// – live now-env web.yaml") and each pane's title say what the side is and
// what is compared. (meld's --label only names the first tab, so it can't do
// this.) It blocks until the window is closed.
func MeldTabs(ctx context.Context, r runner.Runner, leftLabel, rightLabel string, items []Comparison) error {
	if len(items) == 0 {
		return nil
	}
	dir, err := os.MkdirTemp("", "flux-stratio-meld-*")
	if err != nil {
		return fmt.Errorf("creating a temporary directory for meld: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	left, right := safeName(leftLabel), safeName(rightLabel)
	if left == right {
		right += "-2" // two sides of one label would share their files
	}
	var args []string
	used := map[string]int{}
	for _, c := range items {
		base := safeName(c.Name)
		resource := base
		if n := used[base]; n > 0 {
			resource = fmt.Sprintf("%s-%d", base, n+1) // two resources of one name must not share a file
		}
		used[base]++
		l, err := writeInDir(dir, left+"-"+resource+".yaml", c.Left)
		if err != nil {
			return err
		}
		rt, err := writeInDir(dir, right+"-"+resource+".yaml", c.Right)
		if err != nil {
			return err
		}
		args = append(args, "--diff", l, rt)
	}
	if _, stderr, err := r.Run(ctx, "meld", args...); err != nil {
		return fmt.Errorf("running meld: %w (stderr: %s)", err, stderr)
	}
	return nil
}

func writeInDir(dir, name, content string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s for meld: %w", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// safeName makes s fit in one path element: no separators, no leading dot
// or dash (meld would read the latter as an option), never empty.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', 0:
			return '-'
		}
		return r
	}, strings.TrimSpace(s))
	s = strings.TrimLeft(s, ".-")
	if s == "" {
		return "resource"
	}
	return s
}
