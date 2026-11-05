package appdiff

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/runner"
)

// helmTemplateOutputWithSpec is helmTemplateOutputGosec with the workload
// settings a spec comparison looks at.
var helmTemplateOutputWithSpec = strings.Replace(helmTemplateOutputGosec, `spec:
  template:
    spec:
      containers:
        - name: main
`, `spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: main
          image: registry/gosec-agent:3.0.5
          resources:
            limits:
              memory: 2048Mi
`, 1)

func specDiffOptions(t *testing.T) Options {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := chartDiffOptions(t, base)
	opts.Runner = helmSim{&runner.Fake{Responses: map[string]runner.FakeResponse{
		"flux-operator": {Stdout: []byte(rsetOutputHelmRelease)},
		"flux":          {Stdout: []byte(kustomizationBuildOutputHelmRelease)},
		"helm":          {Stdout: []byte(helmTemplateOutputWithSpec)},
	}}}
	return opts
}

func specsByField(diffs []diff.SpecDiff) map[string]diff.SpecDiff {
	out := map[string]diff.SpecDiff{}
	for _, d := range diffs {
		out[d.Field] = d
	}
	return out
}

func checkSpecDiffs(t *testing.T, diffs []diff.SpecDiff) {
	t.Helper()
	got := specsByField(diffs)
	for field, want := range map[string]diff.SpecDiff{
		"resources.limits.memory": {Rendered: "2048Mi", Live: "4096M", Severity: diff.SpecRisk},
		"image":                   {Rendered: "registry/gosec-agent:3.0.5", Live: "registry/gosec-agent:3.0.4", Severity: diff.SpecRisk},
		"replicas":                {Rendered: "1", Live: "0", Severity: diff.SpecRisk},
	} {
		d, ok := got[field]
		if !ok {
			t.Errorf("SpecDiffs has no %s: %+v", field, diffs)
			continue
		}
		if d.Workload != "psql-gosec-agent" || d.Rendered != want.Rendered || d.Live != want.Live || d.Severity != want.Severity {
			t.Errorf("%s = %+v, want rendered %q live %q %s", field, d, want.Rendered, want.Live, want.Severity)
		}
	}
	if len(got) != 3 {
		t.Errorf("SpecDiffs = %+v, want exactly memory limit, image and replicas", diffs)
	}
}

func TestDiff_ChartMode_ReportsSpecDiffs(t *testing.T) {
	opts := specDiffOptions(t)
	dep := deploymentWithEnv(t, "psql-gosec-agent", "stratio-datastores", "INFO")
	zero := int32(0)
	dep.Spec.Replicas = &zero
	dep.Spec.Template.Spec.Containers[0].Image = "registry/gosec-agent:3.0.4"
	dep.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4096M")},
	}
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(dep).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	checkSpecDiffs(t, result.SpecDiffs)
}

// TestDiff_ChartMode_BaselineSpecDiffs reads the live side from the backup's
// deployment.yaml, which names the workload by what it declares.
func TestDiff_ChartMode_BaselineSpecDiffs(t *testing.T) {
	opts := specDiffOptions(t)
	opts.Baseline = t.TempDir()
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: psql-gosec-agent
  namespace: stratio-datastores
status:
  replicas: 3
spec:
  replicas: 0
  template:
    spec:
      containers:
        - name: main
          image: registry/gosec-agent:3.0.4
          resources:
            limits:
              memory: 4096M
`
	for name, content := range map[string]string{
		"env-vars.env": "LOG_LEVEL=INFO\n",
		"env-vars.deployment.psql-gosec-agent.env": "LOG_LEVEL=INFO\n",
		"deployment.yaml":                          manifest,
	} {
		if err := os.WriteFile(filepath.Join(opts.Baseline, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).Build() // never touched in baseline mode

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	checkSpecDiffs(t, result.SpecDiffs)
}

// A backup taken before manifests were kept has none: nothing is compared.
func TestDiff_ChartMode_BaselineWithoutManifestHasNoSpecDiffs(t *testing.T) {
	opts := specDiffOptions(t)
	opts.Baseline = t.TempDir()
	for name, content := range map[string]string{
		"env-vars.env": "LOG_LEVEL=INFO\n",
		"env-vars.deployment.psql-gosec-agent.env": "LOG_LEVEL=INFO\n",
	} {
		if err := os.WriteFile(filepath.Join(opts.Baseline, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if len(result.SpecDiffs) != 0 {
		t.Errorf("SpecDiffs = %+v, want none without a backed-up manifest", result.SpecDiffs)
	}
}

const specChartValues = `controllers:
  main:
    containers:
      main: {}
`

// specCarryOptions is specDiffOptions for a chart whose bjw-s values name the
// workload's controller, with a live Deployment holding more memory, a CPU
// limit the render has none for, and a different image tag.
func specCarryOptions(t *testing.T) (Options, *appsv1.Deployment) {
	opts := specDiffOptions(t)
	valuesFile := filepath.Join(opts.Repos.Charts, "gosec-agent", "values.yaml")
	if err := os.WriteFile(valuesFile, []byte(specChartValues), 0o644); err != nil {
		t.Fatal(err)
	}
	dep := deploymentWithEnv(t, "psql-gosec-agent", "stratio-datastores", "INFO")
	dep.Spec.Template.Labels["app.kubernetes.io/controller"] = "main"
	dep.Spec.Template.Spec.Containers[0].Image = "registry/gosec-agent:3.0.4"
	dep.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4096M")},
	}
	return opts, dep
}

// TestDiff_ChartMode_CarriesLiveResources: the legacy resources reach the
// patch through the controller's container values, the patched render then
// reproduces them, and only what the patch can't carry (the image tag) stays
// a spec difference.
func TestDiff_ChartMode_CarriesLiveResources(t *testing.T) {
	opts, dep := specCarryOptions(t)
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(dep).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	res, _, _ := unstructured.NestedMap(diff.PatchValues(result.Patch), "controllers", "main", "containers", "main", "resources")
	wantRes := map[string]any{
		"requests": map[string]any{"cpu": "4"},
		"limits":   map[string]any{"memory": "4096M"},
	}
	if !reflect.DeepEqual(res, wantRes) {
		t.Errorf("patched resources = %v, want %v", res, wantRes)
	}
	if len(result.CarriedResources) != 1 || result.CarriedResources[0].Path != "controllers.main.containers.main.resources" {
		t.Errorf("CarriedResources = %+v, want one at controllers.main.containers.main.resources", result.CarriedResources)
	}
	fields := specsByField(result.SpecDiffs)
	if _, ok := fields["image"]; !ok || len(fields) != 1 {
		t.Errorf("SpecDiffs = %+v, want only the image tag, which is never patched", result.SpecDiffs)
	}
}

// A chart that doesn't read the resources values doesn't reproduce them:
// the carry is taken back out of the patch and the differences stay.
func TestDiff_ChartMode_UnreproducedResourcesAreNotCarried(t *testing.T) {
	opts, dep := specCarryOptions(t)
	opts.Runner = &runner.Fake{Responses: map[string]runner.FakeResponse{
		"flux-operator": {Stdout: []byte(rsetOutputHelmRelease)},
		"flux":          {Stdout: []byte(kustomizationBuildOutputHelmRelease)},
		"helm":          {Stdout: []byte(helmTemplateOutputWithSpec)},
	}}
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(dep).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if result.Patch != nil || len(result.CarriedResources) != 0 {
		t.Errorf("patch = %v, carried = %+v, want neither: the render doesn't reproduce them", result.Patch, result.CarriedResources)
	}
	if d, ok := specsByField(result.SpecDiffs)["resources.limits.memory"]; !ok || d.Severity != diff.SpecRisk {
		t.Errorf("SpecDiffs = %+v, want the memory limit still reported as a risk", result.SpecDiffs)
	}
}

// A chart selecting on other labels than the legacy workload can't be applied
// over it: a risk, unless the app's prepare step deletes the legacy workload.
func TestDiff_ChartMode_SelectorDifference(t *testing.T) {
	for name, tt := range map[string]struct {
		prepare string
		want    diff.SpecSeverity
	}{
		"no prepare step":           {"", diff.SpecRisk},
		"a step that only edits":    {"prepare-datamarket-agent", diff.SpecRisk},
		"a step that deletes it":    {"prepare-dlc", diff.SpecInfo},
		"an unknown step is no one": {"nope", diff.SpecRisk},
	} {
		t.Run(name, func(t *testing.T) {
			opts := specDiffOptions(t)
			opts.App.Prepare = tt.prepare
			opts.Runner = helmSim{&runner.Fake{Responses: map[string]runner.FakeResponse{
				"flux-operator": {Stdout: []byte(rsetOutputHelmRelease)},
				"flux":          {Stdout: []byte(kustomizationBuildOutputHelmRelease)},
				"helm": {Stdout: []byte(strings.Replace(helmTemplateOutputWithSpec, "spec:\n  replicas: 1\n",
					"spec:\n  replicas: 1\n  selector:\n    matchLabels:\n      app.kubernetes.io/name: gosec\n", 1))},
			}}}
			dep := deploymentWithEnv(t, "psql-gosec-agent", "stratio-datastores", "INFO")
			opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(dep).Build()

			result, err := Diff(context.Background(), opts)
			if err != nil {
				t.Fatalf("Diff returned error: %v", err)
			}
			d, ok := specsByField(result.SpecDiffs)[diff.SelectorField]
			if !ok || d.Severity != tt.want || d.Live != "app=psql-gosec-agent" {
				t.Errorf("selector diff = %+v (found %v), want severity %s against the live app label", d, ok, tt.want)
			}
		})
	}
}

const rsetOutputWithTenantPatch = `
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps-psql-gosec-agent
  namespace: stratio-datastores
spec:
  path: components/gosec-agent/app/overlays/postgres/S
  patches:
    - patch: |
        kind: HelmRelease
        metadata:
          name: psql-gosec-agent
        spec:
          values:
            gosecAgent:
              general:
                log:
                  level: INFO
      target:
        kind: HelmRelease
`

// withTenantPatch is specCarryOptions for a tenant file that already patches the app.
func withTenantPatch(t *testing.T) (Options, *appsv1.Deployment) {
	opts, dep := specCarryOptions(t)
	opts.Runner = helmSim{&runner.Fake{Responses: map[string]runner.FakeResponse{
		"flux-operator": {Stdout: []byte(rsetOutputWithTenantPatch)},
		"flux":          {Stdout: []byte(kustomizationBuildOutputHelmRelease)},
		"helm":          {Stdout: []byte(helmTemplateOutputWithSpec)},
	}}}
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(dep).Build()
	return opts, dep
}

// apps diff compares the tenant file as it is: with a patch in place it is
// applied (rendered once), what the diff computes is only a delta over it,
// so there is no "whole patch" to compare the existing one with and the live
// resources are reported as differences instead of carried into the delta.
func TestDiff_TenantPatchIsAppliedByDefault(t *testing.T) {
	opts, _ := withTenantPatch(t)

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if result.ExistingPatches != 1 {
		t.Errorf("ExistingPatches = %d, want the tenant's one patch", result.ExistingPatches)
	}
	if result.UpToDate || result.ObsoletePatches != 0 || len(result.DroppedFromExisting) != 0 {
		t.Errorf("UpToDate/Obsolete/Dropped = %v/%d/%v, want none: they compare against the whole patch", result.UpToDate, result.ObsoletePatches, result.DroppedFromExisting)
	}
	if len(result.CarriedResources) != 0 {
		t.Errorf("CarriedResources = %+v, want none: a delta must not hide what the patch leaves different", result.CarriedResources)
	}
	if d, ok := specsByField(result.SpecDiffs)["resources.limits.memory"]; !ok || d.Severity != diff.SpecRisk {
		t.Errorf("SpecDiffs = %+v, want the memory limit still reported as a risk", result.SpecDiffs)
	}
}

// apps migrate renders the pristine base instead (StripTenantPatch) and
// carries the resources into the whole patch, whatever the file holds.
func TestDiff_StripTenantPatchComputesTheWholePatch(t *testing.T) {
	opts, _ := withTenantPatch(t)
	opts.StripTenantPatch = true

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if result.ExistingPatches != 1 {
		t.Errorf("ExistingPatches = %d, want 1", result.ExistingPatches)
	}
	if len(result.CarriedResources) != 1 {
		t.Errorf("CarriedResources = %+v, want the live resources carried into the whole patch", result.CarriedResources)
	}
	if result.Patch == nil {
		t.Error("Patch = nil, want the whole patch")
	}
}
