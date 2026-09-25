package appdiff

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/envvars"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
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

	var liveEnv map[string]string
	if opts.Baseline != "" {
		liveEnv, err = readBaselineEnvFile(opts.Baseline, "env-vars.env")
	} else {
		liveWorkloads := FetchLiveWorkloads(ctx, opts, hrNamespace, renderedDocs)
		if len(liveWorkloads) == 0 {
			return nil, fmt.Errorf("no live workload found for chart %q among %v", opts.App.ChartPath, workloadKinds)
		}
		liveEnv, err = MergeLiveEnv(ctx, opts, liveWorkloads)
	}
	if err != nil {
		return nil, err
	}

	valuesMap, err := diff.BuildChartValuesMap(chartPath(opts), opts.App.ValuesRoot)
	if err != nil {
		return nil, fmt.Errorf("mapping chart env vars to .Values paths: %w", err)
	}

	result := diff.ChartDiff(renderedDocs, liveEnv, valuesMap, opts.App.Object, opts.App.Exclude)
	return &Result{
		Patch:             result.Patch,
		Before:            formatEnvLines(result.RenderedEnv),
		After:             formatEnvLines(liveEnv),
		UnmappedDiffs:     result.UnmappedDiffs,
		LiveOnlyCount:     result.LiveOnlyCount,
		RenderedOnlyCount: result.RenderedOnlyCount,
	}, nil
}

// renderChart runs `helm template` for opts.App's chart against the
// rendered HelmRelease's own values and namespace.
func renderChart(ctx context.Context, opts Options, rendered *render.Result) ([]*unstructured.Unstructured, string, error) {
	hrValues, _, err := unstructured.NestedMap(rendered.Object.Object, "spec", "values")
	if err != nil {
		return nil, "", fmt.Errorf("reading rendered HelmRelease spec.values: %w", err)
	}
	namespace := rendered.Object.GetNamespace()
	docs, err := diff.HelmTemplate(ctx, opts.Runner, chartPath(opts), opts.App.Object, namespace, hrValues)
	if err != nil {
		return nil, "", err
	}
	return docs, namespace, nil
}

// FetchLiveWorkloads fetches every Deployment/StatefulSet/DaemonSet
// renderedDocs declares from the live cluster, applying App.Renamed and
// App.PreviousNamespace. A workload the chart renders but that never
// existed live is skipped, not an error — the caller decides whether
// finding none of them is a failure. Exported for internal/backup, which
// runs the same chart-templating pipeline against a live (not
// flux-rendered) HelmRelease's own values to capture every sibling
// workload a multi-workload chart declares, not just the one named after
// the app itself.
func FetchLiveWorkloads(ctx context.Context, opts Options, hrNamespace string, renderedDocs []*unstructured.Unstructured) []*unstructured.Unstructured {
	var workloadDocs []*unstructured.Unstructured
	for _, kind := range workloadKinds {
		workloadDocs = append(workloadDocs, yamldocs.FindByKind(renderedDocs, kind)...)
	}

	var live []*unstructured.Unstructured
	for _, doc := range workloadDocs {
		name := doc.GetName()
		if name == opts.App.Object && opts.App.Renamed != "" {
			name = opts.App.Renamed
		}
		namespace := doc.GetNamespace()
		if namespace == "" {
			namespace = hrNamespace
		}
		obj, err := fetchWorkload(ctx, opts, doc.GroupVersionKind(), namespace, name)
		if err != nil {
			continue
		}
		live = append(live, obj)
	}
	return live
}

func fetchWorkload(ctx context.Context, opts Options, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	live, err := kubeclient.GetUnstructured(ctx, opts.Client, gvk, namespace, name)
	if err != nil && kubeclient.IsNotFound(err) && opts.App.PreviousNamespace != "" {
		live, err = kubeclient.GetUnstructured(ctx, opts.Client, gvk, opts.App.PreviousNamespace, name)
	}
	return live, err
}

// MergeLiveEnv resolves and merges every live workload's env vars — a
// later workload's variables overwrite an earlier one's, extending
// internal/envvars.Extract's own within-workload semantics across
// workloads, since this is the "effective config" view diffed against the
// chart as a whole, not any one container's view. Exported for
// internal/backup, which captures the same merged view apps diff/migrate
// would compare against.
func MergeLiveEnv(ctx context.Context, opts Options, liveWorkloads []*unstructured.Unstructured) (map[string]string, error) {
	getter := envvars.ClientGetter{Client: opts.Client}
	merged := map[string]string{}
	for _, live := range liveWorkloads {
		env, err := envvars.Extract(ctx, getter, live, nil)
		if err != nil {
			return nil, fmt.Errorf("extracting env vars for %s %s/%s: %w", live.GetKind(), live.GetNamespace(), live.GetName(), err)
		}
		for k, v := range env {
			merged[k] = v
		}
	}
	return merged, nil
}

// formatEnvLines renders env as sorted "KEY=VALUE" lines, so
// internal/ui.FileDiff can show a chart-mode diff the same way it shows a
// manifest-mode one: as ordinary text, one line per changed entry.
func formatEnvLines(env map[string]string) string {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, k := range names {
		lines = append(lines, k+"="+env[k])
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
