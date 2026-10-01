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
	"reflect"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/render"
	"github.com/Stratio/flux-stratio/internal/runner"
)

// Options configures diffing one app.
type Options struct {
	// Repos is where the GitOps repositories and the charts repository
	// are checked out.
	Repos           config.RepoPaths
	Cluster, Tenant string
	App             config.App
	Runner          runner.Runner
	// Client talks to the live cluster: used by internal/render to
	// resolve postBuild.substituteFrom, and — in both diff modes — to
	// fetch the app's live state to compare against (unless Baseline is
	// set).
	Client client.Client
	// Baseline, if set, is a backup directory as written by
	// internal/backup — apps diff --baseline. The live side of the
	// comparison is read from it instead of the live cluster: cr.yaml in
	// manifest mode; in chart mode each workload's own env file
	// (WorkloadEnvFile), or env-vars.env for a backup that has none. The
	// rendered/desired side is unaffected either way.
	Baseline string
	Log      *log.Logger
}

// Result is the outcome of diffing one app.
type Result struct {
	// Patch is the whole patch the app needs — computed against the
	// rendered base *without* the tenant file's existing patch for this
	// object's kind (see internal/render.Result.ReplacedPatches) — or nil
	// if live and base agree.
	Patch *diff.PatchDoc
	// UpToDate is true when the tenant file already carries exactly Patch
	// as this object's kind's only patch: migrating would change nothing.
	UpToDate bool
	// ObsoletePatches counts existing patches for this object's kind in
	// the tenant file when Patch is nil — live already matches the base,
	// so whatever they set would move live away from its current state.
	ObsoletePatches int
	// ExistingPatches counts the tenant file's patches for this object's
	// kind, whatever Patch is: Before is rendered without them, so when it's
	// non-zero Before is the unpatched base, not what Flux would apply.
	ExistingPatches int
	// FluxManagedBy is set (to "Kustomization <name>" or "HelmRelease
	// <name>") when the live object compared against is already managed by
	// Flux: its state then reflects the GitOps render, not the legacy
	// installation, and a patch computed from it can miss legacy values
	// Flux already reset — compare against a backup (Baseline) instead.
	// Always empty in Baseline mode.
	FluxManagedBy string
	// Before and After are a human-readable, textual view of the rendered
	// desired state and the live legacy state — a manifest-mode spec's
	// YAML in manifest mode, sorted "KEY=VALUE" env var lines in chart
	// mode (each prefixed with its workload's name when the chart renders
	// several). internal/ui.FileDiff(Before, After) is apps diff's default
	// output; Patch (via internal/ui.Patch) is its --patch output.
	Before, After string
	// The remaining fields are set only in chart mode (App.ChartPath !=
	// ""); they stay at their zero value in manifest mode. See
	// diff.ChartDiffResult for the first three.
	UnmappedDiffs     []diff.UnmappedDiff
	LiveOnly          []diff.LiveOnlyVar
	RenderedOnlyCount int
	// MissingWorkloads names ("Kind namespace/name") the workloads the
	// chart renders that have no live counterpart to compare — not found
	// live, or (with Baseline) not captured in the backup — so nothing of
	// theirs is carried into the patch.
	MissingWorkloads []string
}

// Diff renders opts.App and compares it against its live legacy state.
func Diff(ctx context.Context, opts Options) (*Result, error) {
	rendered, err := renderApp(ctx, opts)
	if err != nil {
		return nil, err
	}

	var result *Result
	if opts.App.ChartPath == "" {
		result, err = manifestDiff(ctx, opts, rendered)
	} else {
		result, err = chartDiff(ctx, opts, rendered)
	}
	if err != nil {
		return nil, err
	}
	result.ExistingPatches = len(rendered.ReplacedPatches)
	switch {
	case result.Patch == nil:
		result.ObsoletePatches = len(rendered.ReplacedPatches)
	case len(rendered.ReplacedPatches) == 1:
		result.UpToDate, err = samePatch(*result.Patch, rendered.ReplacedPatches[0])
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Labels Flux's controllers stamp on every object they apply.
const (
	kustomizeNameLabel = "kustomize.toolkit.fluxcd.io/name"
	helmNameLabel      = "helm.toolkit.fluxcd.io/name"
)

// fluxManagedBy reports which Flux object, if any, manages obj.
func fluxManagedBy(obj *unstructured.Unstructured) string {
	labels := obj.GetLabels()
	if name := labels[kustomizeNameLabel]; name != "" {
		return "Kustomization " + name
	}
	if name := labels[helmNameLabel]; name != "" {
		return "HelmRelease " + name
	}
	return ""
}

// samePatch reports whether existing (a tenant-file patch body) is
// semantically the patch computed as doc — compared as decoded YAML, so
// key order, indentation or quoting never make an unchanged patch look
// changed.
func samePatch(doc diff.PatchDoc, existing string) (bool, error) {
	computed, err := yaml.Marshal(doc.Patch)
	if err != nil {
		return false, fmt.Errorf("marshaling computed patch: %w", err)
	}
	var a, b any
	if err := yaml.Unmarshal(computed, &a); err != nil {
		return false, fmt.Errorf("decoding computed patch: %w", err)
	}
	if err := yaml.Unmarshal([]byte(existing), &b); err != nil {
		// An existing patch that doesn't even parse is certainly not the same.
		return false, nil //nolint:nilerr // unparseable just means "different"
	}
	return reflect.DeepEqual(a, b), nil
}

// renderApp is the one render.Render call site every entrypoint in this
// package goes through, so opts.App's Rset/Kustomization/Object are always
// interpreted the same way.
func renderApp(ctx context.Context, opts Options) (*render.Result, error) {
	return render.Render(ctx, render.Options{
		Repos:         opts.Repos,
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
	return filepath.Join(opts.Repos.Charts, opts.App.ChartPath)
}

func fmtNotFound(kind, namespace, name string, err error) error {
	return fmt.Errorf("fetching live %s %s/%s: %w", kind, namespace, name, err)
}
