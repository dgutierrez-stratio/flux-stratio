package appdiff

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/reporequire"
	"github.com/Stratio/flux-stratio/internal/runner"
)

func fixtureBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	for _, dir := range reporequire.Dirs {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

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

const rsetOutputPgCluster = `
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps-psql
  namespace: stratio-datastores
spec:
  path: components/postgres/app/overlays/S
`

const kustomizationBuildOutputPgCluster = `
---
apiVersion: postgres.stratio.com/v1
kind: PgCluster
metadata:
  name: psql
  namespace: stratio-datastores
spec:
  instances: 1
`

func TestDiff_ManifestMode_ProducesPatch(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio",
		App: config.App{
			ID: "psql", Rset: "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-psql", Object: "psql",
		},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Log: log.New(io.Discard, false),
	}

	liveObj := map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(3)},
	}
	opts.Client = fakeClientWithUnstructured(t, liveObj)

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if result.Patch == nil {
		t.Fatal("Patch = nil, want a patch for the changed instances count")
	}
	if result.Patch.TargetKind != "PgCluster" {
		t.Errorf("TargetKind = %q, want PgCluster", result.Patch.TargetKind)
	}
}

func TestDiff_ManifestMode_NoLiveObjectErrors(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio",
		App: config.App{ID: "psql", Rset: "apps/components/resourceset-apps-datastores.yaml", Kustomization: "apps-psql", Object: "psql"},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fake.NewClientBuilder().WithScheme(mustScheme(t)).Build(),
		Log:    log.New(io.Discard, false),
	}
	if _, err := Diff(context.Background(), opts); err == nil {
		t.Fatal("Diff with no live object present: got nil error, want non-nil")
	}
}

func TestDiff_ManifestMode_LiveNamespaceFallback(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	liveObj := map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "old-namespace"},
		"spec":     map[string]any{"instances": int64(5)},
	}
	opts := Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio",
		App: config.App{
			ID: "psql", Rset: "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-psql", Object: "psql",
			Live: []config.ObjectRef{{Namespace: "old-namespace", Name: "psql"}},
		},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fakeClientWithUnstructured(t, liveObj),
		Log:    log.New(io.Discard, false),
	}
	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if result.Patch == nil {
		t.Fatal("Patch = nil, want the live-namespace fallback to have found the object")
	}
}

func TestDiff_RenderFailurePropagates(t *testing.T) {
	base := fixtureBase(t)
	opts := Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio",
		App:    config.App{ID: "psql", Rset: "x.yaml", Kustomization: "apps-psql", Object: "psql"},
		Runner: &runner.Fake{}, // "flux-operator" unconfigured -> errors
		Client: fake.NewClientBuilder().WithScheme(mustScheme(t)).Build(),
		Log:    log.New(io.Discard, false),
	}
	if _, err := Diff(context.Background(), opts); err == nil {
		t.Fatal("Diff when render fails: got nil error, want non-nil")
	}
}

// TestDiff_ManifestMode_AppliesRenameForLiveLookup mirrors chart_test.go's
// TestDiff_ChartMode_AppliesRenameForLiveLookup for manifest mode — the
// live-name/Object fallback itself is config.App.LiveName's own tested
// responsibility (see internal/config), so this exercises the real
// behavior through Diff rather than re-testing the fallback in isolation.
func TestDiff_ManifestMode_AppliesRenameForLiveLookup(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio",
		App: config.App{
			ID: "psql", Rset: "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-psql", Object: "psql",
			Live: []config.ObjectRef{{Namespace: "stratio-datastores", Name: "psql-legacy"}},
		},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Log: log.New(io.Discard, false),
	}

	// The live object exists only under the old, pre-rename name — its
	// live name must be what fetchLiveManifestObject looks it up as, not
	// Object.
	liveObj := map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql-legacy", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(3)},
	}
	opts.Client = fakeClientWithUnstructured(t, liveObj)

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if result.Patch == nil {
		t.Fatal("Patch = nil, want the renamed live object to have been found and diffed")
	}
}

// fakeClientWithUnstructured builds a fake client seeded with one
// unstructured object, using an empty scheme — matching how kubeclient
// itself treats every non-core kind (see internal/kubeclient's own tests,
// which established that the fake client handles unregistered CRD-shaped
// GVKs without any explicit RESTMapper).
func fakeClientWithUnstructured(t *testing.T, obj map[string]any) client.Client {
	t.Helper()
	u := &unstructured.Unstructured{Object: obj}
	return fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(u).Build()
}

func TestLiveManifestObject_Success(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	liveObj := map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(3)},
	}
	opts := Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio",
		App: config.App{ID: "psql", Rset: "apps/components/resourceset-apps-datastores.yaml", Kustomization: "apps-psql", Object: "psql"},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fakeClientWithUnstructured(t, liveObj),
		Log:    log.New(io.Discard, false),
	}

	got, err := LiveManifestObject(context.Background(), opts)
	if err != nil {
		t.Fatalf("LiveManifestObject returned error: %v", err)
	}
	if got.GetName() != "psql" || got.GetKind() != "PgCluster" {
		t.Errorf("got = kind=%s name=%s", got.GetKind(), got.GetName())
	}
}

func TestLiveManifestObject_RejectsChartModeApp(t *testing.T) {
	opts := Options{App: config.App{ID: "x", ChartPath: "x"}}
	if _, err := LiveManifestObject(context.Background(), opts); err == nil {
		t.Fatal("LiveManifestObject on a chart-mode app: got nil error, want non-nil")
	}
}

func TestLiveChartWorkloads_RejectsManifestModeApp(t *testing.T) {
	opts := Options{App: config.App{ID: "x"}}
	if _, err := LiveChartWorkloads(context.Background(), opts); err == nil {
		t.Fatal("LiveChartWorkloads on a manifest-mode app: got nil error, want non-nil")
	}
}

// TestDiff_ManifestMode_ReportsFluxManagedLive: a live object Flux already
// reconciles no longer holds the legacy values, so Diff says who manages it
// (and apps diff/migrate warn to use --baseline instead).
func TestDiff_ManifestMode_ReportsFluxManagedLive(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		labels map[string]any
		want   string
	}{
		{"legacy", nil, ""},
		{"flux-managed", map[string]any{"kustomize.toolkit.fluxcd.io/name": "apps-psql"}, "Kustomization apps-psql"},
	} {
		t.Run(c.name, func(t *testing.T) {
			metadata := map[string]any{"name": "psql", "namespace": "stratio-datastores"}
			if c.labels != nil {
				metadata["labels"] = c.labels
			}
			opts := Options{
				Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio",
				App: config.App{ID: "psql", Rset: "apps/components/resourceset-apps-datastores.yaml", Kustomization: "apps-psql", Object: "psql"},
				Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
					"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
					"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
				}},
				Client: fakeClientWithUnstructured(t, map[string]any{
					"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster", "metadata": metadata,
					"spec": map[string]any{"instances": int64(1)},
				}),
				Log: log.New(io.Discard, false),
			}
			result, err := Diff(context.Background(), opts)
			if err != nil {
				t.Fatalf("Diff returned error: %v", err)
			}
			if result.FluxManagedBy != c.want {
				t.Errorf("FluxManagedBy = %q, want %q", result.FluxManagedBy, c.want)
			}
		})
	}
}

func TestDroppedFromExisting(t *testing.T) {
	computed := diff.PatchDoc{TargetKind: "HelmRelease", Patch: map[string]any{
		"spec": map[string]any{"values": map[string]any{
			"app": map[string]any{"logLevel": "DEBUG"},
		}},
	}}
	existing := []string{
		"spec:\n  values:\n    app:\n      logLevel: INFO\n      replicas: 3\n    handEdited: true\n",
		"spec:\n  values:\n    other: [a, b]\n",
	}
	got, err := droppedFromExisting(computed, existing)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"spec.values.app.replicas", "spec.values.handEdited", "spec.values.other"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dropped = %v, want %v (a changed value is the update, not a loss)", got, want)
	}

	none, err := droppedFromExisting(computed, []string{"spec:\n  values:\n    app:\n      logLevel: INFO\n", "not: [valid"})
	if err != nil || len(none) != 0 {
		t.Errorf("dropped = %v (err %v), want none: same path, new value; an unparseable body is skipped", none, err)
	}
}

func TestDroppedFromExisting_JSON6902OpsByPath(t *testing.T) {
	computed := diff.PatchDoc{TargetKind: "PgCluster", Patch: []diff.JSONPatchOp{{Op: "replace", Path: "/spec/a", Value: 1}}}
	got, err := droppedFromExisting(computed, []string{"- op: replace\n  path: /spec/a\n  value: 0\n- op: add\n  path: /spec/b\n  value: 2\n"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/spec/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dropped = %v, want %v", got, want)
	}
}
