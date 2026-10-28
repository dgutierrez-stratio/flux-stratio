package drift

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
	"github.com/Stratio/flux-stratio/internal/ui"
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
	writeBackupFile(t, liveDir, "deployment.yaml", "kind: Deployment\n")
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
	// The same comparison, one workload at a time: what meld opens as tabs.
	wantItems := []ui.Comparison{
		{Env: true, Name: "env genai-api", Left: "VAULT_ROLE=api-role\n", Right: "VAULT_ROLE=api-role\n"},
		{Env: true, Name: "env genai-ui", Left: "VAULT_ROLE=ui-role\n", Right: "VAULT_ROLE=changed-ui-role\n"},
	}
	if !reflect.DeepEqual(result.Items, wantItems) {
		t.Errorf("Items = %+v, want %+v", result.Items, wantItems)
	}
}

// TestCompare_PerWorkloadItems_AWorkloadOnOneSideIsAnEmptySide: a sibling
// the backup has and live lost (or the reverse) is still its own tab, the
// missing side empty.
func TestCompare_PerWorkloadItems_AWorkloadOnOneSideIsAnEmptySide(t *testing.T) {
	backupDir, liveDir := t.TempDir(), t.TempDir()
	writeBackupFile(t, backupDir, "env-vars.env", "A=1\n")
	writeBackupFile(t, backupDir, "env-vars.deployment.web.env", "A=1\n")
	writeBackupFile(t, backupDir, "env-vars.deployment.old.env", "B=1\n")
	writeBackupFile(t, liveDir, "env-vars.env", "A=1\n")
	writeBackupFile(t, liveDir, "env-vars.deployment.web.env", "A=1\n")
	writeBackupFile(t, liveDir, "env-vars.deployment.new.env", "C=1\n")

	result, err := compare(backupDir, liveDir, []string{"env-vars.env", "env-vars.deployment.web.env", "env-vars.deployment.new.env"})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	want := []ui.Comparison{
		{Env: true, Name: "env new", Left: "", Right: "C=1\n"},
		{Env: true, Name: "env old", Left: "B=1\n", Right: ""},
		{Env: true, Name: "env web", Left: "A=1\n", Right: "A=1\n"},
	}
	if !reflect.DeepEqual(result.Items, want) {
		t.Errorf("Items = %+v, want %+v", result.Items, want)
	}
}

// TestCompare_MergedEnvIsOneItem: a backup with no per-workload env files
// compares the merged env-vars.env, as one resource next to the specs.
func TestCompare_MergedEnvIsOneItem(t *testing.T) {
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\nspec:\n  replicas: %d\n"
	backupDir, liveDir := t.TempDir(), t.TempDir()
	writeBackupFile(t, backupDir, "env-vars.env", "A=1\n")
	writeBackupFile(t, backupDir, "deployment.yaml", fmt.Sprintf(manifest, 1))
	writeBackupFile(t, liveDir, "env-vars.env", "A=2\n")
	writeBackupFile(t, liveDir, "deployment.yaml", fmt.Sprintf(manifest, 2))

	result, err := compare(backupDir, liveDir, []string{"env-vars.env", "deployment.yaml"})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	want := []ui.Comparison{
		{Env: true, Name: "env vars", Left: "A=1\n", Right: "A=2\n"},
		{Name: "spec Deployment/web", Left: "replicas: 1\n", Right: "replicas: 2\n"},
	}
	if !reflect.DeepEqual(result.Items, want) {
		t.Errorf("Items = %+v, want %+v", result.Items, want)
	}
}

// TestCompare_ManifestModeHasNoItems: a CR is one resource; there is
// nothing to split into tabs.
func TestCompare_ManifestModeHasNoItems(t *testing.T) {
	backupDir, liveDir := t.TempDir(), t.TempDir()
	writeBackupFile(t, backupDir, "cr.yaml", "kind: Thing\nspec:\n  a: 1\n")
	writeBackupFile(t, liveDir, "cr.yaml", "kind: Thing\nspec:\n  a: 2\n")
	result, err := compare(backupDir, liveDir, []string{"cr.yaml"})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if len(result.Items) != 0 {
		t.Errorf("Items = %+v, want none", result.Items)
	}
}

// TestCompare_WorkloadManifests_ShowSpecDriftByIdentity: an image or
// replica change shows up in the diff, siblings are matched by what their
// manifest names (not by which file the capture put them in), and a
// workload only one side holds is not reported as drift.
func TestCompare_WorkloadManifests_ShowSpecDriftByIdentity(t *testing.T) {
	manifest := func(name, image string, replicas int) string {
		return fmt.Sprintf("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: %s\nspec:\n  replicas: %d\n  image: %s\n", name, replicas, image)
	}
	backupDir, liveDir := t.TempDir(), t.TempDir()
	// The backup's anchor is genai-api; the live capture found genai-ui first.
	writeBackupFile(t, backupDir, "deployment.yaml", manifest("genai-api", "api:1", 1))
	writeBackupFile(t, backupDir, "workload.deployment.genai-ui.yaml", manifest("genai-ui", "ui:1", 1))
	writeBackupFile(t, backupDir, "env-vars.env", "A=1\n")
	writeBackupFile(t, liveDir, "deployment.yaml", manifest("genai-ui", "ui:2", 1))
	writeBackupFile(t, liveDir, "workload.deployment.genai-api.yaml", manifest("genai-api", "api:1", 1))
	writeBackupFile(t, liveDir, "workload.deployment.genai-proxy.yaml", manifest("genai-proxy", "proxy:1", 1))
	writeBackupFile(t, liveDir, "env-vars.env", "A=1\n")

	result, err := compare(backupDir, liveDir, []string{
		"deployment.yaml", "workload.deployment.genai-api.yaml", "workload.deployment.genai-proxy.yaml", "env-vars.env",
	})
	if err != nil {
		t.Fatalf("compare returned error: %v", err)
	}
	if !strings.Contains(result.Before, "# Deployment/genai-ui (spec)\nimage: ui:1") ||
		!strings.Contains(result.After, "# Deployment/genai-ui (spec)\nimage: ui:2") {
		t.Errorf("genai-ui's image change missing:\nBefore=%q\nAfter=%q", result.Before, result.After)
	}
	if strings.Contains(result.Before, "genai-proxy") || strings.Contains(result.After, "genai-proxy") {
		t.Errorf("genai-proxy exists only live and must not be compared: %q", result.After)
	}
	for _, side := range []string{result.Before, result.After} {
		if !strings.Contains(side, "# Deployment/genai-api (spec)\nimage: api:1") {
			t.Errorf("genai-api should be matched across deployment.yaml and its workload file: %q", side)
		}
	}
	// One spec tab per workload both sides hold, matched by identity.
	var specs []string
	for _, it := range result.Items {
		if strings.HasPrefix(it.Name, "spec ") {
			specs = append(specs, it.Name)
		}
	}
	if !reflect.DeepEqual(specs, []string{"spec Deployment/genai-api", "spec Deployment/genai-ui"}) {
		t.Errorf("spec items = %v, want genai-api and genai-ui only", specs)
	}
}

// chartsRepoAt is the default layout under base, but with the charts
// repository checked out at charts instead.
func chartsRepoAt(base, charts string) config.RepoPaths {
	repos := config.ReposUnder(base)
	repos.Charts = charts
	return repos
}

// meldRecorder is the meld the drift view would open: it records which
// resources got a tab (the file each --diff names) and with what.
type meldRecorder struct {
	tabs  map[string][2]string
	order []string // the tabs, in the order meld gets them
}

func (m *meldRecorder) Run(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
	m.tabs = map[string][2]string{}
	for i := 0; i+2 < len(args); i += 3 {
		l, err := os.ReadFile(args[i+1])
		if err != nil {
			return nil, nil, err
		}
		r, err := os.ReadFile(args[i+2])
		if err != nil {
			return nil, nil, err
		}
		// Each file is named after its side and its resource; the tab is the
		// resource, and the side must be right on both.
		lb, rb := filepath.Base(args[i+1]), filepath.Base(args[i+2])
		name, okL := strings.CutPrefix(strings.TrimSuffix(lb, ".yaml"), ui.LabelBackup+"-")
		rname, okR := strings.CutPrefix(strings.TrimSuffix(rb, ".yaml"), ui.LabelLiveNow+"-")
		if !okL || !okR || name != rname {
			return nil, nil, fmt.Errorf("files %q and %q aren't named after their sides and the same resource", lb, rb)
		}
		m.tabs[name] = [2]string{string(l), string(r)}
		m.order = append(m.order, name)
	}
	return nil, nil, nil
}

// TestCompare_EveryWorkloadGetsATabInMeld is virtualizer after its
// migration: a server, a monitor and a ui, a change in the monitor's env
// only, and a spec change in all three. Drift must put the env and the spec
// of every workload in the window — the server's and the ui's env,
// identical, included and marked as such — not just what differs: a tab
// missing from the window can't be told from a comparison that was lost.
func TestCompare_EveryWorkloadGetsATabInMeld(t *testing.T) {
	manifest := func(name string, replicas int) string {
		return fmt.Sprintf("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: %s\nspec:\n  replicas: %d\n", name, replicas)
	}
	backupDir, liveDir := t.TempDir(), t.TempDir()
	workloads := []string{"virtualizer", "virtualizer-monitor", "virtualizer-ui"}
	var liveFiles []string
	for _, w := range workloads {
		envFile := "env-vars.deployment." + w + ".env"
		writeBackupFile(t, backupDir, envFile, "A=1\n")
		liveEnv := "A=1\n"
		if w == "virtualizer-monitor" {
			liveEnv = "A=2\n"
		}
		writeBackupFile(t, liveDir, envFile, liveEnv)
		manifestFile := "workload.deployment." + w + ".yaml"
		writeBackupFile(t, backupDir, manifestFile, manifest(w, 1))
		writeBackupFile(t, liveDir, manifestFile, manifest(w, 2))
		liveFiles = append(liveFiles, envFile, manifestFile)
	}
	writeBackupFile(t, backupDir, "env-vars.env", "A=1\n")
	writeBackupFile(t, liveDir, "env-vars.env", "A=1\n")
	liveFiles = append(liveFiles, "env-vars.env")

	result, err := compare(backupDir, liveDir, liveFiles)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	rec := &meldRecorder{}
	if err := ui.OpenMeld(context.Background(), rec, ui.LabelBackup, result.Before, ui.LabelLiveNow, result.After, result.Items); err != nil {
		t.Fatalf("OpenMeld: %v", err)
	}
	want := []string{
		"env virtualizer (identical)", "env virtualizer-monitor", "env virtualizer-ui (identical)",
		"spec Deployment-virtualizer", "spec Deployment-virtualizer-monitor", "spec Deployment-virtualizer-ui",
	}
	if len(rec.tabs) != len(want) {
		var got []string
		for n := range rec.tabs {
			got = append(got, n)
		}
		t.Fatalf("tabs = %v, want one for every env and spec of all three workloads: %v", got, want)
	}
	for _, name := range want {
		if _, ok := rec.tabs[name]; !ok {
			t.Errorf("no tab %q", name)
		}
	}
	// Environment variables first, then the specs; in each group what
	// differs before what is identical.
	wantOrder := []string{
		"env virtualizer-monitor", "env virtualizer (identical)", "env virtualizer-ui (identical)",
		"spec Deployment-virtualizer", "spec Deployment-virtualizer-monitor", "spec Deployment-virtualizer-ui",
	}
	if !reflect.DeepEqual(rec.order, wantOrder) {
		t.Errorf("tab order = %v, want %v", rec.order, wantOrder)
	}
	if got := rec.tabs["env virtualizer-monitor"]; got != [2]string{"A=1\n", "A=2\n"} {
		t.Errorf("monitor env tab = %q", got)
	}
	if got := rec.tabs["env virtualizer-ui (identical)"]; got != [2]string{"A=1\n", "A=1\n"} {
		t.Errorf("ui env tab = %q, want both sides, identical", got)
	}
}
