package appmigrate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/reporequire"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

// fixtureBase builds a full <base> tree: the four sibling repo checkouts,
// keos-apps/.../overlays/S for the rendered spec.path, keos-use-cases with
// the catalog fixture template, and the tenant RSIP at its real
// tenantfile.Path location, copied from the tenant.yaml fixture content.
func fixtureBase(t *testing.T, cluster, tenant string) string {
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

	catalogSrc := filepath.Join("..", "tenantfile", "testdata", "catalog", "apps", "components", "resourceset-apps-fixture.yaml")
	catalogData, err := os.ReadFile(catalogSrc)
	if err != nil {
		t.Fatal(err)
	}
	catalogDstDir := filepath.Join(base, "keos-use-cases", "apps", "components")
	if err := os.MkdirAll(catalogDstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogDstDir, "resourceset-apps-fixture.yaml"), catalogData, 0o644); err != nil {
		t.Fatal(err)
	}

	tenantSrc := filepath.Join("..", "tenantfile", "testdata", "tenant.yaml")
	tenantData, err := os.ReadFile(tenantSrc)
	if err != nil {
		t.Fatal(err)
	}
	tenantPath := tenantfile.Path(base, cluster, tenant)
	if err := os.MkdirAll(filepath.Dir(tenantPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tenantPath, tenantData, 0o644); err != nil {
		t.Fatal(err)
	}

	return base
}

func loadCatalog(t *testing.T, base string) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load(filepath.Join(base, "keos-use-cases"))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func loadTenantDoc(t *testing.T, base, cluster, tenant string) (*tenantfile.Doc, error) {
	t.Helper()
	return tenantfile.Load(tenantfile.Path(base, cluster, tenant))
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

func TestPlan_MigratedFalseWhenNoDiff(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)

	liveObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(1)}, // matches rendered -> no diff
	}}

	opts := Options{
		Base: base, Cluster: "eosdev", Tenant: "stratio", Catalog: cat,
		App: config.App{ID: "psql", Rset: "apps/components/resourceset-apps-fixture.yaml", Kustomization: "apps-psql", Object: "psql"},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(liveObj).Build(),
		Log:    log.New(io.Discard, false),
	}

	result, err := Plan(context.Background(), opts)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if result.Migrated {
		t.Error("Migrated = true, want false (no diff)")
	}
	if result.Before != result.After {
		t.Error("Before != After even though nothing changed")
	}
}

func TestPlan_NeverWritesToDisk(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	tenantPath := tenantfile.Path(base, "eosdev", "stratio")
	before, err := os.ReadFile(tenantPath)
	if err != nil {
		t.Fatal(err)
	}

	liveObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(5)},
	}}
	opts := Options{
		Base: base, Cluster: "eosdev", Tenant: "stratio", Catalog: cat,
		App: config.App{ID: "psql", Rset: "apps/components/resourceset-apps-fixture.yaml", Kustomization: "apps-psql", Object: "psql"},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(liveObj).Build(),
		Log:    log.New(io.Discard, false),
	}

	result, err := Plan(context.Background(), opts)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if !result.Migrated {
		t.Fatal("Migrated = false, want true (instances differ: 1 rendered vs 5 live)")
	}

	after, err := os.ReadFile(tenantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("Plan modified the tenant file on disk; it must never write")
	}
}

func TestApply_WritesPatchAndIsIdempotent(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	tenantPath := tenantfile.Path(base, "eosdev", "stratio")

	liveObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(5)},
	}}
	opts := Options{
		Base: base, Cluster: "eosdev", Tenant: "stratio", Catalog: cat,
		App: config.App{ID: "psql", Rset: "apps/components/resourceset-apps-fixture.yaml", Kustomization: "apps-psql", Object: "psql"},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(liveObj).Build(),
		Log:    log.New(io.Discard, false),
	}

	result, err := Apply(context.Background(), opts)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.Migrated {
		t.Fatal("Migrated = false, want true")
	}

	firstWrite, err := os.ReadFile(tenantPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(firstWrite), "instances: 5") {
		t.Errorf("tenant file does not contain the new patch:\n%s", firstWrite)
	}
	// Comments must survive the edit.
	if !strings.Contains(string(firstWrite), "# rocket is not yet migrated") {
		t.Error("comment lost after Apply")
	}

	// Re-run: must converge, not duplicate the patch or change anything else.
	result2, err := Apply(context.Background(), opts)
	if err != nil {
		t.Fatalf("second Apply returned error: %v", err)
	}
	if !result2.Migrated {
		t.Fatal("second Apply: Migrated = false, want true (still a real patch, just the same one)")
	}
	secondWrite, err := os.ReadFile(tenantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstWrite) != string(secondWrite) {
		t.Errorf("re-running Apply changed the file:\nfirst:\n%s\nsecond:\n%s", firstWrite, secondWrite)
	}
	if n := strings.Count(string(secondWrite), "patches:"); n != 1 {
		t.Errorf("patches: appears %d times, want exactly 1 (idempotent upsert, not a duplicate)", n)
	}
}
