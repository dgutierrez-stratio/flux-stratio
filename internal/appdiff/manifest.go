package appdiff

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/render"
)

// manifestDiff compares rendered.Object directly against the live cluster
// object of the same kind.
func manifestDiff(ctx context.Context, opts Options, rendered *render.Result) (*Result, error) {
	var live *unstructured.Unstructured
	var err error
	if opts.Baseline != "" {
		live, err = readBaselineYAML(opts.Baseline, "cr.yaml")
	} else {
		live, err = fetchLiveManifestObject(ctx, opts, rendered.Object.GroupVersionKind(), rendered.Object.GetNamespace())
	}
	if err != nil {
		return nil, err
	}

	patch, err := diff.ManifestDiff(rendered.Object, live, opts.App.Exclude)
	if err != nil {
		return nil, err
	}

	localYAML, _ := yaml.Marshal(rendered.Object.Object["spec"])
	liveYAML, _ := yaml.Marshal(live.Object["spec"])
	result := &Result{Patch: patch, Before: string(localYAML), After: string(liveYAML)}
	if opts.Baseline == "" {
		result.FluxManagedBy = fluxManagedBy(live)
	}
	return result, nil
}

// fetchLiveManifestObject fetches the live cluster object at gvk/namespace,
// falling back to the namespace the app's live object was classified in
// (App.LiveNamespace) if it isn't found there.
func fetchLiveManifestObject(ctx context.Context, opts Options, gvk schema.GroupVersionKind, namespace string) (*unstructured.Unstructured, error) {
	name := opts.App.LiveName()
	live, err := getWithLiveNamespaceFallback(ctx, opts, gvk, namespace, name)
	if err != nil {
		return nil, fmtNotFound(gvk.Kind, namespace, name, err)
	}
	return live, nil
}

// LiveManifestObject renders app (which must be manifest-mode:
// App.ChartPath == "") and returns its live cluster counterpart. Exported
// for internal/backup, which captures exactly what apps diff/migrate
// would compare against, and errors immediately for a chart-mode app
// rather than returning a nonsensical empty result.
func LiveManifestObject(ctx context.Context, opts Options) (*unstructured.Unstructured, error) {
	if opts.App.ChartPath != "" {
		return nil, fmt.Errorf("app %q is chart-mode (chartPath set); use LiveChartWorkloads instead", opts.App.ID)
	}
	rendered, err := renderApp(ctx, opts)
	if err != nil {
		return nil, err
	}
	return fetchLiveManifestObject(ctx, opts, rendered.Object.GroupVersionKind(), rendered.Object.GetNamespace())
}

// getWithLiveNamespaceFallback gets gvk namespace/name, retrying in
// App.LiveNamespace when it's not found and that namespace differs — the
// legacy object may still live where CCT put it, not where the GitOps
// redesign's rendered manifest now expects it.
func getWithLiveNamespaceFallback(ctx context.Context, opts Options, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	live, err := kubeclient.GetUnstructured(ctx, opts.Client, gvk, namespace, name)
	if liveNS := opts.App.LiveNamespace(); err != nil && kubeclient.IsNotFound(err) && liveNS != "" && liveNS != namespace {
		live, err = kubeclient.GetUnstructured(ctx, opts.Client, gvk, liveNS, name)
	}
	return live, err
}
