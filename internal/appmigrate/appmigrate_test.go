package appmigrate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
	tenantPath := tenantfile.Path(filepath.Join(base, "keos-fleet"), cluster, tenant)
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
	return tenantfile.Load(tenantfile.Path(filepath.Join(base, "keos-fleet"), cluster, tenant))
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
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio", Catalog: cat,
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
	tenantPath := tenantfile.Path(filepath.Join(base, "keos-fleet"), "eosdev", "stratio")
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
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio", Catalog: cat,
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
	tenantPath := tenantfile.Path(filepath.Join(base, "keos-fleet"), "eosdev", "stratio")

	liveObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(5)},
	}}
	opts := Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio", Catalog: cat,
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

// rsetOutputPgClusterWithPatch is rsetOutputPgCluster as it renders once
// the tenant file carries patchBody for the PgCluster — what the fake
// flux-operator returns to simulate a re-run after an earlier migrate.
func rsetOutputPgClusterWithPatch(patchBody string) string {
	var indented strings.Builder
	for _, line := range strings.Split(strings.TrimRight(patchBody, "\n"), "\n") {
		indented.WriteString("        " + line + "\n")
	}
	return rsetOutputPgCluster + "  patches:\n    - patch: |\n" + indented.String() + "      target:\n        kind: PgCluster\n"
}

// tenantPgClusterPatch reads back the PgCluster patch body Apply spliced
// into the fixture tenant file's psql entry.
func tenantPgClusterPatch(t *testing.T, tenantPath string) string {
	t.Helper()
	data, err := os.ReadFile(tenantPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Spec struct {
			DefaultValues struct {
				Components map[string][]struct {
					Name    string `yaml:"name"`
					Patches []struct {
						Patch string `yaml:"patch"`
					} `yaml:"patches"`
				} `yaml:"components"`
			} `yaml:"defaultValues"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, e := range doc.Spec.DefaultValues.Components["postgres"] {
		if e.Name == "psql" && len(e.Patches) == 1 {
			return e.Patches[0].Patch
		}
	}
	t.Fatalf("no single psql patch in the tenant file:\n%s", data)
	return ""
}

// TestApply_RerunKeepsWhatTheExistingPatchCarried covers re-running apps
// migrate once the tenant file already carries a partial patch: the
// replacement written must be the whole patch (both fields), not just the
// leftover delta — which would silently drop what the first patch carried.
// That the base is rendered without the existing patch is
// internal/render's TestRender_StripsTheObjectKindsOwnPatches (the fake
// flux build here doesn't apply patches at all).
// TestPlan_SaveWritesWithoutReadingLiveAgain is apps migrate's order for
// a destructive prepare step (prepare-dlc): plan while the legacy object
// is still live, delete it, then save the planned patch.
func TestPlan_SaveWritesWithoutReadingLiveAgain(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	tenantPath := tenantfile.Path(filepath.Join(base, "keos-fleet"), "eosdev", "stratio")

	liveObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(5)},
	}}
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(liveObj).Build()
	opts := Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio", Catalog: cat,
		App: config.App{ID: "psql", Rset: "apps/components/resourceset-apps-fixture.yaml", Kustomization: "apps-psql", Object: "psql"},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: c,
		Log:    log.New(io.Discard, false),
	}

	original, err := os.ReadFile(tenantPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Plan(context.Background(), opts)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if untouched, err := os.ReadFile(tenantPath); err != nil || string(untouched) != string(original) {
		t.Fatalf("Plan wrote the tenant file (err %v)", err)
	}
	if err := c.Delete(context.Background(), liveObj); err != nil {
		t.Fatal(err)
	}

	if err := result.Save(); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	written, err := os.ReadFile(tenantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != result.After || !strings.Contains(string(written), "instances: 5") {
		t.Errorf("tenant file after Save:\n%s\nwant the planned After:\n%s", written, result.After)
	}
}

func TestApply_RerunKeepsWhatTheExistingPatchCarried(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	tenantPath := tenantfile.Path(filepath.Join(base, "keos-fleet"), "eosdev", "stratio")

	// The tenant already patches the image; live also differs in instances.
	existing := "apiVersion: postgres.stratio.com/v1\nkind: PgCluster\nmetadata:\n  name: psql\nspec:\n  image: legacy:1.0\n"
	liveObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(5), "image": "legacy:1.0"},
	}}
	opts := Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio", Catalog: cat,
		App: config.App{ID: "psql", Rset: "apps/components/resourceset-apps-fixture.yaml", Kustomization: "apps-psql", Object: "psql"},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgClusterWithPatch(existing))},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(liveObj).Build(),
		Log:    log.New(io.Discard, false),
	}

	if _, err := Apply(context.Background(), opts); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	written := tenantPgClusterPatch(t, tenantPath)
	for _, want := range []string{"instances: 5", "image: legacy:1.0"} {
		if !strings.Contains(written, want) {
			t.Errorf("rewritten patch lost %q:\n%s", want, written)
		}
	}
}

// TestPlan_ExistingPatchAlreadyExactIsUpToDate: once the tenant file
// carries exactly the patch live needs, a re-run reports nothing to do.
func TestPlan_ExistingPatchAlreadyExactIsUpToDate(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	tenantPath := tenantfile.Path(filepath.Join(base, "keos-fleet"), "eosdev", "stratio")

	liveObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(5)},
	}}
	fakeRunner := &runner.Fake{Responses: map[string]runner.FakeResponse{
		"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
		"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
	}}
	opts := Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio", Catalog: cat,
		App:    config.App{ID: "psql", Rset: "apps/components/resourceset-apps-fixture.yaml", Kustomization: "apps-psql", Object: "psql"},
		Runner: fakeRunner,
		Client: fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(liveObj).Build(),
		Log:    log.New(io.Discard, false),
	}
	if _, err := Apply(context.Background(), opts); err != nil {
		t.Fatalf("first Apply returned error: %v", err)
	}

	// Second run: the rendered Kustomization now carries what was written.
	fakeRunner.Responses["flux-operator"] = runner.FakeResponse{Stdout: []byte(rsetOutputPgClusterWithPatch(tenantPgClusterPatch(t, tenantPath)))}
	result, err := Plan(context.Background(), opts)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if result.Migrated || !result.UpToDate {
		t.Errorf("Migrated=%v UpToDate=%v, want false/true (the tenant file already carries exactly this patch)", result.Migrated, result.UpToDate)
	}
}

// TestPlan_BaselineUsedInsteadOfLive: --baseline computes the patch from a
// backup, for a component Flux already reconciled unpatched (so live
// matches the base and would yield no patch at all).
func TestPlan_BaselineUsedInsteadOfLive(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)

	// Live was already overwritten by Flux: identical to the base.
	liveObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(1)},
	}}
	backupDir := t.TempDir()
	backupCR := "apiVersion: postgres.stratio.com/v1\nkind: PgCluster\nmetadata:\n  name: psql\n  namespace: stratio-datastores\nspec:\n  instances: 3\n"
	if err := os.WriteFile(filepath.Join(backupDir, "cr.yaml"), []byte(backupCR), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio", Catalog: cat, Baseline: backupDir,
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
	if !result.Migrated || !strings.Contains(result.After, "instances: 3") {
		t.Errorf("Migrated=%v, want a patch carrying the backup's instances: 3; after:\n%s", result.Migrated, result.After)
	}
}

const rsetOutputGosecAgent = `
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: apps-psql-gosec-agent
  namespace: stratio-datastores
spec:
  path: components/gosec-agent/app/overlays/postgres/S
`

const kustomizationBuildOutputGosecAgent = `
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: psql-gosec-agent
  namespace: stratio-datastores
spec:
  replicas: 1
`

// TestPlan_ReportsALegacyAgentPatchAndWritesNothing: a tenant file the
// legacy client left a gosec agent's patch in the parent entry's top-level
// patches is flagged by Plan whether or not the agent has a difference, and
// Plan never touches the file.
func TestPlan_ReportsALegacyAgentPatchAndWritesNothing(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	tenantPath := tenantfile.Path(filepath.Join(base, "keos-fleet"), "eosdev", "stratio")
	data, err := os.ReadFile(tenantPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := "      - name: psql\n        patches:\n        - patch: |\n" +
		"            apiVersion: helm.toolkit.fluxcd.io/v2\n            kind: HelmRelease\n" +
		"            metadata:\n              name: psql-gosec-agent\n" +
		"          target:\n            kind: HelmRelease\n"
	before := strings.Replace(string(data), "      - name: psql\n", legacy, 1)
	if err := os.WriteFile(tenantPath, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	for name, replicas := range map[string]int64{"agent matches live": 1, "agent differs from live": 3} {
		t.Run(name, func(t *testing.T) {
			liveObj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": map[string]any{"name": "psql-gosec-agent", "namespace": "stratio-datastores"},
				"spec":     map[string]any{"replicas": replicas},
			}}
			opts := Options{
				Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio", Catalog: cat,
				App: config.App{ID: "psql-gosec-agent", Rset: "apps/components/resourceset-apps-fixture.yaml",
					Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent"},
				Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
					"flux-operator": {Stdout: []byte(rsetOutputGosecAgent)},
					"flux":          {Stdout: []byte(kustomizationBuildOutputGosecAgent)},
				}},
				Client: fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(liveObj).Build(),
				Log:    log.New(io.Discard, false),
			}
			result, err := Plan(context.Background(), opts)
			if err != nil {
				t.Fatalf("Plan returned error: %v", err)
			}
			if result.LegacyAgentPatches != 1 {
				t.Errorf("LegacyAgentPatches = %d, want 1", result.LegacyAgentPatches)
			}
			if result.Migrated != (replicas != 1) {
				t.Errorf("Migrated = %v, want %v", result.Migrated, replicas != 1)
			}
			after, err := os.ReadFile(tenantPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != before {
				t.Error("Plan modified the tenant file on disk")
			}
		})
	}
}

// A plain app (anchored at its own entry) never reports a legacy agent patch.
func TestPlan_NoLegacyAgentPatchForAPlainApp(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	liveObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": "psql", "namespace": "stratio-datastores"},
		"spec":     map[string]any{"instances": int64(1)},
	}}
	result, err := Plan(context.Background(), Options{
		Repos: config.ReposUnder(base), Cluster: "eosdev", Tenant: "stratio", Catalog: loadCatalog(t, base),
		App: config.App{ID: "psql", Rset: "apps/components/resourceset-apps-fixture.yaml", Kustomization: "apps-psql", Object: "psql"},
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(liveObj).Build(),
		Log:    log.New(io.Discard, false),
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if result.LegacyAgentPatches != 0 {
		t.Errorf("LegacyAgentPatches = %d, want 0", result.LegacyAgentPatches)
	}
}
