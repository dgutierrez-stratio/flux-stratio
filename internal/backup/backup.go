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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

var helmReleaseGVK = schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}

// Options configures backing up one app.
type Options struct {
	// Repos is where the repositories are checked out — only its Charts
	// is used, to resolve a chart-mode app's on-disk chart path.
	Repos config.RepoPaths
	App   config.App
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
	// The timestamp has one-second resolution, and a capture is renamed into
	// place (which fails onto a non-empty directory): a second capture of the
	// same app within the second (backupBeforePrepare right after a backup)
	// takes the next free second rather than failing.
	when := clock().UTC()
	stamp := when.Format(timestampFormat)
	dir := filepath.Join(opts.Dir, opts.App.ID, stamp)
	for {
		if _, err := os.Stat(dir); err != nil {
			break
		}
		when = when.Add(time.Second)
		stamp = when.Format(timestampFormat)
		dir = filepath.Join(opts.Dir, opts.App.ID, stamp)
	}
	// Everything is captured into a partial directory first, renamed to
	// its timestamp only once complete: a capture that fails halfway (a
	// Secret read denied, say) must never leave a timestamped directory
	// behind, since --baseline latest would pick it over an older,
	// complete one. latestTimestampSubdir ignores the partial name.
	partial := filepath.Join(opts.Dir, opts.App.ID, ".partial-"+stamp)
	if err := os.RemoveAll(partial); err != nil {
		return nil, fmt.Errorf("clearing %s: %w", partial, err)
	}
	name := opts.App.LiveName()

	var files []string
	var err error
	if opts.App.ChartPath != "" {
		files, err = captureChartMode(ctx, opts, partial, name)
	} else {
		files, err = captureManifestMode(ctx, opts, partial, name)
	}
	if err != nil || len(files) == 0 {
		if rmErr := os.RemoveAll(partial); rmErr != nil && err == nil {
			err = fmt.Errorf("clearing %s: %w", partial, rmErr)
		}
		if err != nil {
			return nil, err
		}
		return &Result{}, nil
	}
	if err := os.Rename(partial, dir); err != nil {
		_ = os.RemoveAll(partial)
		return nil, fmt.Errorf("moving the completed backup into place at %s: %w", dir, err)
	}
	return &Result{Dir: dir, Files: files}, nil
}

// DiscoveredApps returns catalogApps (the instances internal/components
// classified, captured exactly as `apps backup --catalog` would capture
// them) plus one minimal synthetic App per remaining live object idx found —
// for `apps backup --all`, "everything, no filters applied".
//
// An object is covered, and gets no synthetic App of its own, only when it
// is exactly (group, kind, namespace, name) one of the live objects a
// catalog app was classified from: so the "genai-api" Deployment the genai
// chart app anchors on isn't captured twice, while a same-named-but-unrelated
// "genai" PgDatabase, or another tenant's "opensearch1" in a different
// namespace, still is. (Matching on the bare name, as this once did, silently
// dropped every PgDatabase that shares its Deployment's name, and every
// same-named object in a second namespace.)
//
// Per name, a synthetic App is made for each uncovered object except:
//   - a Kustomization, unless nothing else carries that name (then one
//     Live-less App is kept, which Run reports as having nothing to back up);
//   - a HelmRelease with a workload of the same name in its namespace — the
//     workload is what gets captured, as in the legacy client.
//
// A synthetic App has Object set to the discovered name and Live pinned to
// its one object (no Type or ChartPath), so Run captures exactly that
// object; it can't run chart-templating without a catalog type's chart to
// locate on disk.
//
// A synthetic App's ID is the object's name — for the first of several
// uncovered objects sharing it, see syntheticIDs; the others add their
// namespace (and kind, if still ambiguous) — and gets "-live" if it would
// otherwise collide with another App's ID
// — e.g. a renamed app's new, post-migration name live alongside its legacy
// object (a legitimate mid-migration state): without disambiguation, both
// would be captured under the same <dir>/<App.ID>/<timestamp>/ directory,
// silently mixing two different captures together and corrupting later
// --baseline/--drift resolution.
func DiscoveredApps(catalogApps []config.App, idx *discovery.Index) []config.App {
	covered := map[liveKey]bool{}
	usedIDs := make(map[string]bool, len(catalogApps))
	for _, app := range catalogApps {
		usedIDs[app.ID] = true
		for _, ref := range app.Live {
			covered[liveKey{ref.GVK.Group, ref.GVK.Kind, ref.Namespace, ref.Name}] = true
		}
	}

	byName := map[string][]*unstructured.Unstructured{}
	for _, obj := range idx.Objects() {
		byName[obj.GetName()] = append(byName[obj.GetName()], obj)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	apps := append(make([]config.App, 0, len(catalogApps)+len(names)), catalogApps...)
	add := func(id string, app config.App) {
		for usedIDs[id] {
			id += "-live"
		}
		usedIDs[id] = true
		app.ID = id
		apps = append(apps, app)
	}
	for _, name := range names {
		objs := byName[name]
		if onlyKustomizations(objs) {
			if !allCovered(objs, covered) {
				add(name, config.App{Name: name, Object: name})
			}
			continue
		}
		var pick []*unstructured.Unstructured
		for _, obj := range objs {
			if obj.GetKind() == "Kustomization" || covered[keyOf(obj)] || hasWorkloadSibling(obj, objs) {
				continue
			}
			pick = append(pick, obj)
		}
		ids := syntheticIDs(name, pick)
		for i, obj := range pick {
			add(ids[i], config.App{
				Name: name, Object: name,
				Live: []config.ObjectRef{{GVK: obj.GroupVersionKind(), Namespace: obj.GetNamespace(), Name: name}},
			})
		}
	}
	return apps
}

// liveKey identifies one live object the way discovery.Index.Get does:
// version-independent.
type liveKey struct{ group, kind, namespace, name string }

func keyOf(obj *unstructured.Unstructured) liveKey {
	gvk := obj.GroupVersionKind()
	return liveKey{gvk.Group, gvk.Kind, obj.GetNamespace(), obj.GetName()}
}

func onlyKustomizations(objs []*unstructured.Unstructured) bool {
	for _, obj := range objs {
		if obj.GetKind() != "Kustomization" {
			return false
		}
	}
	return true
}

func allCovered(objs []*unstructured.Unstructured, covered map[liveKey]bool) bool {
	for _, obj := range objs {
		if !covered[keyOf(obj)] {
			return false
		}
	}
	return true
}

// hasWorkloadSibling reports whether obj is a HelmRelease and objs holds a
// workload in its namespace — the same-named workload it rendered.
func hasWorkloadSibling(obj *unstructured.Unstructured, objs []*unstructured.Unstructured) bool {
	if obj.GetKind() != "HelmRelease" {
		return false
	}
	for _, other := range objs {
		if isWorkload(other) && other.GetNamespace() == obj.GetNamespace() {
			return true
		}
	}
	return false
}

// syntheticIDs are the ID bases for pick, in order: the first object keeps
// the bare name (the one the legacy cascade would have captured — a custom
// resource, else a workload, else a HelmRelease, then the alphabetically
// first namespace), so an app that already has backups under that name
// keeps them; each further one is name-namespace, plus -kind when two of
// those still collide. pick is reordered into that priority.
func syntheticIDs(name string, pick []*unstructured.Unstructured) []string {
	rank := func(o *unstructured.Unstructured) int {
		switch {
		case isWorkload(o):
			return 1
		case o.GetKind() == "HelmRelease":
			return 2
		}
		return 0
	}
	sort.SliceStable(pick, func(i, j int) bool {
		if ri, rj := rank(pick[i]), rank(pick[j]); ri != rj {
			return ri < rj
		}
		if pick[i].GetNamespace() != pick[j].GetNamespace() {
			return pick[i].GetNamespace() < pick[j].GetNamespace()
		}
		return pick[i].GetKind() < pick[j].GetKind()
	})
	ids := make([]string, len(pick))
	taken := map[string]bool{}
	for i, obj := range pick {
		id := name
		if i > 0 {
			id = name + "-" + obj.GetNamespace()
			if taken[id] {
				id += "-" + strings.ToLower(obj.GetKind())
			}
		}
		taken[id] = true
		ids[i] = id
	}
	return ids
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

// managingHelmRelease is the HelmRelease that rendered opts.App's primary
// live workload, per the labels helm-controller stamps on what it renders
// — for an already-migrated app, whose HelmRelease is rarely named like
// the workload it was resolved by (genai's "genai" renders genai-api and
// genai-ui). Capturing through it templates the chart, so every workload
// it renders is captured, not just the one the app was resolved by.
func managingHelmRelease(opts Options) (*unstructured.Unstructured, bool) {
	live, ok := classifiedLive(opts)
	if !ok || !isWorkload(live) {
		return nil, false
	}
	labels := live.GetLabels()
	name, namespace := labels["helm.toolkit.fluxcd.io/name"], labels["helm.toolkit.fluxcd.io/namespace"]
	if name == "" || namespace == "" {
		return nil, false
	}
	return opts.Index.Get(helmReleaseGVK, namespace, name)
}

// classifiedSiblings are the live workloads besides its primary one that
// opts.App was classified from — a chart type's siblings (Chart.Siblings,
// genai's genai-ui next to its genai-api anchor), which a legacy CCT
// installation deployed as separate apps with no HelmRelease to enumerate
// them by. Captured with the primary one, they're what lets `apps diff
// --baseline` and drift checks compare each sibling against its own
// rendered workload. A sibling no longer in the index is skipped.
func classifiedSiblings(opts Options) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for i, ref := range opts.App.Live {
		if i == 0 {
			continue
		}
		obj, ok := opts.Index.Get(ref.GVK, ref.Namespace, ref.Name)
		if !ok || !isWorkload(obj) {
			opts.Log.Debugf("%s %s/%s: classified as part of %q but no longer live; not captured", ref.GVK.Kind, ref.Namespace, ref.Name, opts.App.ID)
			continue
		}
		out = append(out, obj)
	}
	return out
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
	if hr, ok := managingHelmRelease(opts); ok {
		return captureChartFromHelmRelease(ctx, opts, dir, hr)
	}
	if hr, ok := opts.Index.FindHelmRelease(name, opts.Log); ok {
		return captureChartFromHelmRelease(ctx, opts, dir, hr)
	}
	if live, ok := classifiedLive(opts); ok && isWorkload(live) {
		return writeWorkloads(ctx, opts, dir, append([]*unstructured.Unstructured{live}, classifiedSiblings(opts)...))
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

	chartDir := filepath.Join(opts.Repos.Charts, opts.App.ChartPath)
	// hr's own spec.releaseName (falling back to opts.App.Object, not
	// hr.GetName() — the live, maybe-renamed name) — the same release name
	// internal/appdiff's own renderChart uses (diff.ReleaseName), so a
	// chart whose rendered resource names derive from .Release.Name
	// produces the same names here as it would for a desired-state
	// render, letting FetchLiveWorkloads' live-name translation apply
	// identically in both places.
	releaseName := diff.ReleaseName(hr, opts.App.Object)
	renderedDocs, err := diff.HelmTemplate(ctx, opts.Runner, chartDir, releaseName, hr.GetNamespace(), values)
	if err != nil {
		// A chart that won't template (a checkout at another version, a
		// values shape it rejects) must not cost the capture of workloads
		// that are right there live.
		if files, ok, ferr := captureClassifiedWorkloads(ctx, opts, dir); ok {
			opts.Log.Warningf("HelmRelease %q: templating its chart at %s failed (%v); captured the classified live workloads directly instead", hr.GetName(), chartDir, err)
			return files, ferr
		}
		return nil, err
	}

	aopts := appdiff.Options{App: opts.App, Client: opts.Client, Log: opts.Log}
	liveWorkloads, err := appdiff.FetchLiveWorkloads(ctx, aopts, hr.GetNamespace(), renderedDocs)
	if err != nil {
		return nil, err
	}
	if len(liveWorkloads) == 0 {
		rendered := appdiff.RenderedWorkloadNames(aopts, hr.GetNamespace(), renderedDocs)
		if files, ok, ferr := captureClassifiedWorkloads(ctx, opts, dir); ok {
			opts.Log.Warningf("HelmRelease %q: none of the workloads the chart at %s renders (%s) exist live; captured the classified live workloads directly instead",
				hr.GetName(), chartDir, strings.Join(rendered, ", "))
			return files, ferr
		}
		warnNoLiveWorkloads(opts, hr, chartDir, rendered)
		return writeHelmReleaseFiles(dir, hr, values, opts.Log)
	}

	manifests, err := writeWorkloadManifests(dir, liveWorkloads)
	if err != nil {
		return nil, err
	}
	envs, err := appdiff.LiveWorkloadEnvs(ctx, aopts, liveWorkloads)
	if err != nil {
		return nil, err
	}
	envFiles, err := writeEnvFiles(dir, liveWorkloads, envs)
	if err != nil {
		return nil, err
	}
	return append(manifests, envFiles...), nil
}

// workloadManifestPrefix/-Suffix frame WorkloadManifestFile's names.
const (
	workloadManifestPrefix = "workload."
	workloadManifestSuffix = ".yaml"
)

// WorkloadManifestFile is the backup file name holding a sibling workload's
// manifest: <prefix><kind>.<name>.yaml. The first workload (the app's
// anchor) is deployment.yaml instead, so a backup of a single workload is
// unchanged; the others — genai-ui next to genai-api, virtualizer-ui and
// -monitor next to the virtualizer server — used to keep an env file only,
// losing their image, replicas, resources, probes and volumes. Workload
// names are DNS-1123 labels, safe as file name parts.
func WorkloadManifestFile(kind, name string) string {
	return workloadManifestPrefix + strings.ToLower(kind) + "." + name + workloadManifestSuffix
}

// IsWorkloadManifestFile reports whether name is a WorkloadManifestFile name.
func IsWorkloadManifestFile(name string) bool {
	return strings.HasPrefix(name, workloadManifestPrefix) && strings.HasSuffix(name, workloadManifestSuffix) &&
		strings.Count(strings.TrimSuffix(strings.TrimPrefix(name, workloadManifestPrefix), workloadManifestSuffix), ".") >= 1
}

// writeWorkloadManifests writes the first workload as deployment.yaml and
// every other one as its WorkloadManifestFile, returning the names written.
func writeWorkloadManifests(dir string, wls []*unstructured.Unstructured) ([]string, error) {
	if err := writeYAMLFile(dir, "deployment.yaml", wls[0].Object); err != nil {
		return nil, err
	}
	files := []string{"deployment.yaml"}
	for _, w := range wls[1:] {
		name := WorkloadManifestFile(w.GetKind(), w.GetName())
		if err := writeYAMLFile(dir, name, w.Object); err != nil {
			return nil, err
		}
		files = append(files, name)
	}
	return files, nil
}

// captureClassifiedWorkloads captures the live workloads opts.App was
// classified from (the primary one and its siblings) as plain workloads —
// no chart templating — when it has a workload as its primary live object.
// ok is false, and nothing is written, when it doesn't.
func captureClassifiedWorkloads(ctx context.Context, opts Options, dir string) (files []string, ok bool, err error) {
	live, found := classifiedLive(opts)
	if !found || !isWorkload(live) {
		return nil, false, nil
	}
	files, err = writeWorkloads(ctx, opts, dir, append([]*unstructured.Unstructured{live}, classifiedSiblings(opts)...))
	return files, true, err
}

// writeEnvFiles writes each live workload's env vars to its own
// appdiff.WorkloadEnvFile, plus all of them merged — a later workload's
// variables overwriting an earlier one's — into env-vars.env, returning
// the file names written (env-vars.env first).
//
// The per-workload files are what `apps diff --baseline` compares each
// sibling of a multi-workload chart with, and what a drift check diffs
// when both sides have them: a merge can't say which sibling a same-named
// variable (genai-api's and genai-ui's VAULT_ROLE) came from. env-vars.env
// stays for backups' own detection (ResolveBaseline) and as the fallback
// for comparing against backups taken before per-workload files existed.
func writeEnvFiles(dir string, workloads []*unstructured.Unstructured, envs []map[string]string) ([]string, error) {
	merged := map[string]string{}
	for _, env := range envs {
		for k, v := range env {
			merged[k] = v
		}
	}
	if err := writeEnvFile(dir, "env-vars.env", merged); err != nil {
		return nil, err
	}
	files := []string{"env-vars.env"}
	for i, w := range workloads {
		name := appdiff.WorkloadEnvFile(w.GetKind(), w.GetName())
		if err := writeEnvFile(dir, name, envs[i]); err != nil {
			return nil, err
		}
		files = append(files, name)
	}
	return files, nil
}

// warnNoLiveWorkloads explains why a chart-mode capture degrades to the
// HelmRelease and its values: either the chart renders no workload at all,
// or none of the ones it renders exist live — most often because the chart
// in the charts repository checkout isn't the version the release runs, and names its
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
	return writeWorkloads(ctx, opts, dir, []*unstructured.Unstructured{wl})
}

// writeWorkloads captures live workloads directly (no chart templating):
// the first one's manifest as deployment.yaml, the others' as their
// WorkloadManifestFile, and every one's env vars (see writeEnvFiles).
func writeWorkloads(ctx context.Context, opts Options, dir string, wls []*unstructured.Unstructured) ([]string, error) {
	manifests, err := writeWorkloadManifests(dir, wls)
	if err != nil {
		return nil, err
	}
	getter := envvars.ClientGetter{Client: opts.Client}
	envs := make([]map[string]string, 0, len(wls))
	for _, wl := range wls {
		env, err := envvars.Extract(ctx, getter, wl, opts.Log.Warningf)
		if err != nil {
			return nil, err
		}
		envs = append(envs, env)
	}
	envFiles, err := writeEnvFiles(dir, wls, envs)
	if err != nil {
		return nil, err
	}
	return append(manifests, envFiles...), nil
}
