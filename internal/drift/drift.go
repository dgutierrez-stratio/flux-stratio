// Package drift answers a different question than internal/appdiff does:
// not "what would migrating this app change" (rendered GitOps desired
// state vs. live, internal/appdiff's job), but "has this app's live state
// changed since I backed it up" — the live cluster right now, compared
// directly against a stored internal/backup capture, with no GitOps
// rendering involved at all. It exists for post-migration operational
// drift checks, where the GitOps side is no longer the interesting
// question — Flux is already reconciling it — and what matters is whether
// something has changed on the live side since a known-good snapshot.
//
// Run captures live state the exact same way `apps backup` does (via
// internal/backup.Run, to a throwaway temporary directory) and compares
// it against an existing backup directory file for file, so the two sides
// are always read through the identical capture logic — never two
// different ways of looking at "what's live."
package drift

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/backup"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/discovery"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

// Options configures a drift check for one app.
type Options struct {
	App config.App
	// Repos is forwarded to backup.Options — see its doc comment.
	Repos  config.RepoPaths
	Runner runner.Runner
	Client client.Client
	Index  *discovery.Index
	// Against is the backup directory to compare live state against —
	// resolve it first with backup.ResolveBaseline.
	Against string
	Log     *log.Logger
}

// Result is a drift check's outcome: Before is the stored backup's
// content, After is what's live right now — the same before/after
// direction internal/ui.FileDiff expects, reading as "this is what's
// changed since the backup was taken."
type Result struct {
	Before, After string
}

// Run captures opts.App's live state and compares it against the backup
// at opts.Against.
func Run(ctx context.Context, opts Options) (*Result, error) {
	tempDir, err := os.MkdirTemp("", "flux-stratio-drift-*")
	if err != nil {
		return nil, fmt.Errorf("creating a temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	live, err := backup.Run(ctx, backup.Options{
		Repos: opts.Repos, App: opts.App, Runner: opts.Runner, Client: opts.Client,
		Index: opts.Index, Dir: tempDir, Log: opts.Log,
	})
	if err != nil {
		return nil, fmt.Errorf("capturing live state: %w", err)
	}
	if live.Dir == "" {
		return nil, fmt.Errorf("nothing live to compare %q against", opts.App.ID)
	}

	return compare(opts.Against, live.Dir, live.Files)
}

// compare picks the one file both the stored backup and the fresh live
// capture actually have — the signal internal/backup's own shape cascade
// chose for the live side — and diffs that. A shape mismatch (the live
// object now resolves to a different kind than the stored backup did) is
// reported clearly rather than comparing unrelated files.
func compare(backupDir, liveDir string, liveFiles []string) (*Result, error) {
	signal := primarySignal(liveFiles)
	if signal == "" || !fileExists(backupDir, signal) {
		backupFiles := listFiles(backupDir)
		if (signal == "values.yaml" || signal == "helmrelease.yaml") && has(backupFiles, "env-vars.env") {
			return nil, fmt.Errorf(
				"live state was captured as %v because none of the live HelmRelease's workloads were found (see the "+
					"warning above), but the backup at %s was captured as %v — make sure the charts repository "+
					"checkout (repos.charts) holds the chart version the release runs, so its workloads are found "+
					"and captured as env vars too",
				liveFiles, backupDir, backupFiles,
			)
		}
		return nil, fmt.Errorf(
			"live state was captured as %v, but the backup at %s was captured as %v — "+
				"the app may have changed shape (e.g. a CR became a HelmRelease) since that backup was taken",
			liveFiles, backupDir, backupFiles,
		)
	}
	if signal == "cr.yaml" || signal == "helmrelease.yaml" {
		return compareSpec(backupDir, liveDir, signal)
	}
	return compareText(backupDir, liveDir, signal)
}

// primarySignal returns the one file worth diffing for a given capture
// shape — env-vars.env over deployment.yaml's noisy raw manifest, and
// values.yaml over helmrelease.yaml's, the same way apps diff's own
// chart-mode Before/After already prefers env vars over the raw workload.
func primarySignal(files []string) string {
	switch {
	case has(files, "cr.yaml"):
		return "cr.yaml"
	case has(files, "env-vars.env"):
		return "env-vars.env"
	case has(files, "values.yaml"):
		return "values.yaml"
	case has(files, "helmrelease.yaml"):
		return "helmrelease.yaml"
	}
	return ""
}

func has(files []string, name string) bool {
	for _, f := range files {
		if f == name {
			return true
		}
	}
	return false
}

// listFiles is dir's file names, or nil when it can't be read.
func listFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// compareSpec diffs two raw-manifest captures (cr.yaml/helmrelease.yaml)
// by their .spec only — resourceVersion/managedFields/status churn
// between any two live fetches would otherwise swamp the real diff,
// exactly the reason internal/appdiff's own manifest-mode Before/After
// only ever marshals .spec too.
func compareSpec(backupDir, liveDir, filename string) (*Result, error) {
	before, err := readSpec(backupDir, filename)
	if err != nil {
		return nil, err
	}
	after, err := readSpec(liveDir, filename)
	if err != nil {
		return nil, err
	}
	return &Result{Before: before, After: after}, nil
}

func readSpec(dir, filename string) (string, error) {
	path := filepath.Join(dir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	docs, err := yamldocs.Decode(data)
	if err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(docs) == 0 {
		return "", fmt.Errorf("%s is empty", path)
	}
	specYAML, err := yaml.Marshal(specOf(docs[0]))
	if err != nil {
		return "", fmt.Errorf("marshaling %s's spec: %w", path, err)
	}
	return string(specYAML), nil
}

func specOf(obj *unstructured.Unstructured) any {
	return obj.Object["spec"]
}

// compareText diffs two already-clean text captures (env-vars.env,
// values.yaml) verbatim — no noisy metadata to strip.
func compareText(backupDir, liveDir, filename string) (*Result, error) {
	before, err := readText(backupDir, filename)
	if err != nil {
		return nil, err
	}
	after, err := readText(liveDir, filename)
	if err != nil {
		return nil, err
	}
	return &Result{Before: before, After: after}, nil
}

func readText(dir, filename string) (string, error) {
	path := filepath.Join(dir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return string(data), nil
}
