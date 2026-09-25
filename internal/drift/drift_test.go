package drift

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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/discovery"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
)

func mustScheme(t *testing.T) *apiruntime.Scheme {
	t.Helper()
	s := apiruntime.NewScheme()
	for _, add := range []func(*apiruntime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func fixtureChart(t *testing.T, base, name string) string {
	t.Helper()
	chartDir := filepath.Join(base, "charts", name)
	if err := os.MkdirAll(filepath.Join(chartDir, "charts"), 0o755); err != nil { // avoid a `helm dependency build` call
		t.Fatal(err)
	}
	return "charts/" + name
}

func writeBackupFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRun_ManifestMode_DiffsSpecOnly(t *testing.T) {
	logger := log.New(io.Discard, false)
	live := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{
			"name": "psql", "namespace": "stratio-datastores",
			"resourceVersion": "999", // must never leak into the diff
		},
		"spec": map[string]any{"instances": int64(5)},
	}}
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(live).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	backupDir := t.TempDir()
	writeBackupFile(t, backupDir, "cr.yaml", "apiVersion: postgres.stratio.com/v1\nkind: PgCluster\nmetadata:\n    name: psql\n    resourceVersion: \"1\"\nspec:\n    instances: 3\n")

	opts := Options{
		Base:    t.TempDir(),
		App:     config.App{ID: "psql", Object: "psql"},
		Index:   idx,
		Against: backupDir,
		Log:     logger,
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(result.Before, "instances: 3") {
		t.Errorf("Before = %q, want it to contain the backup's instances count", result.Before)
	}
	if !strings.Contains(result.After, "instances: 5") {
		t.Errorf("After = %q, want it to contain the live instances count", result.After)
	}
	if strings.Contains(result.Before, "resourceVersion") || strings.Contains(result.After, "resourceVersion") {
		t.Errorf("resourceVersion leaked into the diff: Before=%q After=%q", result.Before, result.After)
	}
}

func TestRun_ChartMode_DiffsEnvVarsOnly(t *testing.T) {
	logger := log.New(io.Discard, false)
	live := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "eureka-agent", Namespace: "stratio-datastores"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "eureka-agent"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "eureka-agent"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "main",
						Env:  []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "DEBUG"}},
					}},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(live).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	backupDir := t.TempDir()
	writeBackupFile(t, backupDir, "deployment.yaml", "kind: Deployment\n")
	writeBackupFile(t, backupDir, "env-vars.env", "LOG_LEVEL=INFO\n")

	opts := Options{
		Base: t.TempDir(),
		App: config.App{
			ID: "eureka-agent", Object: "eureka-agent", ChartPath: "charts/eureka-agent",
		},
		Index:   idx,
		Against: backupDir,
		Log:     logger,
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Before != "LOG_LEVEL=INFO\n" {
		t.Errorf("Before = %q, want %q", result.Before, "LOG_LEVEL=INFO\n")
	}
	if result.After != "LOG_LEVEL=DEBUG\n" {
		t.Errorf("After = %q, want %q", result.After, "LOG_LEVEL=DEBUG\n")
	}
}

// TestRun_ChartMode_HelmReleaseSeeded_ThreadsChartsBase covers the path
// TestRun_ChartMode_DiffsEnvVarsOnly doesn't: a live HelmRelease (not just
// a Deployment) present, so backup.Run's captureChartFromHelmRelease —
// the one place that actually joins a base dir with App.ChartPath into a
// chart directory — is exercised. The chart lives only under ChartsBase,
// never under Base, so this fails loudly if drift.Run's Options.ChartsBase
// ever stops reaching backup.Options.ChartsBase.
func TestRun_ChartMode_HelmReleaseSeeded_ThreadsChartsBase(t *testing.T) {
	logger := log.New(io.Discard, false)
	chartsRoot := t.TempDir()
	chartPath := fixtureChart(t, chartsRoot, "eureka-agent")

	hr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "eureka-agent", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"values": map[string]any{}},
	}}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "eureka-agent", Namespace: "stratio-datastores"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "eureka-agent"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "eureka-agent"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "main",
						Env:  []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "DEBUG"}},
					}},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(dep, hr).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	backupDir := t.TempDir()
	writeBackupFile(t, backupDir, "deployment.yaml", "kind: Deployment\n")
	writeBackupFile(t, backupDir, "env-vars.env", "LOG_LEVEL=INFO\n")

	opts := Options{
		Base: t.TempDir(), ChartsBase: chartsRoot, // chart deliberately absent under Base
		App: config.App{
			ID: "eureka-agent", Object: "eureka-agent", ChartPath: chartPath,
		},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"helm": {Stdout: []byte(`
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: eureka-agent
  namespace: stratio-datastores
spec:
  template:
    spec:
      containers:
        - name: main
`)},
		}},
		Client:  c,
		Index:   idx,
		Against: backupDir,
		Log:     logger,
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error (chart should resolve under ChartsBase, not Base): %v", err)
	}
	if result.Before != "LOG_LEVEL=INFO\n" {
		t.Errorf("Before = %q, want %q", result.Before, "LOG_LEVEL=INFO\n")
	}
	if result.After != "LOG_LEVEL=DEBUG\n" {
		t.Errorf("After = %q, want %q", result.After, "LOG_LEVEL=DEBUG\n")
	}
}

func TestRun_ShapeMismatch_ReturnsClearError(t *testing.T) {
	logger := log.New(io.Discard, false)
	live := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{},
	}}
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(live).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	backupDir := t.TempDir()
	// The stored backup was captured as HelmRelease-only, not a CR.
	writeBackupFile(t, backupDir, "helmrelease.yaml", "kind: HelmRelease\n")
	writeBackupFile(t, backupDir, "values.yaml", "a: 1\n")

	opts := Options{
		Base:    t.TempDir(),
		App:     config.App{ID: "psql", Object: "psql"},
		Index:   idx,
		Against: backupDir,
		Log:     logger,
	}
	_, err = Run(context.Background(), opts)
	if err == nil {
		t.Fatal("Run with a shape mismatch: got nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "changed shape") {
		t.Errorf("error = %v, want it to explain the shape mismatch", err)
	}
}

func TestRun_LiveObjectNotFound_Errors(t *testing.T) {
	logger := log.New(io.Discard, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Base:    t.TempDir(),
		App:     config.App{ID: "psql", Object: "psql"},
		Index:   idx,
		Against: t.TempDir(),
		Log:     logger,
	}
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("Run with no live object: got nil error, want non-nil")
	}
}
