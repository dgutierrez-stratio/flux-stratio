package appdiff

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
)

const rsetOutputHelmRelease = `
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps-psql-gosec-agent
  namespace: stratio-datastores
spec:
  path: components/gosec-agent/app/overlays/postgres/S
`

const kustomizationBuildOutputHelmRelease = `
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: psql-gosec-agent
  namespace: stratio-datastores
spec:
  values:
    gosecAgent:
      general:
        log:
          level: INFO
`

const helmTemplateOutputGosec = `
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: psql-gosec-agent-config
data:
  LOG_LEVEL: INFO
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: psql-gosec-agent
  namespace: stratio-datastores
spec:
  template:
    spec:
      containers:
        - name: main
`

func fixtureChart(t *testing.T, base string) string {
	t.Helper()
	chartDir := filepath.Join(base, "charts", "gosec-agent")
	configDir := filepath.Join(chartDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(chartDir, "charts"), 0o755); err != nil { // avoid a `helm dependency build` call
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\nname: gosec-agent\nversion: 0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "env_vars.yaml"), []byte(`LOG_LEVEL: {{ .Values.gosecAgent.general.log.level | quote }}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return "gosec-agent"
}

func chartDiffOptions(t *testing.T, base string) Options {
	return Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio",
		App: config.App{
			ID: "psql-gosec-agent", Rset: "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent",
			ChartPath: fixtureChart(t, base),
		},
		Runner: helmSim{&runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputHelmRelease)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputHelmRelease)},
			"helm":          {Stdout: []byte(helmTemplateOutputGosec)},
		}}},
		Log: log.New(io.Discard, false),
	}
}

func TestDiff_ChartMode_ProducesMappedPatch(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := chartDiffOptions(t, base)

	liveDeployment := deploymentWithEnv(t, "psql-gosec-agent", "stratio-datastores", "DEBUG")
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(liveDeployment).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if result.Patch == nil {
		t.Fatal("Patch = nil, want a patch for the changed LOG_LEVEL")
	}

	body, _ := result.Patch.Patch.(map[string]any)
	spec, _ := body["spec"].(map[string]any)
	values, _ := spec["values"].(map[string]any)
	gosecAgent, _ := values["gosecAgent"].(map[string]any)
	general, _ := gosecAgent["general"].(map[string]any)
	logSection, _ := general["log"].(map[string]any)
	if logSection["level"] != "DEBUG" {
		t.Errorf("level = %v, want %q", logSection["level"], "DEBUG")
	}
}

// A difference the app's exclude keeps out of the patch isn't dropped: it
// comes back in Result.Excluded, and the patch stays empty.
func TestDiff_ChartMode_ExcludedDifferenceIsReportedNotPatched(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := chartDiffOptions(t, base)
	opts.App.Exclude = []string{"spec.values.gosecAgent.general.log"}
	liveDeployment := deploymentWithEnv(t, "psql-gosec-agent", "stratio-datastores", "DEBUG")
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(liveDeployment).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if result.Patch != nil {
		t.Errorf("Patch = %+v, want nil: the only difference is excluded", result.Patch)
	}
	want := []diff.ExcludedDiff{{
		Workload: "psql-gosec-agent", Name: "LOG_LEVEL", Rendered: "INFO", Live: "DEBUG", Path: "gosecAgent.general.log.level",
	}}
	if !reflect.DeepEqual(result.Excluded, want) {
		t.Errorf("Excluded = %+v, want %+v", result.Excluded, want)
	}
}

func TestDiff_ChartMode_AppliesRenameForLiveLookup(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := chartDiffOptions(t, base)
	// the live cluster's old name for this object
	opts.App.Live = []config.ObjectRef{{Namespace: "stratio-datastores", Name: "psql-agent"}}

	liveDeployment := deploymentWithEnv(t, "psql-agent", "stratio-datastores", "WARN")
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(liveDeployment).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if result.Patch == nil {
		t.Fatal("Patch = nil, want the renamed live object to have been found and diffed")
	}
}

func TestDiff_ChartMode_NoLiveWorkloadErrors(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := chartDiffOptions(t, base)
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).Build() // nothing seeded

	if _, err := Diff(context.Background(), opts); err == nil {
		t.Fatal("Diff with no live workload present: got nil error, want non-nil")
	}
}

func TestDiff_ChartMode_UnmappedAndCountsPropagate(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := chartDiffOptions(t, base)

	dep := deploymentWithEnv(t, "psql-gosec-agent", "stratio-datastores", "INFO")
	// Add a live-only env var the chart doesn't render at all.
	dep.Spec.Template.Spec.Containers[0].Env = append(dep.Spec.Template.Spec.Containers[0].Env,
		corev1.EnvVar{Name: "LIVE_ONLY_VAR", Value: "x"})
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(dep).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if len(result.LiveOnly) != 1 || result.LiveOnly[0].Name != "LIVE_ONLY_VAR" || result.LiveOnly[0].Live != "x" {
		t.Errorf("LiveOnly = %+v, want LIVE_ONLY_VAR=x", result.LiveOnly)
	}
}

func deploymentWithEnv(t *testing.T, name, namespace, logLevel string) *appsv1.Deployment {
	t.Helper()
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "main",
						Env:  []corev1.EnvVar{{Name: "LOG_LEVEL", Value: logLevel}},
					}},
				},
			},
		},
	}
}

// TestDiff_ChartMode_ChartsRepoOutsideBase asserts a chart-mode app
// resolves its chart under Options.Repos.Charts when the charts repo is
// checked out outside base — the chart deliberately doesn't exist anywhere under
// base, so this fails loudly (a "no such file" error from `helm`) if
// chartPath ever regresses to resolving against base again.
func TestDiff_ChartMode_ChartsRepoOutsideBase(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	chartsRoot := t.TempDir() // the chart lives only here, never under base

	opts := Options{
		Repos: chartsRepoAt(base, filepath.Join(chartsRoot, "charts")), Cluster: "eosdev", Tenant: "stratio",
		App: config.App{
			ID: "psql-gosec-agent", Rset: "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent",
			ChartPath: fixtureChart(t, chartsRoot),
		},
		Runner: helmSim{&runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputHelmRelease)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputHelmRelease)},
			"helm":          {Stdout: []byte(helmTemplateOutputGosec)},
		}}},
		Log: log.New(io.Discard, false),
	}
	liveDeployment := deploymentWithEnv(t, "psql-gosec-agent", "stratio-datastores", "DEBUG")
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(liveDeployment).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error (chart should resolve under repos.charts, not base): %v", err)
	}
	if result.Patch == nil {
		t.Fatal("Patch = nil, want a patch for the changed LOG_LEVEL")
	}
}

func TestLiveChartWorkloads_Success(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := chartDiffOptions(t, base)
	liveDeployment := deploymentWithEnv(t, "psql-gosec-agent", "stratio-datastores", "DEBUG")
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(liveDeployment).Build()

	got, err := LiveChartWorkloads(context.Background(), opts)
	if err != nil {
		t.Fatalf("LiveChartWorkloads returned error: %v", err)
	}
	if len(got) != 1 || got[0].GetKind() != "Deployment" || got[0].GetName() != "psql-gosec-agent" {
		t.Errorf("got = %+v", got)
	}
}

func TestLiveChartWorkloads_NoneFoundErrors(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := chartDiffOptions(t, base)
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).Build()

	if _, err := LiveChartWorkloads(context.Background(), opts); err == nil {
		t.Fatal("LiveChartWorkloads with no live workload present: got nil error, want non-nil")
	}
}

// chartsRepoAt is the default layout under base, but with the charts
// repository checked out at charts instead.
func chartsRepoAt(base, charts string) config.RepoPaths {
	repos := config.ReposUnder(base)
	repos.Charts = charts
	return repos
}

// TestDiff_ChartMode_ValuesFromRefused: a HelmRelease whose values come
// partly from spec.valuesFrom can't be rendered as helm-controller would,
// and a spec.values patch would silently override those values.
func TestDiff_ChartMode_ValuesFromRefused(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := chartDiffOptions(t, base)
	withValuesFrom := strings.Replace(kustomizationBuildOutputHelmRelease, "spec:\n",
		"spec:\n  valuesFrom:\n    - kind: ConfigMap\n      name: gosec-agent-values\n", 1)
	opts.Runner = helmSim{&runner.Fake{Responses: map[string]runner.FakeResponse{
		"flux-operator": {Stdout: []byte(rsetOutputHelmRelease)},
		"flux":          {Stdout: []byte(withValuesFrom)},
		"helm":          {Stdout: []byte(helmTemplateOutputGosec)},
	}}}
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		deploymentWithEnv(t, "psql-gosec-agent", "stratio-datastores", "DEBUG")).Build()

	if _, err := Diff(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "valuesFrom") {
		t.Errorf("err = %v, want valuesFrom refused", err)
	}
}
