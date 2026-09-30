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
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	return name
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
		Repos:   config.ReposUnder(t.TempDir()),
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
		Repos: config.ReposUnder(t.TempDir()),
		App: config.App{
			ID: "eureka-agent", Object: "eureka-agent", ChartPath: "eureka-agent",
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

// TestRun_ChartMode_HelmReleaseSeeded_ThreadsChartsRepo covers the path
// TestRun_ChartMode_DiffsEnvVarsOnly doesn't: a live HelmRelease (not just
// a Deployment) present, so backup.Run's captureChartFromHelmRelease —
// the one place that actually joins the charts repo with App.ChartPath
// into a chart directory — is exercised. The charts repo is checked out
// away from the other repos, so this fails loudly if drift.Run's
// Options.Repos ever stops reaching backup.Options.Repos.
func TestRun_ChartMode_HelmReleaseSeeded_ThreadsChartsRepo(t *testing.T) {
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
		Repos: chartsRepoAt(t.TempDir(), filepath.Join(chartsRoot, "charts")), // chart deliberately absent under base
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
		t.Fatalf("Run returned error (chart should resolve under repos.charts, not base): %v", err)
	}
	if result.Before != "LOG_LEVEL=INFO\n" {
		t.Errorf("Before = %q, want %q", result.Before, "LOG_LEVEL=INFO\n")
	}
	if result.After != "LOG_LEVEL=DEBUG\n" {
		t.Errorf("After = %q, want %q", result.After, "LOG_LEVEL=DEBUG\n")
	}
}

// TestRun_ChartMode_CCTBackupVsMigratedSiblings is drift after a genai
// migration: the backup was taken from the legacy CCT install (anchor and
// sibling captured per workload), live is now the HelmRelease's render.
// The app is shaped as components.Resolve resolves a migrated genai named
// by one of its workloads (`apps diff genai-ui --drift`): object "genai",
// primary live object genai-ui — no HelmRelease is named like it, so the
// live capture must find it through the workload's own Helm labels to
// capture every workload the chart renders. Only genai-ui's VAULT_ROLE
// changed; genai-api sets the same name and wins both sides' merged
// env-vars.env, so only a per-workload comparison can show it.
func TestRun_ChartMode_CCTBackupVsMigratedSiblings(t *testing.T) {
	logger := log.New(io.Discard, false)
	chartsRoot := t.TempDir()
	chartPath := fixtureChart(t, chartsRoot, "genai")

	hr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "genai", "namespace": "stratio-genai"},
		"spec":     map[string]any{"values": map[string]any{}},
	}}
	workload := func(name, role string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "stratio-genai", Labels: map[string]string{
				"helm.toolkit.fluxcd.io/name": "genai", "helm.toolkit.fluxcd.io/namespace": "stratio-genai",
			}},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: "main", Env: []corev1.EnvVar{{Name: "VAULT_ROLE", Value: role}},
					}}},
				},
			},
		}
	}
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		hr, workload("genai-api", "api-role"), workload("genai-ui", "changed-ui-role"),
	).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	backupDir := t.TempDir()
	writeBackupFile(t, backupDir, "deployment.yaml", "kind: Deployment\n")
	writeBackupFile(t, backupDir, "env-vars.env", "VAULT_ROLE=api-role\n")
	writeBackupFile(t, backupDir, "env-vars.deployment.genai-api.env", "VAULT_ROLE=api-role\n")
	writeBackupFile(t, backupDir, "env-vars.deployment.genai-ui.env", "VAULT_ROLE=ui-role\n")

	result, err := Run(context.Background(), Options{
		Repos: chartsRepoAt(t.TempDir(), filepath.Join(chartsRoot, "charts")),
		App: config.App{
			ID: "genai", Object: "genai", ChartPath: chartPath,
			Live: []config.ObjectRef{
				{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Namespace: "stratio-genai", Name: "genai-ui"},
				{GVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Namespace: "stratio-genai", Name: "genai-api"},
			},
		},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			// genai-ui renders first, so genai-api's VAULT_ROLE wins the merge.
			"helm": {Stdout: []byte(`
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: genai-ui
  namespace: stratio-genai
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: genai-api
  namespace: stratio-genai
`)},
		}},
		Client: c, Index: idx, Against: backupDir, Log: logger,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	wantBefore := "# env-vars.deployment.genai-api.env\nVAULT_ROLE=api-role\n# env-vars.deployment.genai-ui.env\nVAULT_ROLE=ui-role\n"
	wantAfter := "# env-vars.deployment.genai-api.env\nVAULT_ROLE=api-role\n# env-vars.deployment.genai-ui.env\nVAULT_ROLE=changed-ui-role\n"
	if result.Before != wantBefore || result.After != wantAfter {
		t.Errorf("Before = %q\nAfter = %q\nwant %q\n     %q", result.Before, result.After, wantBefore, wantAfter)
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
		Repos:   config.ReposUnder(t.TempDir()),
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
		Repos:   config.ReposUnder(t.TempDir()),
		App:     config.App{ID: "psql", Object: "psql"},
		Index:   idx,
		Against: t.TempDir(),
		Log:     logger,
	}
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("Run with no live object: got nil error, want non-nil")
	}
}

// A live HelmRelease whose workloads weren't found degrades to
// helmrelease.yaml/values.yaml; against a workload backup that's not a
// shape change, and the error says what to check instead.
func TestCompare_DegradedHelmReleaseVsEnvBackup_PointsAtChart(t *testing.T) {
	backupDir := t.TempDir()
	writeBackupFile(t, backupDir, "deployment.yaml", "kind: Deployment\n")
	writeBackupFile(t, backupDir, "env-vars.env", "A=1\n")

	_, err := compare(backupDir, t.TempDir(), []string{"helmrelease.yaml", "values.yaml"})
	if err == nil {
		t.Fatal("compare: got nil error, want non-nil")
	}
	if strings.Contains(err.Error(), "changed shape") || !strings.Contains(err.Error(), "repos.charts") ||
		!strings.Contains(err.Error(), "env-vars.env") {
		t.Errorf("error = %v, want it to point at repos.charts and name the backup's files", err)
	}
}

// TestCompare_PerWorkloadEnvFiles_ShowSiblingDrift covers a change the
// merged env-vars.env hides: genai-ui's VAULT_ROLE changed, but genai-api
// sets the same name and wins the merge, so the merged files are equal.
func TestCompare_PerWorkloadEnvFiles_ShowSiblingDrift(t *testing.T) {
	backupDir, liveDir := t.TempDir(), t.TempDir()
	writeBackupFile(t, backupDir, "env-vars.env", "VAULT_ROLE=api-role\n")
	writeBackupFile(t, backupDir, "env-vars.deployment.genai-api.env", "VAULT_ROLE=api-role\n")
	writeBackupFile(t, backupDir, "env-vars.deployment.genai-ui.env", "VAULT_ROLE=ui-role\n")
	writeBackupFile(t, liveDir, "env-vars.env", "VAULT_ROLE=api-role\n")
	writeBackupFile(t, liveDir, "env-vars.deployment.genai-api.env", "VAULT_ROLE=api-role\n")
	writeBackupFile(t, liveDir, "env-vars.deployment.genai-ui.env", "VAULT_ROLE=changed-ui-role\n")

	result, err := compare(backupDir, liveDir, []string{
		"deployment.yaml", "env-vars.env", "env-vars.deployment.genai-api.env", "env-vars.deployment.genai-ui.env",
	})
	if err != nil {
		t.Fatalf("compare returned error: %v", err)
	}
	wantBefore := "# env-vars.deployment.genai-api.env\nVAULT_ROLE=api-role\n# env-vars.deployment.genai-ui.env\nVAULT_ROLE=ui-role\n"
	wantAfter := "# env-vars.deployment.genai-api.env\nVAULT_ROLE=api-role\n# env-vars.deployment.genai-ui.env\nVAULT_ROLE=changed-ui-role\n"
	if result.Before != wantBefore || result.After != wantAfter {
		t.Errorf("Before = %q, After = %q, want %q / %q", result.Before, result.After, wantBefore, wantAfter)
	}
}

// chartsRepoAt is the default layout under base, but with the charts
// repository checked out at charts instead.
func chartsRepoAt(base, charts string) config.RepoPaths {
	repos := config.ReposUnder(base)
	repos.Charts = charts
	return repos
}
