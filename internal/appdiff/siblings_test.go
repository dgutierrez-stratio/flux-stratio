package appdiff

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
)

const rsetOutputGenai = `
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps-genai
  namespace: stratio-genai
spec:
  path: components/genai/app/overlays/S
`

const kustomizationBuildOutputGenai = `
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: genai
  namespace: stratio-genai
spec:
  values: {}
`

// helmTemplateOutputGenai is the genai chart's shape: sibling workloads,
// each reading its own ConfigMap built from its own env-vars file, both
// setting VAULT_ROLE.
const helmTemplateOutputGenai = `
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: genai-api-config
data:
  VAULT_ROLE: genai_genai-api
  LOG_LEVEL: INFO
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: genai-ui-config
data:
  VAULT_ROLE: genai_genai-ui
  UI_THEME: light
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
        - name: api
          envFrom:
            - configMapRef:
                name: genai-api-config
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: genai-ui
  namespace: stratio-genai
spec:
  template:
    spec:
      containers:
        - name: ui
          envFrom:
            - configMapRef:
                name: genai-ui-config
`

func genaiDiffOptions(t *testing.T) Options {
	t.Helper()
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "genai", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	chartDir := filepath.Join(base, "charts", "genai")
	for dir, files := range map[string]map[string]string{
		chartDir: {"Chart.yaml": "apiVersion: v2\nname: genai\nversion: 0.1.0\n"},
		filepath.Join(chartDir, "config"): {
			"genai_api_env_vars.yaml": "VAULT_ROLE: {{ .Values.genaiApi.general.identity.approlename | quote }}\n" +
				"LOG_LEVEL: {{ .Values.genaiApi.general.log.level | quote }}\n",
			"genai_ui_env_vars.yaml": "VAULT_ROLE: {{ .Values.genaiUi.general.identity.approlename | quote }}\n" +
				"UI_THEME: {{ .Values.genaiUi.settings.theme | quote }}\n",
		},
		filepath.Join(chartDir, "charts"): {}, // avoid a `helm dependency build` call
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio",
		App: config.App{
			ID: "genai", Rset: "apps/components/resourceset-apps-genai.yaml",
			Kustomization: "apps-genai", Object: "genai", ChartPath: "genai",
		},
		Runner: helmSim{&runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputGenai)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputGenai)},
			"helm":          {Stdout: []byte(helmTemplateOutputGenai)},
		}}},
		Log: log.New(io.Discard, false),
	}
}

// liveGenaiWorkload is a live legacy genai sibling with a single env var.
func liveGenaiWorkload(t *testing.T, name, key, value string) *appsv1.Deployment {
	dep := deploymentWithEnv(t, name, "stratio-genai", value)
	dep.Spec.Template.Spec.Containers[0].Env[0].Name = key
	return dep
}

func patchValuesOf(t *testing.T, result *Result) map[string]any {
	t.Helper()
	if result.Patch == nil {
		return nil
	}
	body, _ := result.Patch.Patch.(map[string]any)
	spec, _ := body["spec"].(map[string]any)
	values, _ := spec["values"].(map[string]any)
	return values
}

func TestDiff_ChartMode_SiblingsPatchTheirOwnPaths(t *testing.T) {
	opts := genaiDiffOptions(t)
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		liveGenaiWorkload(t, "genai-api", "VAULT_ROLE", "stratio-genai-genai-api"),
		liveGenaiWorkload(t, "genai-ui", "VAULT_ROLE", "stratio-genai-genai-ui"),
	).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	want := map[string]any{
		"genaiApi": map[string]any{"general": map[string]any{"identity": map[string]any{"approlename": "stratio-genai-genai-api"}}},
		"genaiUi":  map[string]any{"general": map[string]any{"identity": map[string]any{"approlename": "stratio-genai-genai-ui"}}},
	}
	if got := patchValuesOf(t, result); !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
	if len(result.UnmappedDiffs) != 0 || len(result.MissingWorkloads) != 0 {
		t.Errorf("UnmappedDiffs = %+v, MissingWorkloads = %v, want none", result.UnmappedDiffs, result.MissingWorkloads)
	}
	wantAfter := "genai-api/VAULT_ROLE=stratio-genai-genai-api\ngenai-ui/VAULT_ROLE=stratio-genai-genai-ui"
	if result.After != wantAfter {
		t.Errorf("After = %q, want %q (each line under its own workload)", result.After, wantAfter)
	}
}

// TestDiff_ChartMode_OnlyAnchorLive is the migration that wrote
// genai-api's Vault role into genaiUi: genai-ui never ran live.
func TestDiff_ChartMode_OnlyAnchorLive(t *testing.T) {
	opts := genaiDiffOptions(t)
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		liveGenaiWorkload(t, "genai-api", "VAULT_ROLE", "stratio-genai-genai-api"),
	).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	want := map[string]any{
		"genaiApi": map[string]any{"general": map[string]any{"identity": map[string]any{"approlename": "stratio-genai-genai-api"}}},
	}
	if got := patchValuesOf(t, result); !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
	if want := []string{"Deployment stratio-genai/genai-ui"}; !reflect.DeepEqual(result.MissingWorkloads, want) {
		t.Errorf("MissingWorkloads = %v, want %v", result.MissingWorkloads, want)
	}
}

func TestDiff_ChartMode_BaselinePerWorkloadFiles(t *testing.T) {
	opts := genaiDiffOptions(t)
	opts.Baseline = t.TempDir()
	for name, content := range map[string]string{
		"env-vars.env":                      "VAULT_ROLE=stratio-genai-genai-ui\n",
		"env-vars.deployment.genai-api.env": "VAULT_ROLE=stratio-genai-genai-api\n",
		"env-vars.deployment.genai-ui.env":  "VAULT_ROLE=stratio-genai-genai-ui\n",
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
	want := map[string]any{
		"genaiApi": map[string]any{"general": map[string]any{"identity": map[string]any{"approlename": "stratio-genai-genai-api"}}},
		"genaiUi":  map[string]any{"general": map[string]any{"identity": map[string]any{"approlename": "stratio-genai-genai-ui"}}},
	}
	if got := patchValuesOf(t, result); !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
}

func TestDiff_ChartMode_BaselineMissingSiblingIsNamed(t *testing.T) {
	opts := genaiDiffOptions(t)
	opts.Baseline = t.TempDir()
	// A legacy CCT genai backup: only the anchor workload was captured.
	for name, content := range map[string]string{
		"env-vars.env":                      "VAULT_ROLE=stratio-genai-genai-api\n",
		"env-vars.deployment.genai-api.env": "VAULT_ROLE=stratio-genai-genai-api\n",
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
	want := map[string]any{
		"genaiApi": map[string]any{"general": map[string]any{"identity": map[string]any{"approlename": "stratio-genai-genai-api"}}},
	}
	if got := patchValuesOf(t, result); !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
	if want := []string{"Deployment stratio-genai/genai-ui"}; !reflect.DeepEqual(result.MissingWorkloads, want) {
		t.Errorf("MissingWorkloads = %v, want %v", result.MissingWorkloads, want)
	}
}

// TestDiff_ChartMode_BaselineFlatFileReportsAmbiguity covers a backup
// taken before per-workload files existed: its merged env-vars.env can't
// say which sibling VAULT_ROLE came from, so it must not be guessed.
func TestDiff_ChartMode_BaselineFlatFileReportsAmbiguity(t *testing.T) {
	opts := genaiDiffOptions(t)
	opts.Baseline = t.TempDir()
	if err := os.WriteFile(filepath.Join(opts.Baseline, "env-vars.env"), []byte("VAULT_ROLE=stratio-genai-genai-api\nLOG_LEVEL=DEBUG\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).Build()

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	want := map[string]any{
		"genaiApi": map[string]any{"general": map[string]any{"log": map[string]any{"level": "DEBUG"}}},
	}
	if got := patchValuesOf(t, result); !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v (only the unambiguous LOG_LEVEL)", got, want)
	}
	wantUnmapped := []diff.UnmappedDiff{{
		Name: "VAULT_ROLE", Rendered: "genai_genai-ui", Live: "stratio-genai-genai-api", Reason: diff.UnmappedAmbiguous,
		Candidates: []string{"genaiApi.general.identity.approlename", "genaiUi.general.identity.approlename"},
	}}
	if !reflect.DeepEqual(result.UnmappedDiffs, wantUnmapped) {
		t.Errorf("UnmappedDiffs = %+v, want %+v", result.UnmappedDiffs, wantUnmapped)
	}
}

func TestWorkloadEnvFile(t *testing.T) {
	name := WorkloadEnvFile("Deployment", "genai-ui")
	if name != "env-vars.deployment.genai-ui.env" {
		t.Errorf("WorkloadEnvFile = %q", name)
	}
	for file, want := range map[string]bool{
		name:               true,
		"env-vars.env":     false,
		"deployment.yaml":  false,
		"env-vars.foo.txt": false,
	} {
		if got := IsWorkloadEnvFile(file); got != want {
			t.Errorf("IsWorkloadEnvFile(%q) = %v, want %v", file, got, want)
		}
	}
}

// TestDiff_ChartMode_UnreadableSiblingFails: only a NotFound sibling is
// "missing". One the client can't read (Forbidden) must fail the diff —
// treated as absent, its variables would stop protecting shared .Values
// paths and the patch could change them.
func TestDiff_ChartMode_UnreadableSiblingFails(t *testing.T) {
	opts := genaiDiffOptions(t)
	opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		liveGenaiWorkload(t, "genai-api", "VAULT_ROLE", "stratio-genai-genai-api"),
	).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == "genai-ui" {
				return apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, key.Name, errors.New("rbac"))
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()

	_, err := Diff(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "genai-ui") || !apierrors.IsForbidden(err) {
		t.Errorf("err = %v, want the Forbidden read of genai-ui", err)
	}
}
