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
	"strings"
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
	// on-disk chart directory (see config.Environment.ChartsBase).
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

// DiscoveredApps returns catalogApps (the instances internal/components
// classified, captured exactly as `apps backup --catalog` would capture
// them) plus one minimal synthetic App per remaining live name idx found —
// for `apps backup --all`, "everything, no filters applied". A name is
// covered, and gets no synthetic App of its own, when it's the name of any
// live object a catalog app was classified from: so the "genai-api"
// Deployment the genai chart app anchors on isn't captured twice, while
// the same-named-but-unrelated "genai" PgDatabase still is, on its own.
//
// A synthetic App (ID and Object both the discovered name, no Type or
// ChartPath) goes through the exact same Run dispatch — it just can't run
// chart-templating without a catalog type's chart to locate on disk, so it
// falls back to whatever Run's manifest-mode cascade finds.
//
// A synthetic App's ID is suffixed "-live" if it would otherwise collide
// with a catalog App's ID — e.g. a renamed app's new, post-migration name
// live alongside its legacy object (a legitimate mid-migration state):
// without disambiguation, both would be captured under the same
// <dir>/<App.ID>/<timestamp>/ directory, silently mixing two different
// captures together and corrupting later --baseline/--drift resolution.
func DiscoveredApps(catalogApps []config.App, idx *discovery.Index) []config.App {
	covered := map[string]bool{}
	usedIDs := make(map[string]bool, len(catalogApps))
	for _, app := range catalogApps {
		usedIDs[app.ID] = true
		for _, ref := range app.Live {
			covered[ref.Name] = true
		}
	}

	apps := append(make([]config.App, 0, len(catalogApps)+len(idx.Names())), catalogApps...)
	for _, name := range idx.Names() {
		if covered[name] {
			continue
		}
		id := name
		if usedIDs[id] {
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
	if live, ok := classifiedLive(opts); ok {
		return captureClassified(ctx, opts, dir, live)
	}
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

// isCatalogApp reports whether app is a classified catalog instance rather
// than a synthetic one DiscoveredApps built for a live object no catalog
// type selects.
func isCatalogApp(app config.App) bool {
	return app.Type != ""
}

// classifiedLive returns the exact live object opts.App was classified
// from (its primary App.Live ref), when it has one and it's still in the
// index — the precise lookup a catalog instance gets, instead of the
// name-only cascade, which can't tell a chart's "genai" workload from a
// "genai" PgDatabase.
func classifiedLive(opts Options) (*unstructured.Unstructured, bool) {
	if len(opts.App.Live) == 0 {
		return nil, false
	}
	ref := opts.App.Live[0]
	return opts.Index.Get(ref.GVK, ref.Namespace, ref.Name)
}

// captureClassified backs up a classified live object by its own kind.
func captureClassified(ctx context.Context, opts Options, dir string, live *unstructured.Unstructured) ([]string, error) {
	switch {
	case isWorkload(live):
		return writeWorkload(ctx, opts, dir, live)
	case live.GroupVersionKind().Group == "helm.toolkit.fluxcd.io":
		values, err := resolveHelmReleaseValues(ctx, opts, live)
		if err != nil {
			return nil, err
		}
		return writeHelmReleaseFiles(dir, live, values, opts.Log)
	default:
		return writeCR(dir, live)
	}
}

func isWorkload(obj *unstructured.Unstructured) bool {
	gvk := obj.GroupVersionKind()
	return gvk.Group == "apps" && (gvk.Kind == "Deployment" || gvk.Kind == "StatefulSet" || gvk.Kind == "DaemonSet")
}

// captureChartMode backs up a chart-mode app (App.ChartPath != ""): every
// live workload its chart declares, sourced from the live HelmRelease's
// own values (never the flux-rendered desired ones), falling back
// progressively when there's no live HelmRelease under this identity.
func captureChartMode(ctx context.Context, opts Options, dir, name string) ([]string, error) {
	if hr, ok := opts.Index.FindHelmRelease(name, opts.Log); ok {
		return captureChartFromHelmRelease(ctx, opts, dir, hr)
	}
	if live, ok := classifiedLive(opts); ok && isWorkload(live) {
		return writeWorkload(ctx, opts, dir, live)
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
	// opts.App.Object, not hr.GetName() (the live, maybe-renamed name) — the
	// same release name internal/appdiff's own renderChart uses, so a
	// chart whose rendered resource names derive from .Release.Name
	// produces the same names here as it would for a desired-state
	// render, letting FetchLiveWorkloads' live-name translation apply
	// identically in both places.
	renderedDocs, err := diff.HelmTemplate(ctx, opts.Runner, chartDir, opts.App.Object, hr.GetNamespace(), values)
	if err != nil {
		return nil, err
	}

	aopts := appdiff.Options{App: opts.App, Client: opts.Client}
	liveWorkloads := appdiff.FetchLiveWorkloads(ctx, aopts, hr.GetNamespace(), renderedDocs)
	if len(liveWorkloads) == 0 {
		warnNoLiveWorkloads(opts, hr, chartDir, appdiff.RenderedWorkloadNames(aopts, hr.GetNamespace(), renderedDocs))
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

// warnNoLiveWorkloads explains why a chart-mode capture degrades to the
// HelmRelease and its values: either the chart renders no workload at all,
// or none of the ones it renders exist live — most often because the chart
// under chartsBase isn't the version the release runs, and names its
// workloads differently.
func warnNoLiveWorkloads(opts Options, hr *unstructured.Unstructured, chartDir string, rendered []string) {
	const consequence = "capturing the HelmRelease and its values instead, so this capture has no env-vars.env " +
		"to compare against a workload capture (apps diff --baseline/--drift)"
	if len(rendered) == 0 {
		opts.Log.Warningf("HelmRelease %q declares no live workloads: the chart at %s renders no Deployment/StatefulSet/DaemonSet; %s",
			hr.GetName(), chartDir, consequence)
		return
	}
	running := ""
	if chart := releasedChart(hr); chart != "" {
		running = fmt.Sprintf(" (the release runs %s)", chart)
	}
	opts.Log.Warningf("HelmRelease %q declares no live workloads: none of those the chart at %s renders exist live "+
		"(looked for %s); if that directory doesn't hold the chart version the release runs%s, its workloads may be "+
		"named differently; %s", hr.GetName(), chartDir, strings.Join(rendered, ", "), running, consequence)
}

// releasedChart is the chart hr last released, as "name@version", or ""
// when its status doesn't say.
func releasedChart(hr *unstructured.Unstructured) string {
	history, _, _ := unstructured.NestedSlice(hr.Object, "status", "history")
	if len(history) > 0 {
		if latest, ok := history[0].(map[string]any); ok {
			name, _ := latest["chartName"].(string)
			version, _ := latest["chartVersion"].(string)
			if name != "" && version != "" {
				return name + "@" + version
			}
		}
	}
	revision, _, _ := unstructured.NestedString(hr.Object, "status", "lastAttemptedRevision")
	return revision
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
