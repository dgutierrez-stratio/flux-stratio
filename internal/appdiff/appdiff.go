// Package appdiff diffs one app's rendered GitOps desired state against
// its live legacy state: it renders the app via internal/render, then
// dispatches to internal/diff's manifest mode (a CRD/manifest object
// compared directly) or chart mode (a HelmRelease compared via its
// chart's env vars), chosen by whether the app's config sets ChartPath —
// mirroring the Python client's own --chart-path dispatch gate, but
// without a --chart-path flag: it comes from the app's catalog entry, not
// something the operator types per invocation.
//
// Diff is the one place both `apps diff` and `apps migrate` (from Phase 6
// onward) compute a patch, so the two commands can never disagree about
// what a migration would do.
package appdiff

import (
	"context"
	"fmt"
	"path/filepath"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/render"
	"github.com/Stratio/flux-stratio/internal/runner"
)

// Options configures diffing one app.
type Options struct {
	Base, Cluster, Tenant string
	// ChartsBase, if set, overrides Base for resolving a chart-mode app's
	// on-disk chart directory (see config.Config.ChartsBase).
	ChartsBase string
	App        config.App
	Runner     runner.Runner
	// Client talks to the live cluster: used by internal/render to
	// resolve postBuild.substituteFrom, and — in both diff modes — to
	// fetch the app's live state to compare against (unless Baseline is
	// set).
	Client client.Client
	// Baseline, if set, is a backup directory as written by
	// internal/backup — apps diff --baseline. The live side of the
	// comparison is read from it instead of the live cluster: cr.yaml in
	// manifest mode, env-vars.env in chart mode. The rendered/desired
	// side is unaffected either way.
	Baseline string
	Log      *log.Logger
}

// Result is the outcome of diffing one app.
type Result struct {
	// Patch is the patch flux-stratio would splice into the tenant YAML,
	// or nil if there is no difference.
	Patch *diff.PatchDoc
	// Before and After are a human-readable, textual view of the rendered
	// desired state and the live legacy state — a manifest-mode spec's
	// YAML in manifest mode, sorted "KEY=VALUE" env var lines in chart
	// mode. internal/ui.FileDiff(Before, After) is apps diff's default
	// output; Patch (via internal/ui.Patch) is its --patch output.
	Before, After string
	// The remaining fields are set only in chart mode (App.ChartPath !=
	// ""); they stay at their zero value in manifest mode.
	UnmappedDiffs     []diff.UnmappedDiff
	LiveOnlyCount     int
	RenderedOnlyCount int
}

// Diff renders opts.App and compares it against its live legacy state.
func Diff(ctx context.Context, opts Options) (*Result, error) {
	rendered, err := renderApp(ctx, opts)
	if err != nil {
		return nil, err
	}

	if opts.App.ChartPath == "" {
		return manifestDiff(ctx, opts, rendered)
	}
	return chartDiff(ctx, opts, rendered)
}

// renderApp is the one render.Render call site every entrypoint in this
// package goes through, so opts.App's Rset/Kustomization/Object are always
// interpreted the same way.
func renderApp(ctx context.Context, opts Options) (*render.Result, error) {
	return render.Render(ctx, render.Options{
		Base:          opts.Base,
		Cluster:       opts.Cluster,
		Tenant:        opts.Tenant,
		Rset:          opts.App.Rset,
		Kustomization: opts.App.Kustomization,
		Object:        opts.App.Object,
		Runner:        opts.Runner,
		Client:        opts.Client,
		Log:           opts.Log,
	})
}

func chartPath(opts Options) string {
	root := opts.Base
	if opts.ChartsBase != "" {
		root = opts.ChartsBase
	}
	return filepath.Join(root, opts.App.ChartPath)
}

func fmtNotFound(kind, namespace, name string, err error) error {
	return fmt.Errorf("fetching live %s %s/%s: %w", kind, namespace, name, err)
}
