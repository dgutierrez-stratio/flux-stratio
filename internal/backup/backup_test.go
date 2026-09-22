package backup

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/reporequire"
	"github.com/Stratio/flux-stratio/internal/runner"
)

var fixedClock = func() time.Time { return time.Date(2026, 3, 4, 10, 30, 0, 0, time.UTC) }

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

func TestRun_ManifestMode_WritesCRYAML(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	backupsDir := t.TempDir()

	liveObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(3)},
	}}

	opts := Options{
		Base: base, Cluster: "eosdev", Tenant: "stratio",
		App:   config.App{ID: "psql", Rset: "apps/components/resourceset-apps-datastores.yaml", Kustomization: "apps-psql", Object: "psql"},
		Dir:   backupsDir,
		Clock: fixedClock,
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(liveObj).Build(),
		Log:    log.New(io.Discard, false),
	}

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	wantDir := filepath.Join(backupsDir, "psql", "2026-03-04T10-30-00Z")
	if result.Dir != wantDir {
		t.Errorf("Dir = %q, want %q", result.Dir, wantDir)
	}
	if len(result.Files) != 1 || result.Files[0] != "cr.yaml" {
		t.Errorf("Files = %v, want [cr.yaml]", result.Files)
	}

	data, err := os.ReadFile(filepath.Join(wantDir, "cr.yaml"))
	if err != nil {
		t.Fatalf("cr.yaml not written: %v", err)
	}
	if !strings.Contains(string(data), "instances: 3") {
		t.Errorf("cr.yaml content = %s, want it to contain the live instances count", data)
	}
}

func TestRun_ManifestMode_LiveObjectNotFoundWritesNothing(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	backupsDir := t.TempDir()

	opts := Options{
		Base: base, Cluster: "eosdev", Tenant: "stratio",
		App:   config.App{ID: "psql", Rset: "apps/components/resourceset-apps-datastores.yaml", Kustomization: "apps-psql", Object: "psql"},
		Dir:   backupsDir,
		Clock: fixedClock,
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).Build(),
		Log:    log.New(io.Discard, false),
	}

	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("Run with no live object present: got nil error, want non-nil")
	}

	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("backupsDir has %d entries, want 0 (no directory should be created on failure)", len(entries))
	}
}

func TestRun_UsesRealClockByDefault(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	backupsDir := t.TempDir()
	liveObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{},
	}}
	opts := Options{
		Base: base, Cluster: "eosdev", Tenant: "stratio",
		App: config.App{ID: "psql", Rset: "apps/components/resourceset-apps-datastores.yaml", Kustomization: "apps-psql", Object: "psql"},
		Dir: backupsDir,
		// Clock deliberately left nil.
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(liveObj).Build(),
		Log:    log.New(io.Discard, false),
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	nowYear := time.Now().UTC().Format("2006")
	if !strings.Contains(result.Dir, nowYear) {
		t.Errorf("Dir = %q, want it to contain the current year %q (default clock should be time.Now)", result.Dir, nowYear)
	}
}
