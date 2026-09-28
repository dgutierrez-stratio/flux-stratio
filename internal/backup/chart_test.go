package backup

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/discovery"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
)

const helmTemplateOutputSingleWorkload = `
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

const helmTemplateOutputMultiWorkload = `
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: genai-api
  namespace: stratio-genai
spec:
  template:
    spec:
      containers:
        - name: main
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: genai-gateway
  namespace: stratio-genai
spec:
  template:
    spec:
      containers:
        - name: main
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: genai-litellm
  namespace: stratio-genai
spec:
  template:
    spec:
      containers:
        - name: main
`

func fixtureChart(t *testing.T, base, name string) string {
	t.Helper()
	chartDir := filepath.Join(base, "charts", name)
	if err := os.MkdirAll(filepath.Join(chartDir, "charts"), 0o755); err != nil { // avoid a `helm dependency build` call
		t.Fatal(err)
	}
	return "charts/" + name
}

func deploymentWithEnv(name, namespace, logLevel string) *appsv1.Deployment {
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

func helmRelease(name, namespace string, values map[string]any) *unstructured.Unstructured {
	spec := map[string]any{}
	if values != nil {
		spec["values"] = values
	}
	return obj("helm.toolkit.fluxcd.io/v2", "HelmRelease", namespace, name, map[string]any{"spec": spec})
}

func TestRun_ChartMode_SingleWorkload_WritesDeploymentAndEnvFile(t *testing.T) {
	base := fixtureBase(t)
	chartPath := fixtureChart(t, base, "gosec-agent")
	backupsDir := t.TempDir()
	logger := log.New(io.Discard, false)

	hr := helmRelease("psql-gosec-agent", "stratio-datastores", map[string]any{})
	dep := deploymentWithEnv("psql-gosec-agent", "stratio-datastores", "DEBUG")

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(dep, hr).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Base: base,
		App: config.App{
			ID: "psql-gosec-agent", Object: "psql-gosec-agent", ChartPath: chartPath,
		},
		Index: idx,
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"helm": {Stdout: []byte(helmTemplateOutputSingleWorkload)},
		}},
		Client: c,
		Dir:    backupsDir,
		Clock:  fixedClock,
		Log:    logger,
	}

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	wantFiles := map[string]bool{"deployment.yaml": true, "env-vars.env": true}
	if len(result.Files) != 2 || !wantFiles[result.Files[0]] || !wantFiles[result.Files[1]] {
		t.Errorf("Files = %v, want [deployment.yaml env-vars.env] in some order", result.Files)
	}

	depData, err := os.ReadFile(filepath.Join(result.Dir, "deployment.yaml"))
	if err != nil {
		t.Fatalf("deployment.yaml not written: %v", err)
	}
	if !strings.Contains(string(depData), "psql-gosec-agent") {
		t.Errorf("deployment.yaml content = %s", depData)
	}

	envData, err := os.ReadFile(filepath.Join(result.Dir, "env-vars.env"))
	if err != nil {
		t.Fatalf("env-vars.env not written: %v", err)
	}
	if string(envData) != "LOG_LEVEL=DEBUG\n" {
		t.Errorf("env-vars.env content = %q, want %q", envData, "LOG_LEVEL=DEBUG\n")
	}
}

// TestRun_ChartMode_MultiWorkload_MergesEveryWorkloadsEnv is the exact
// scenario that motivated this package's redesign: a chart whose
// HelmRelease name (genai) differs from every workload name it declares
// (genai-api/genai-gateway/genai-litellm). All three siblings' env vars
// must end up merged into one env-vars.env, not just one of them.
func TestRun_ChartMode_MultiWorkload_MergesEveryWorkloadsEnv(t *testing.T) {
	base := fixtureBase(t)
	chartPath := fixtureChart(t, base, "genai")
	backupsDir := t.TempDir()
	logger := log.New(io.Discard, false)

	hr := helmRelease("genai", "stratio-genai", map[string]any{})
	api := deploymentWithEnv("genai-api", "stratio-genai", "API")
	gateway := deploymentWithEnv("genai-gateway", "stratio-genai", "GATEWAY")
	litellm := deploymentWithEnv("genai-litellm", "stratio-genai", "LITELLM")
	// Give each a distinct env var name so the merge is unambiguous.
	api.Spec.Template.Spec.Containers[0].Env[0].Name = "API_LOG_LEVEL"
	gateway.Spec.Template.Spec.Containers[0].Env[0].Name = "GATEWAY_LOG_LEVEL"
	litellm.Spec.Template.Spec.Containers[0].Env[0].Name = "LITELLM_LOG_LEVEL"

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(api, gateway, litellm, hr).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Base: base,
		App: config.App{
			ID: "genai", Object: "genai", ChartPath: chartPath,
		},
		Index: idx,
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"helm": {Stdout: []byte(helmTemplateOutputMultiWorkload)},
		}},
		Client: c,
		Dir:    backupsDir,
		Clock:  fixedClock,
		Log:    logger,
	}

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	envData, err := os.ReadFile(filepath.Join(result.Dir, "env-vars.env"))
	if err != nil {
		t.Fatalf("env-vars.env not written: %v", err)
	}
	for _, want := range []string{"API_LOG_LEVEL=API", "GATEWAY_LOG_LEVEL=GATEWAY", "LITELLM_LOG_LEVEL=LITELLM"} {
		if !strings.Contains(string(envData), want) {
			t.Errorf("env-vars.env = %q, want it to contain %q", envData, want)
		}
	}
}

// TestRun_ChartMode_ChartsBaseOverridesBase asserts a chart-mode app
// resolves its chart under Options.ChartsBase, not Options.Base, when
// ChartsBase is set — the chart deliberately doesn't exist anywhere under
// base, so this fails loudly if the resolution ever regresses to
// preferring Base again.
func TestRun_ChartMode_ChartsBaseOverridesBase(t *testing.T) {
	base := fixtureBase(t)
	chartsRoot := t.TempDir() // the chart lives only here, never under base
	chartPath := fixtureChart(t, chartsRoot, "gosec-agent")
	backupsDir := t.TempDir()
	logger := log.New(io.Discard, false)

	hr := helmRelease("psql-gosec-agent", "stratio-datastores", map[string]any{})
	dep := deploymentWithEnv("psql-gosec-agent", "stratio-datastores", "DEBUG")

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(dep, hr).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Base: base, ChartsBase: chartsRoot,
		App: config.App{
			ID: "psql-gosec-agent", Object: "psql-gosec-agent", ChartPath: chartPath,
		},
		Index: idx,
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"helm": {Stdout: []byte(helmTemplateOutputSingleWorkload)},
		}},
		Client: c,
		Dir:    backupsDir,
		Clock:  fixedClock,
		Log:    logger,
	}

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error (chart should resolve under ChartsBase, not Base): %v", err)
	}
	wantFiles := map[string]bool{"deployment.yaml": true, "env-vars.env": true}
	if len(result.Files) != 2 || !wantFiles[result.Files[0]] || !wantFiles[result.Files[1]] {
		t.Errorf("Files = %v, want [deployment.yaml env-vars.env] in some order", result.Files)
	}
}

// TestRun_ChartMode_TemplatesWithAppObjectNotLiveHelmReleaseName covers a
// renamed app: the live HelmRelease is found under its OLD/legacy name,
// but `helm template` must be invoked with App.Object (the NEW/GitOps
// name) — the same release name internal/appdiff's own renderChart uses
// — so a chart whose rendered resource names derive from .Release.Name
// produces names FetchLiveWorkloads' live-name translation can actually
// match, identically to how apps diff's own chart-mode render works.
func TestRun_ChartMode_TemplatesWithAppObjectNotLiveHelmReleaseName(t *testing.T) {
	base := fixtureBase(t)
	chartPath := fixtureChart(t, base, "gosec-agent")
	backupsDir := t.TempDir()
	logger := log.New(io.Discard, false)

	// Live under the OLD name; App.Object is the NEW/GitOps name.
	hr := helmRelease("psql-agent", "stratio-datastores", map[string]any{})
	dep := deploymentWithEnv("psql-agent", "stratio-datastores", "DEBUG")

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(dep, hr).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	fakeRunner := &runner.Fake{Responses: map[string]runner.FakeResponse{
		"helm": {Stdout: []byte(helmTemplateOutputSingleWorkload)},
	}}
	opts := Options{
		Base: base,
		App: config.App{
			ID: "psql-gosec-agent", Object: "psql-gosec-agent", ChartPath: chartPath,
			Live: []config.ObjectRef{{Namespace: "stratio-datastores", Name: "psql-agent"}},
		},
		Index:  idx,
		Runner: fakeRunner,
		Client: c,
		Dir:    backupsDir,
		Clock:  fixedClock,
		Log:    logger,
	}

	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var templateCall *runner.FakeCall
	for i := range fakeRunner.Calls {
		call := fakeRunner.Calls[i]
		if call.Name == "helm" && len(call.Args) > 1 && call.Args[0] == "template" {
			templateCall = &fakeRunner.Calls[i]
		}
	}
	if templateCall == nil {
		t.Fatal("no `helm template` call recorded")
	}
	if templateCall.Args[1] != "psql-gosec-agent" {
		t.Errorf("helm template release name = %q, want %q (App.Object)", templateCall.Args[1], "psql-gosec-agent")
	}
}

func TestRun_ChartMode_NoLiveWorkloads_DegradesToHelmReleaseAndValues(t *testing.T) {
	base := fixtureBase(t)
	chartPath := fixtureChart(t, base, "gosec-agent")
	backupsDir := t.TempDir()
	var logbuf bytes.Buffer
	logger := log.New(&logbuf, false)

	hr := helmRelease("psql-gosec-agent", "stratio-datastores", map[string]any{"replicaCount": int64(2)})
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(hr).Build() // no live Deployment
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Base: base,
		App: config.App{
			ID: "psql-gosec-agent", Object: "psql-gosec-agent", ChartPath: chartPath,
		},
		Index: idx,
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"helm": {Stdout: []byte("")},
		}},
		Client: c,
		Dir:    backupsDir,
		Clock:  fixedClock,
		Log:    logger,
	}

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	wantFiles := map[string]bool{"helmrelease.yaml": true, "values.yaml": true}
	if len(result.Files) != 2 || !wantFiles[result.Files[0]] || !wantFiles[result.Files[1]] {
		t.Errorf("Files = %v, want [helmrelease.yaml values.yaml] in some order", result.Files)
	}
	if !strings.Contains(logbuf.String(), "declares no live workloads") {
		t.Errorf("expected a baseline-incompatibility warning, got: %s", logbuf.String())
	}
	data, err := os.ReadFile(filepath.Join(result.Dir, "values.yaml"))
	if err != nil {
		t.Fatalf("values.yaml not written: %v", err)
	}
	if !strings.Contains(string(data), "replicaCount: 2") {
		t.Errorf("values.yaml content = %s", data)
	}
}

func TestRun_ChartMode_HelmReleaseValuesFromConfigMap(t *testing.T) {
	base := fixtureBase(t)
	chartPath := fixtureChart(t, base, "gosec-agent")
	backupsDir := t.TempDir()
	logger := log.New(io.Discard, false)

	hr := helmRelease("psql-gosec-agent", "stratio-datastores", nil) // no inline values
	hr.Object["spec"].(map[string]any)["valuesFrom"] = []any{
		map[string]any{"kind": "ConfigMap", "name": "00-psql-gosec-agent-values", "valuesKey": "values.yaml"},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "00-psql-gosec-agent-values", Namespace: "stratio-datastores"},
		Data:       map[string]string{"values.yaml": "replicaCount: 5\n"},
	}

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(cm, hr).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Base: base,
		App: config.App{
			ID: "psql-gosec-agent", Object: "psql-gosec-agent", ChartPath: chartPath,
		},
		Index: idx,
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"helm": {Stdout: []byte("")},
		}},
		Client: c,
		Dir:    backupsDir,
		Clock:  fixedClock,
		Log:    logger,
	}

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(result.Dir, "values.yaml"))
	if err != nil {
		t.Fatalf("values.yaml not written: %v", err)
	}
	if !strings.Contains(string(data), "replicaCount: 5") {
		t.Errorf("values.yaml content = %s, want it resolved from the ConfigMap", data)
	}
}

func TestRun_ChartMode_LiveObjectNotFoundErrors(t *testing.T) {
	base := fixtureBase(t)
	chartPath := fixtureChart(t, base, "gosec-agent")
	backupsDir := t.TempDir()
	logger := log.New(io.Discard, false)

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Base: base,
		App: config.App{
			ID: "psql-gosec-agent", Object: "psql-gosec-agent", ChartPath: chartPath,
		},
		Index:  idx,
		Runner: &runner.Fake{},
		Client: c,
		Dir:    backupsDir,
		Clock:  fixedClock,
		Log:    logger,
	}

	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("Run with no live object present: got nil error, want non-nil")
	}
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("backupsDir has %d entries, want 0", len(entries))
	}
}
