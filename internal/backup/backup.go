// Package backup captures an app's live legacy state to disk before it is
// migrated, written to <dir>/<app-id>/<UTC-timestamp>/ so it survives the
// live object changing or disappearing later, and so `apps diff
// --baseline` can diff against it instead of the live cluster.
//
// The live object is found via internal/discovery's cluster-wide,
// name-indexed scan — never by rendering the app's GitOps desired state —
// so a backup never depends on the tenant file already declaring the
// app's component (matching the legacy Python migration client's own
// discovery.py, which never touched the tenant file or flux-operator/flux
// either).
//
// What gets captured is decided the same way apps diff/migrate's dispatch
// is (App.ChartPath), so a backup always produces the file shape `apps
// diff --baseline` expects for that app — cr.yaml in manifest mode,
// env-vars.env in chart mode — falling back progressively (mirroring the
// Python client's own CR > Deployment > HelmRelease-only cascade) only
// when the live object isn't backed by the expected kind, in which case a
// Warningf says so. Nothing is written to disk unless something was
// actually found — Run creates the timestamped directory lazily, right
// before its first successful write, so a failed or skipped backup never
// leaves an empty directory behind.
package backup

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/appdiff"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/discovery"
	"github.com/Stratio/flux-stratio/internal/envvars"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
)

// timestampFormat matches the Python client's own backup directory naming
// (BackupManager.create_backup_dir): UTC, colon-free so it's a valid path
// component on every OS.
const timestampFormat = "2006-01-02T15-04-05Z"

// Options configures backing up one app.
type Options struct {
	// Base is the parent directory holding keos-apps/keos-use-cases/etc.
	// — only used to resolve a chart-mode app's on-disk chart path.
	Base string
	// ChartsBase, if set, overrides Base for resolving a chart-mode app's
	// on-disk chart directory (see config.Config.ChartsBase).
	ChartsBase string
	App        config.App
	// Runner runs `helm template`/`helm dependency build` for a
	// chart-mode app; unused in manifest mode.
	Runner runner.Runner
	Client client.Client
	// Index is a discovery.Scan result — built once by the caller (see
	// internal/cli/apps.go's backupAll) and reused across every app in an
	// `apps backup --all` run, since it's the same ~18 cluster-wide lists
	// regardless of which app is being backed up.
	Index *discovery.Index
	// Dir is the backups root directory; Run writes into
	// Dir/<App.ID>/<timestamp>/.
	Dir string
	Log *log.Logger
	// Clock returns the current time; defaults to time.Now. Tests inject
	// a fixed clock for a deterministic directory name.
	Clock func() time.Time
}

// Result reports what a backup run captured.
type Result struct {
	// Dir is the timestamped directory written to, or "" if nothing was
	// captured.
	Dir string
	// Files are the file names written, relative to Dir.
	Files []string
}

// Run backs up opts.App's live state.
func Run(ctx context.Context, opts Options) (*Result, error) {
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	dir := filepath.Join(opts.Dir, opts.App.ID, clock().UTC().Format(timestampFormat))
	name := opts.App.LiveName()

	var files []string
	var err error
	if opts.App.ChartPath != "" {
		files, err = captureChartMode(ctx, opts, dir, name)
	} else {
		files, err = captureManifestMode(ctx, opts, dir, name)
	}
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return &Result{}, nil
	}
	return &Result{Dir: dir, Files: files}, nil
}

// DiscoveredApps returns one config.App per distinct live identity idx
// found — for `apps backup --all`, "everything, no filters applied". A
// name idx found that also matches a catalog app's own live name (see
// config.App.LiveName) resolves to that real config.App, so it's
// captured exactly the same way `apps backup --catalog` would capture it
// (same directory, same ChartPath-driven dispatch, same shape `apps diff
// --baseline` expects) — the same app must come out identical whichever
// flag reached it. Everything else gets a minimal synthetic App (ID and
// Object both the discovered name, no ChartPath), which still goes
// through the exact same Run dispatch — it just can't run
// chart-templating without a catalog entry's ChartPath to locate the
// chart on disk, so it falls back to whatever Run's manifest-mode cascade
// finds.
//
// A synthetic App's ID is suffixed "-live" if it would otherwise collide
// with a real catalog App's ID — the one real way this happens is a
// Renamed app whose *new*, post-migration name is also live at once
// (a legitimate mid-migration state, since the app's own catalog ID is
// conventionally its new/Object name): without disambiguation, both the
// pre- and post-migration live objects would be captured under the same
// <dir>/<App.ID>/<timestamp>/ directory, silently mixing two different
// captures together and corrupting later --baseline/--drift resolution.
func DiscoveredApps(cfg *config.Config, idx *discovery.Index) []config.App {
	byLiveName := make(map[string]config.App, len(cfg.Apps))
	catalogIDs := make(map[string]bool, len(cfg.Apps))
	for _, app := range cfg.Apps {
		byLiveName[app.LiveName()] = app
		catalogIDs[app.ID] = true
	}

	apps := make([]config.App, 0, len(idx.Names()))
	usedIDs := make(map[string]bool, len(idx.Names()))
	for _, name := range idx.Names() {
		if app, ok := byLiveName[name]; ok {
			apps = append(apps, app)
			usedIDs[app.ID] = true
			continue
		}
		id := name
		if catalogIDs[id] || usedIDs[id] {
			id = name + "-live"
		}
		apps = append(apps, config.App{ID: id, Name: name, Object: name})
		usedIDs[id] = true
	}
	return apps
}

func notFoundErr(name string) error {
	return fmt.Errorf("no live Kustomization, HelmRelease, Deployment/StatefulSet/DaemonSet or known CRD instance named %q found on the cluster", name)
}

// captureManifestMode backs up a manifest-mode app (App.ChartPath == ""):
// its live custom resource, falling back progressively when the expected
// CR isn't there. This is also the path every synthetic, non-catalog App
// DiscoveredApps builds for `apps backup --all` goes through (a synthetic
// App never has a ChartPath to template a chart against) — the mismatch
// warnings below only fire for a real catalog entry (isCatalogApp), since
// a synthetic App has no "expected" shape apps diff --baseline could ever
// compare against in the first place.
func captureManifestMode(ctx context.Context, opts Options, dir, name string) ([]string, error) {
	if cr, ok := opts.Index.FindCR(name, opts.Log); ok {
		return writeCR(dir, cr)
	}
	if wl, ok := opts.Index.FindWorkload(name, opts.Log); ok {
		if isCatalogApp(opts.App) {
			opts.Log.Warningf("%q is configured as manifest-mode but was only found live as a workload; apps diff --baseline expects cr.yaml for this app", opts.App.ID)
		}
		return writeWorkload(ctx, opts, dir, wl)
	}
	if hr, ok := opts.Index.FindHelmRelease(name, opts.Log); ok {
		if isCatalogApp(opts.App) {
			opts.Log.Warningf("%q is configured as manifest-mode but was only found live as a HelmRelease; apps diff --baseline expects cr.yaml for this app", opts.App.ID)
		}
		values, err := resolveHelmReleaseValues(ctx, opts, hr)
		if err != nil {
			return nil, err
		}
		return writeHelmReleaseFiles(dir, hr, values, opts.Log)
	}
	if _, ok := opts.Index.FindKustomization(name, opts.Log); ok {
		opts.Log.Warningf("only a Kustomization named %q was found live (no HelmRelease/Deployment/CR backing it); nothing to back up", name)
		return nil, nil
	}
	return nil, notFoundErr(name)
}

// isCatalogApp reports whether app is a real config catalog entry rather
// than a synthetic one DiscoveredApps built for a live object the catalog
// doesn't know about — Rset is required for every catalog entry
// (config.Config.validate) and never set on a synthetic App.
func isCatalogApp(app config.App) bool {
	return app.Rset != ""
}

// captureChartMode backs up a chart-mode app (App.ChartPath != ""): every
// live workload its chart declares, sourced from the live HelmRelease's
// own values (never the flux-rendered desired ones), falling back
// progressively when there's no live HelmRelease under this identity.
func captureChartMode(ctx context.Context, opts Options, dir, name string) ([]string, error) {
	if hr, ok := opts.Index.FindHelmRelease(name, opts.Log); ok {
		return captureChartFromHelmRelease(ctx, opts, dir, hr)
	}
	if wl, ok := opts.Index.FindWorkload(name, opts.Log); ok {
		return writeWorkload(ctx, opts, dir, wl)
	}
	if cr, ok := opts.Index.FindCR(name, opts.Log); ok {
		opts.Log.Warningf("%q is configured as chart-mode but was only found live as a CR; apps diff --baseline expects env-vars.env for this app", opts.App.ID)
		return writeCR(dir, cr)
	}
	if _, ok := opts.Index.FindKustomization(name, opts.Log); ok {
		opts.Log.Warningf("only a Kustomization named %q was found live (no HelmRelease/Deployment/CR backing it); nothing to back up", name)
		return nil, nil
	}
	return nil, notFoundErr(name)
}

// captureChartFromHelmRelease templates opts.App's chart with hr's own
// live values (not the flux-rendered desired HelmRelease's — a backup
// capturing desired-state values would defeat its own purpose) to
// enumerate every workload the chart declares, the same way
// internal/appdiff's chart-mode diff does, then fetches each one live and
// merges their env vars. A chart that declares no live workloads at all
// degrades to capturing the HelmRelease and its values instead.
func captureChartFromHelmRelease(ctx context.Context, opts Options, dir string, hr *unstructured.Unstructured) ([]string, error) {
	values, err := resolveHelmReleaseValues(ctx, opts, hr)
	if err != nil {
		return nil, err
	}

	chartsBase := opts.Base
	if opts.ChartsBase != "" {
		chartsBase = opts.ChartsBase
	}
	chartDir := filepath.Join(chartsBase, opts.App.ChartPath)
	// opts.App.Object, not hr.GetName() (the live/Renamed name) — the
	// same release name internal/appdiff's own renderChart uses, so a
	// chart whose rendered resource names derive from .Release.Name
	// produces the same names here as it would for a desired-state
	// render, letting FetchLiveWorkloads' Renamed-translation apply
	// identically in both places.
	renderedDocs, err := diff.HelmTemplate(ctx, opts.Runner, chartDir, opts.App.Object, hr.GetNamespace(), values)
	if err != nil {
		return nil, err
	}

	aopts := appdiff.Options{App: opts.App, Client: opts.Client}
	liveWorkloads := appdiff.FetchLiveWorkloads(ctx, aopts, hr.GetNamespace(), renderedDocs)
	if len(liveWorkloads) == 0 {
		opts.Log.Warningf("HelmRelease %q declares no live workloads; apps diff --baseline won't have env-vars.env for this app's capture", hr.GetName())
		return writeHelmReleaseFiles(dir, hr, values, opts.Log)
	}

	if err := writeYAMLFile(dir, "deployment.yaml", liveWorkloads[0].Object); err != nil {
		return nil, err
	}
	env, err := appdiff.MergeLiveEnv(ctx, aopts, liveWorkloads)
	if err != nil {
		return nil, err
	}
	if err := writeEnvFile(dir, "env-vars.env", env); err != nil {
		return nil, err
	}
	return []string{"deployment.yaml", "env-vars.env"}, nil
}

func writeCR(dir string, cr *unstructured.Unstructured) ([]string, error) {
	if err := writeYAMLFile(dir, "cr.yaml", cr.Object); err != nil {
		return nil, err
	}
	return []string{"cr.yaml"}, nil
}

func writeWorkload(ctx context.Context, opts Options, dir string, wl *unstructured.Unstructured) ([]string, error) {
	if err := writeYAMLFile(dir, "deployment.yaml", wl.Object); err != nil {
		return nil, err
	}
	env, err := envvars.Extract(ctx, envvars.ClientGetter{Client: opts.Client}, wl, opts.Log.Debugf)
	if err != nil {
		return nil, err
	}
	if err := writeEnvFile(dir, "env-vars.env", env); err != nil {
		return nil, err
	}
	return []string{"deployment.yaml", "env-vars.env"}, nil
}
