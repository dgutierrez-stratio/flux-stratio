package appdiff

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/envvars"
	"github.com/Stratio/flux-stratio/internal/render"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

// workloadKinds are the Kubernetes workload kinds internal/diff's chart
// mode looks for in a chart's rendered output.
var workloadKinds = []string{"Deployment", "StatefulSet", "DaemonSet"}

func chartDiff(ctx context.Context, opts Options, rendered *render.Result) (*Result, error) {
	renderedDocs, hrNamespace, err := renderChart(ctx, opts, rendered)
	if err != nil {
		return nil, err
	}

	var live []diff.LiveWorkloadEnv
	var missing []string
	var managedBy string
	if opts.Baseline != "" {
		live, missing, err = baselineWorkloadEnvs(opts, hrNamespace, renderedDocs)
	} else {
		live, missing, managedBy, err = liveWorkloadEnvs(ctx, opts, hrNamespace, renderedDocs)
	}
	if err != nil {
		return nil, err
	}

	files, err := diff.ScanChartFiles(chartPath(opts))
	if err != nil {
		return nil, fmt.Errorf("mapping chart env vars to .Values paths: %w", err)
	}

	values, err := chartValues(opts, rendered)
	if err != nil {
		return nil, err
	}
	result := diff.ChartDiff(diff.ChartDiffInput{
		Rendered: renderedDocs, Live: live, Files: files,
		ValuesRoot: opts.App.ValuesRoot, HRName: opts.App.Object, Exclude: opts.App.Exclude,
		Values: values,
	})
	if result.Patch != nil && len(result.UnmappedDiffs) > 0 {
		result.UnmappedDiffs = settleUnmapped(ctx, opts, rendered, result)
	}
	before, after := formatComparisons(result.Workloads)
	return &Result{
		Patch:             result.Patch,
		Before:            before,
		After:             after,
		UnmappedDiffs:     result.UnmappedDiffs,
		LiveOnly:          result.LiveOnly,
		RenderedOnlyCount: result.RenderedOnlyCount,
		MissingWorkloads:  missing,
		FluxManagedBy:     managedBy,
	}, nil
}

// liveWorkloadEnvs fetches every workload renderedDocs declares from the
// live cluster and resolves each one's env vars, paired with its rendered
// counterpart — so each sibling of a multi-workload chart is compared
// against its own rendered workload, never a merge of all of them.
// missing names the rendered workloads not found live.
func liveWorkloadEnvs(ctx context.Context, opts Options, hrNamespace string, renderedDocs []*unstructured.Unstructured) ([]diff.LiveWorkloadEnv, []string, string, error) {
	pairs, missing := fetchWorkloadPairs(ctx, opts, hrNamespace, renderedDocs)
	if len(pairs) == 0 {
		return nil, nil, "", fmt.Errorf("no live workload found for chart %q among %v", opts.App.ChartPath, workloadKinds)
	}
	getter := envvars.ClientGetter{Client: opts.Client}
	var managedBy string
	live := make([]diff.LiveWorkloadEnv, 0, len(pairs))
	for _, p := range pairs {
		if managedBy == "" {
			managedBy = fluxManagedBy(p.Live)
		}
		env, err := envvars.Extract(ctx, getter, p.Live, nil)
		if err != nil {
			return nil, nil, "", fmt.Errorf("extracting env vars for %s %s/%s: %w", p.Live.GetKind(), p.Live.GetNamespace(), p.Live.GetName(), err)
		}
		live = append(live, diff.LiveWorkloadEnv{Rendered: p.Rendered, Env: env})
	}
	return live, missing, managedBy, nil
}

// baselineWorkloadEnvs reads the live side from a backup: one env file
// per live workload (WorkloadEnvFile), paired with its rendered
// counterpart by the same live name liveWorkloadEnvs would fetch — or,
// for a backup that has none (taken before per-workload files existed,
// or whose workloads no longer match what the chart renders), its merged
// env-vars.env, compared against every rendered workload at once.
// missing names the rendered workloads the backup holds nothing for.
func baselineWorkloadEnvs(opts Options, hrNamespace string, renderedDocs []*unstructured.Unstructured) ([]diff.LiveWorkloadEnv, []string, error) {
	var live []diff.LiveWorkloadEnv
	var missing []string
	for _, t := range workloadTargets(opts, hrNamespace, renderedDocs) {
		env, ok, err := readBaselineWorkloadEnv(opts.Baseline, WorkloadEnvFile(t.gvk.Kind, t.name))
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			missing = append(missing, t.String())
			continue
		}
		live = append(live, diff.LiveWorkloadEnv{Rendered: t.rendered, Env: env})
	}
	if len(live) > 0 {
		return live, missing, nil
	}
	env, err := readBaselineEnvFile(opts.Baseline, mergedEnvFile)
	if err != nil {
		return nil, nil, err
	}
	return []diff.LiveWorkloadEnv{{Env: env}}, nil, nil
}

// formatComparisons renders a chart diff's compared workloads as the
// before (rendered) and after (live) text internal/ui.FileDiff shows:
// sorted "KEY=VALUE" lines, each prefixed with its workload's name when
// more than one workload was compared, so a sibling's change reads as
// that sibling's.
func formatComparisons(workloads []diff.WorkloadComparison) (before, after string) {
	if len(workloads) == 1 {
		return formatEnvLines(workloads[0].Rendered, ""), formatEnvLines(workloads[0].Live, "")
	}
	var beforeParts, afterParts []string
	for _, w := range sortedComparisons(workloads) {
		beforeParts = appendNonEmpty(beforeParts, formatEnvLines(w.Rendered, w.Workload+"/"))
		afterParts = appendNonEmpty(afterParts, formatEnvLines(w.Live, w.Workload+"/"))
	}
	return strings.Join(beforeParts, "\n"), strings.Join(afterParts, "\n")
}

func sortedComparisons(workloads []diff.WorkloadComparison) []diff.WorkloadComparison {
	out := append([]diff.WorkloadComparison(nil), workloads...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Workload < out[j].Workload })
	return out
}

func appendNonEmpty(parts []string, s string) []string {
	if s == "" {
		return parts
	}
	return append(parts, s)
}

// chartValues are the values the chart renders with: its own values.yaml
// defaults under the rendered HelmRelease's spec.values, merged as Helm
// merges them (a list the HelmRelease sets replaces the default).
func chartValues(opts Options, rendered *render.Result) (map[string]any, error) {
	hrValues, _, err := unstructured.NestedMap(rendered.Object.Object, "spec", "values")
	if err != nil {
		return nil, fmt.Errorf("reading rendered HelmRelease spec.values: %w", err)
	}
	defaults := map[string]any{}
	data, err := os.ReadFile(filepath.Join(chartPath(opts), "values.yaml"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := yaml.Unmarshal(data, &defaults); err != nil {
			return nil, fmt.Errorf("parsing %s values.yaml: %w", opts.App.ChartPath, err)
		}
	}
	return diff.MergeValues(defaults, hrValues), nil
}

// settleUnmapped renders the chart again with result's patch applied
// and drops the unmapped diffs that render already settles (see
// diff.SettledByPatch), so review only lists what the patch really
// leaves different. If that render fails, every diff is kept.
func settleUnmapped(ctx context.Context, opts Options, rendered *render.Result, result *diff.ChartDiffResult) []diff.UnmappedDiff {
	patched, _, err := renderChartWith(ctx, opts, rendered, diff.PatchValues(result.Patch))
	if err != nil {
		if opts.Log != nil {
			opts.Log.Debugf("rendering %s with its patch applied: %v; keeping every unmapped difference", opts.App.Object, err)
		}
		return result.UnmappedDiffs
	}
	return diff.SettledByPatch(result.UnmappedDiffs, patched)
}

// renderChart runs `helm template` for opts.App's chart against the
// rendered HelmRelease's own values, namespace and release name (spec.
// releaseName, when the object's GitOps name and release name diverge —
// see diff.ReleaseName).
func renderChart(ctx context.Context, opts Options, rendered *render.Result) ([]*unstructured.Unstructured, string, error) {
	return renderChartWith(ctx, opts, rendered, nil)
}

// renderChartWith is renderChart with extraValues deep-merged over the
// rendered HelmRelease's spec.values, as a tenant patch would be.
func renderChartWith(ctx context.Context, opts Options, rendered *render.Result, extraValues map[string]any) ([]*unstructured.Unstructured, string, error) {
	hrValues, _, err := unstructured.NestedMap(rendered.Object.Object, "spec", "values")
	if err != nil {
		return nil, "", fmt.Errorf("reading rendered HelmRelease spec.values: %w", err)
	}
	if extraValues != nil {
		hrValues = diff.MergeValues(hrValues, extraValues)
	}
	namespace := rendered.Object.GetNamespace()
	releaseName := diff.ReleaseName(rendered.Object, opts.App.Object)
	docs, err := diff.HelmTemplate(ctx, opts.Runner, chartPath(opts), releaseName, namespace, hrValues)
	if err != nil {
		return nil, "", err
	}
	return docs, namespace, nil
}

// FetchLiveWorkloads fetches every Deployment/StatefulSet/DaemonSet
// renderedDocs declares from the live cluster, translating the one named
// App.Object to its live name (App.LiveName, when the redesign renamed it)
// and falling back to App.LiveNamespace. A workload the chart renders but that never
// existed live is skipped, not an error — the caller decides whether
// finding none of them is a failure. Exported for internal/backup, which
// runs the same chart-templating pipeline against a live (not
// flux-rendered) HelmRelease's own values to capture every sibling
// workload a multi-workload chart declares, not just the one named after
// the app itself.
func FetchLiveWorkloads(ctx context.Context, opts Options, hrNamespace string, renderedDocs []*unstructured.Unstructured) []*unstructured.Unstructured {
	pairs, _ := fetchWorkloadPairs(ctx, opts, hrNamespace, renderedDocs)
	live := make([]*unstructured.Unstructured, 0, len(pairs))
	for _, p := range pairs {
		live = append(live, p.Live)
	}
	return live
}

// workloadPair is a rendered workload and its live counterpart.
type workloadPair struct {
	Rendered, Live *unstructured.Unstructured
}

// fetchWorkloadPairs is FetchLiveWorkloads keeping each live workload
// paired with the rendered one it was fetched for; missing names the
// rendered workloads not found live.
func fetchWorkloadPairs(ctx context.Context, opts Options, hrNamespace string, renderedDocs []*unstructured.Unstructured) ([]workloadPair, []string) {
	var pairs []workloadPair
	var missing []string
	for _, t := range workloadTargets(opts, hrNamespace, renderedDocs) {
		obj, err := fetchWorkload(ctx, opts, t.gvk, t.namespace, t.name)
		if err != nil {
			missing = append(missing, t.String())
			continue
		}
		pairs = append(pairs, workloadPair{Rendered: t.rendered, Live: obj})
	}
	return pairs, missing
}

// RenderedWorkloadNames describes every workload renderedDocs declares as
// "Kind namespace/name", after the same live-name translation
// FetchLiveWorkloads applies — what it looked for, for explaining why it
// found none of them live.
func RenderedWorkloadNames(opts Options, hrNamespace string, renderedDocs []*unstructured.Unstructured) []string {
	targets := workloadTargets(opts, hrNamespace, renderedDocs)
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.String())
	}
	return names
}

// workloadTarget is one rendered workload and where it's expected live.
type workloadTarget struct {
	rendered        *unstructured.Unstructured
	gvk             schema.GroupVersionKind
	namespace, name string
}

// String describes t as "Kind namespace/name".
func (t workloadTarget) String() string {
	return fmt.Sprintf("%s %s/%s", t.gvk.Kind, t.namespace, t.name)
}

func workloadTargets(opts Options, hrNamespace string, renderedDocs []*unstructured.Unstructured) []workloadTarget {
	var targets []workloadTarget
	for _, kind := range workloadKinds {
		for _, doc := range yamldocs.FindByKind(renderedDocs, kind) {
			name := doc.GetName()
			if name == opts.App.Object {
				name = opts.App.LiveName()
			}
			namespace := doc.GetNamespace()
			if namespace == "" {
				namespace = hrNamespace
			}
			targets = append(targets, workloadTarget{rendered: doc, gvk: doc.GroupVersionKind(), namespace: namespace, name: name})
		}
	}
	return targets
}

func fetchWorkload(ctx context.Context, opts Options, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	return getWithLiveNamespaceFallback(ctx, opts, gvk, namespace, name)
}

// MergeLiveEnv resolves and merges every live workload's env vars — a
// later workload's variables overwrite an earlier one's — into the one
// flat view a backup's env-vars.env keeps (for drift checks, and as the
// fallback baseline). Chart-mode diffing itself compares each workload
// separately (see liveWorkloadEnvs), since a merge loses which sibling a
// same-named variable came from. Exported for internal/backup.
func MergeLiveEnv(ctx context.Context, opts Options, liveWorkloads []*unstructured.Unstructured) (map[string]string, error) {
	envs, err := LiveWorkloadEnvs(ctx, opts, liveWorkloads)
	if err != nil {
		return nil, err
	}
	merged := map[string]string{}
	for _, env := range envs {
		for k, v := range env {
			merged[k] = v
		}
	}
	return merged, nil
}

// LiveWorkloadEnvs resolves each live workload's env vars, in order.
// Exported for internal/backup, which writes one env file per workload
// (WorkloadEnvFile) next to the merged env-vars.env.
func LiveWorkloadEnvs(ctx context.Context, opts Options, liveWorkloads []*unstructured.Unstructured) ([]map[string]string, error) {
	getter := envvars.ClientGetter{Client: opts.Client}
	envs := make([]map[string]string, 0, len(liveWorkloads))
	for _, live := range liveWorkloads {
		env, err := envvars.Extract(ctx, getter, live, nil)
		if err != nil {
			return nil, fmt.Errorf("extracting env vars for %s %s/%s: %w", live.GetKind(), live.GetNamespace(), live.GetName(), err)
		}
		envs = append(envs, env)
	}
	return envs, nil
}

// formatEnvLines renders env as sorted "KEY=VALUE" lines, each prefixed
// with prefix, so internal/ui.FileDiff can show a chart-mode diff the same
// way it shows a manifest-mode one: as ordinary text, one line per changed
// entry.
func formatEnvLines(env map[string]string, prefix string) string {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, k := range names {
		lines = append(lines, prefix+k+"="+env[k])
	}
	return strings.Join(lines, "\n")
}

// LiveChartWorkloads renders app's chart (which must be chart-mode:
// App.ChartPath != "") and returns every live Deployment/StatefulSet/DaemonSet
// workload it declares. Exported for internal/backup, and errors
// immediately for a manifest-mode app rather than returning an empty
// result.
func LiveChartWorkloads(ctx context.Context, opts Options) ([]*unstructured.Unstructured, error) {
	if opts.App.ChartPath == "" {
		return nil, fmt.Errorf("app %q is manifest-mode (no chartPath); use LiveManifestObject instead", opts.App.ID)
	}
	rendered, err := renderApp(ctx, opts)
	if err != nil {
		return nil, err
	}
	renderedDocs, hrNamespace, err := renderChart(ctx, opts, rendered)
	if err != nil {
		return nil, err
	}
	live := FetchLiveWorkloads(ctx, opts, hrNamespace, renderedDocs)
	if len(live) == 0 {
		return nil, fmt.Errorf("no live workload found for chart %q among %v", opts.App.ChartPath, workloadKinds)
	}
	return live, nil
}
