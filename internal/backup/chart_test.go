package backup

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
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
  values: {}
`

const helmTemplateOutputGosec = `
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
	if err := os.MkdirAll(filepath.Join(chartDir, "charts"), 0o755); err != nil { // avoid a `helm dependency build` call
		t.Fatal(err)
	}
	return "charts/gosec-agent"
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

func TestRun_ChartMode_WritesDeploymentAndEnvFile(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	backupsDir := t.TempDir()

	opts := Options{
		Base: base, Cluster: "eosdev", Tenant: "stratio",
		App: config.App{
			ID: "psql-gosec-agent", Rset: "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent",
			ChartPath: fixtureChart(t, base),
		},
		Dir:   backupsDir,
		Clock: fixedClock,
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputHelmRelease)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputHelmRelease)},
			"helm":          {Stdout: []byte(helmTemplateOutputGosec)},
		}},
		Client: fake.NewClientBuilder().WithScheme(mustScheme(t)).
			WithObjects(deploymentWithEnv("psql-gosec-agent", "stratio-datastores", "DEBUG")).Build(),
		Log: log.New(io.Discard, false),
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

func TestRun_ChartMode_NoLiveWorkloadWritesNothing(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	backupsDir := t.TempDir()

	opts := Options{
		Base: base, Cluster: "eosdev", Tenant: "stratio",
		App: config.App{
			ID: "psql-gosec-agent", Rset: "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent",
			ChartPath: fixtureChart(t, base),
		},
		Dir:   backupsDir,
		Clock: fixedClock,
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputHelmRelease)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputHelmRelease)},
			"helm":          {Stdout: []byte(helmTemplateOutputGosec)},
		}},
		Client: fake.NewClientBuilder().WithScheme(mustScheme(t)).Build(), // nothing seeded
		Log:    log.New(io.Discard, false),
	}

	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("Run with no live workload present: got nil error, want non-nil")
	}
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("backupsDir has %d entries, want 0", len(entries))
	}
}
