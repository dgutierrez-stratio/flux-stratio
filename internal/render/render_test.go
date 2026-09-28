package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/reporequire"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

const rsetOutput = `
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps-psql
  namespace: stratio-datastores
spec:
  path: components/postgres/app/overlays/S
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps-pool-psql
  namespace: stratio-datastores
spec:
  path: components/pgbouncer/app/overlays/S
`

const rsetOutputWithSubstituteFrom = `
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps-psql
  namespace: stratio-datastores
spec:
  path: components/postgres/app/overlays/S
  postBuild:
    substituteFrom:
      - kind: ConfigMap
        name: keos-runtime-info
    substitute:
      EXISTING_KEY: keep-me
`

const kustomizationBuildOutput = `
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: psql
  namespace: stratio-datastores
spec:
  values:
    foo: bar
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: unrelated-configmap
  namespace: stratio-datastores
data:
  x: "1"
`

func fixtureBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	for _, dir := range reporequire.Dirs {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	return base
}

func baseOptions(t *testing.T) Options {
	return Options{
		Base:          fixtureBase(t),
		Cluster:       "eosdev",
		Tenant:        "stratio",
		Rset:          "apps/components/resourceset-apps-datastores.yaml",
		Kustomization: "apps-psql",
		Object:        "psql",
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutput)},
			"flux":          {Stdout: []byte(kustomizationBuildOutput)},
		}},
		Client: fake.NewClientBuilder().WithScheme(mustCoreScheme(t)).Build(),
		Log:    log.New(os.Stderr, false),
	}
}

func mustCoreScheme(t *testing.T) *apiruntime.Scheme {
	t.Helper()
	s := apiruntime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRender_Success(t *testing.T) {
	opts := baseOptions(t)
	result, err := Render(context.Background(), opts)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if result.Object.GetKind() != "HelmRelease" || result.Object.GetName() != "psql" {
		t.Errorf("Object = kind=%s name=%s, want HelmRelease/psql", result.Object.GetKind(), result.Object.GetName())
	}
	if len(result.AllDocs) != 2 {
		t.Errorf("len(AllDocs) = %d, want 2", len(result.AllDocs))
	}
	if result.Kustomization.GetName() != "apps-psql" {
		t.Errorf("Kustomization.Name = %q, want %q", result.Kustomization.GetName(), "apps-psql")
	}
}

func TestRender_MissingBaseLayout(t *testing.T) {
	opts := baseOptions(t)
	opts.Base = t.TempDir() // no sibling repo dirs created
	if _, err := Render(context.Background(), opts); err == nil {
		t.Fatal("Render with an incomplete base layout: got nil error, want non-nil")
	}
}

func TestRender_KustomizationNotFound(t *testing.T) {
	opts := baseOptions(t)
	opts.Kustomization = "apps-does-not-exist"
	_, err := Render(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "apps-does-not-exist") {
		t.Errorf("Render error = %v, want it to name the missing kustomization", err)
	}
	if err != nil && !strings.Contains(err.Error(), "apps-psql") {
		t.Errorf("Render error = %v, want it to list the available kustomizations", err)
	}
	// The most common real-world cause is the app's component being
	// missing/commented out in the tenant file, not a genuine rendering
	// failure — the error should point a less experienced operator there
	// instead of leaving them to puzzle out what "not found among the
	// rendered" implies.
	if err != nil && !strings.Contains(err.Error(), tenantFilePath(opts)) {
		t.Errorf("Render error = %v, want it to name the tenant file to check", err)
	}
	if err != nil && !strings.Contains(err.Error(), "commented out") {
		t.Errorf("Render error = %v, want it to explain the likely cause", err)
	}
}

func TestRender_ObjectNotFound(t *testing.T) {
	opts := baseOptions(t)
	opts.Object = "does-not-exist"
	_, err := Render(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("Render error = %v, want it to name the missing object", err)
	}
}

func TestRender_FluxOperatorBuildFails(t *testing.T) {
	opts := baseOptions(t)
	opts.Runner = &runner.Fake{Responses: map[string]runner.FakeResponse{
		"flux-operator": {Stderr: []byte("boom"), Err: context.DeadlineExceeded},
	}}
	if _, err := Render(context.Background(), opts); err == nil {
		t.Fatal("Render when flux-operator build rset fails: got nil error, want non-nil")
	}
}

func TestRender_FluxBuildFails(t *testing.T) {
	opts := baseOptions(t)
	opts.Runner = &runner.Fake{Responses: map[string]runner.FakeResponse{
		"flux-operator": {Stdout: []byte(rsetOutput)},
		"flux":          {Stderr: []byte("boom"), Err: context.DeadlineExceeded},
	}}
	if _, err := Render(context.Background(), opts); err == nil {
		t.Fatal("Render when flux build kustomization fails: got nil error, want non-nil")
	}
}

func TestRender_SpecPathMissingUnderKeosApps(t *testing.T) {
	opts := baseOptions(t)
	opts.Runner = &runner.Fake{Responses: map[string]runner.FakeResponse{
		"flux-operator": {Stdout: []byte(`
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps-psql
  namespace: stratio-datastores
spec:
  path: components/does-not-exist/app/overlays/S
`)},
	}}
	_, err := Render(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("Render error = %v, want it to name the missing spec.path", err)
	}
}

func TestRender_SubstituteFromResolvedAndRemoved(t *testing.T) {
	opts := baseOptions(t)
	opts.Runner = &runner.Fake{Responses: map[string]runner.FakeResponse{
		"flux-operator": {Stdout: []byte(rsetOutputWithSubstituteFrom)},
		"flux":          {Stdout: []byte(kustomizationBuildOutput)},
	}}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "keos-runtime-info", Namespace: "stratio-datastores"},
		Data:       map[string]string{"CLUSTER_DOMAIN": "eosdev.int", "EXISTING_KEY": "should-not-overwrite"},
	}
	opts.Client = fake.NewClientBuilder().WithScheme(mustCoreScheme(t)).WithObjects(cm).Build()

	result, err := Render(context.Background(), opts)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	substitute, found, err := unstructured.NestedStringMap(result.Kustomization.Object, "spec", "postBuild", "substitute")
	if err != nil || !found {
		t.Fatalf("spec.postBuild.substitute missing or errored: found=%v err=%v", found, err)
	}
	if substitute["CLUSTER_DOMAIN"] != "eosdev.int" {
		t.Errorf("substitute[CLUSTER_DOMAIN] = %q, want %q", substitute["CLUSTER_DOMAIN"], "eosdev.int")
	}
	if substitute["EXISTING_KEY"] != "keep-me" {
		t.Errorf("substitute[EXISTING_KEY] = %q, want the pre-existing value preserved (%q)", substitute["EXISTING_KEY"], "keep-me")
	}
	if _, found, _ := unstructured.NestedSlice(result.Kustomization.Object, "spec", "postBuild", "substituteFrom"); found {
		t.Error("spec.postBuild.substituteFrom should have been removed after resolution")
	}
}

func TestWriteTempKustomization_CreatesAndCallerRemoves(t *testing.T) {
	docs, err := yamldocs.Decode([]byte(rsetOutput))
	if err != nil {
		t.Fatal(err)
	}
	path, err := writeTempKustomization(docs[0])
	if err != nil {
		t.Fatalf("writeTempKustomization returned error: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("temp file %s not created: %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "apps-psql") {
		t.Errorf("temp file content missing kustomization name: %s", data)
	}
}

const rsetOutputWithPatches = `
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps-psql
  namespace: stratio-datastores
spec:
  path: components/postgres/app/overlays/S
  patches:
    - patch: |
        kind: HelmRelease
        metadata:
          name: psql
        spec:
          values:
            foo: patched
      target:
        kind: HelmRelease
    - patch: |
        kind: ConfigMap
        metadata:
          name: unrelated-configmap
      target:
        kind: ConfigMap
`

// TestRender_StripsTheObjectKindsOwnPatches: the base is rendered without
// the tenant file's existing patch for the selected object's kind (the one
// tenantfile.Splice would replace), keeping every other kind's patch.
func TestRender_StripsTheObjectKindsOwnPatches(t *testing.T) {
	opts := baseOptions(t)
	fakeRunner := &runner.Fake{Responses: map[string]runner.FakeResponse{
		"flux-operator": {Stdout: []byte(rsetOutputWithPatches)},
		"flux":          {Stdout: []byte(kustomizationBuildOutput)},
	}}
	opts.Runner = fakeRunner

	result, err := Render(context.Background(), opts)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if len(result.ReplacedPatches) != 1 || !strings.Contains(result.ReplacedPatches[0], "foo: patched") {
		t.Fatalf("ReplacedPatches = %q, want the one HelmRelease patch", result.ReplacedPatches)
	}
	patches, _, _ := unstructured.NestedSlice(result.Kustomization.Object, "spec", "patches")
	if len(patches) != 1 {
		t.Fatalf("rendered Kustomization kept %d patches, want only the ConfigMap one", len(patches))
	}
	if kind, _, _ := unstructured.NestedString(patches[0].(map[string]any), "target", "kind"); kind != "ConfigMap" {
		t.Errorf("kept patch targets %q, want ConfigMap", kind)
	}
	var builds int
	for _, c := range fakeRunner.Calls {
		if c.Name == "flux" {
			builds++
		}
	}
	if builds != 2 {
		t.Errorf("flux build ran %d times, want 2 (once to learn the object's kind, once without its patch)", builds)
	}
}

func TestRender_NoOwnPatchesRendersOnce(t *testing.T) {
	opts := baseOptions(t)
	fakeRunner := opts.Runner.(*runner.Fake)
	result, err := Render(context.Background(), opts)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if len(result.ReplacedPatches) != 0 {
		t.Errorf("ReplacedPatches = %q, want none", result.ReplacedPatches)
	}
	var builds int
	for _, c := range fakeRunner.Calls {
		if c.Name == "flux" {
			builds++
		}
	}
	if builds != 1 {
		t.Errorf("flux build ran %d times, want 1", builds)
	}
}
