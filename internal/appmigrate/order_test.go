package appmigrate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

func TestOrderApps_DependencyBeforeDependent(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	doc, err := loadTenantDoc(t, base, "eosdev", "stratio")
	if err != nil {
		t.Fatal(err)
	}

	// pool-psql (pgbouncer) depends on psql (postgres) in the fixture's
	// tenant.yaml (config.dependencies.postgres.name: psql). Listed here
	// in the "wrong" order on purpose.
	apps := []config.App{
		{ID: "pool-psql", Kustomization: "apps-pool-psql", Object: "pool-psql"},
		{ID: "psql", Kustomization: "apps-psql", Object: "psql"},
	}

	ordered := OrderApps(doc, cat, apps)

	psqlIdx, poolIdx := -1, -1
	for i, a := range ordered {
		switch a.ID {
		case "psql":
			psqlIdx = i
		case "pool-psql":
			poolIdx = i
		}
	}
	if psqlIdx == -1 || poolIdx == -1 {
		t.Fatalf("ordered = %+v, missing an app", ordered)
	}
	if psqlIdx > poolIdx {
		t.Errorf("psql (dependency) at index %d, pool-psql (dependent) at index %d — dependency must come first", psqlIdx, poolIdx)
	}
}

func TestOrderApps_IndependentAppsKeepRelativeOrder(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	doc, err := loadTenantDoc(t, base, "eosdev", "stratio")
	if err != nil {
		t.Fatal(err)
	}

	apps := []config.App{
		{ID: "connectors", Kustomization: "apps-connectors", Object: "connectors"},
		{ID: "opensearch1", Kustomization: "apps-opensearch1", Object: "opensearch1"},
	}
	ordered := OrderApps(doc, cat, apps)
	if !reflect.DeepEqual(ordered, apps) {
		t.Errorf("ordered = %+v, want unchanged %+v (no dependency edge between them)", ordered, apps)
	}
}

func TestOrderApps_AppNotYetInTenantFileKeepsPosition(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	doc, err := loadTenantDoc(t, base, "eosdev", "stratio")
	if err != nil {
		t.Fatal(err)
	}

	apps := []config.App{
		{ID: "not-present", Kustomization: "apps-not-present", Object: "not-present"},
		{ID: "psql", Kustomization: "apps-psql", Object: "psql"},
	}
	ordered := OrderApps(doc, cat, apps)
	if len(ordered) != 2 {
		t.Fatalf("len(ordered) = %d, want 2", len(ordered))
	}
}

func TestTopoSort_NoEdgesPreservesOrder(t *testing.T) {
	apps := []config.App{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	graph := make([][]int, 3)
	got := topoSort(apps, graph)
	if !reflect.DeepEqual(got, apps) {
		t.Errorf("got = %+v, want unchanged %+v", got, apps)
	}
}

func TestTopoSort_SimpleChain(t *testing.T) {
	apps := []config.App{{ID: "dependent"}, {ID: "dependency"}}
	graph := [][]int{{1}, nil} // apps[0] depends on apps[1]
	got := topoSort(apps, graph)
	if got[0].ID != "dependency" || got[1].ID != "dependent" {
		t.Errorf("got = %+v, want dependency before dependent", got)
	}
}

func TestTopoSort_CycleDoesNotHang(t *testing.T) {
	apps := []config.App{{ID: "a"}, {ID: "b"}}
	graph := [][]int{{1}, {0}} // a -> b -> a
	got := topoSort(apps, graph)
	if len(got) != 2 {
		t.Errorf("len(got) = %d, want 2 (a cycle must not drop apps or hang)", len(got))
	}
}

func TestUnresolvedDeps(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	path := tenantfile.Path(filepath.Join(base, "keos-fleet"), "eosdev", "stratio")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// psql's entry gains a postgres dependency naming an entry the tenant
	// doesn't declare; its pgbackuprepository one isn't a component key in
	// the fixture catalog, so it's never reported.
	data = []byte(strings.Replace(string(data),
		"            pgbackuprepository:\n              name: pgbackuprepository\n",
		"            pgbackuprepository:\n              name: pgbackuprepository\n            postgres:\n              name: psql-old\n", 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := loadTenantDoc(t, base, "eosdev", "stratio")
	if err != nil {
		t.Fatal(err)
	}

	got, err := unresolvedDeps(doc, cat, config.App{ID: "psql", Kustomization: "apps-psql", Object: "psql"})
	if err != nil {
		t.Fatal(err)
	}
	want := []tenantfile.UnresolvedDependency{{Key: "postgres", Name: "psql-old", Declared: []string{"psql"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unresolvedDeps = %+v, want %+v", got, want)
	}

	if got, err := unresolvedDeps(doc, cat, config.App{ID: "rocket", Kustomization: "apps-rocket", Object: "rocket"}); err != nil || got != nil {
		t.Errorf("an app not in the tenant file: unresolvedDeps = %+v, %v; want nothing", got, err)
	}
}

func TestLegacyAgentPatches(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	path := tenantfile.Path(filepath.Join(base, "keos-fleet"), "eosdev", "stratio")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// psql's entry gains the patch the legacy client wrote for its gosec
	// agent at the entry's own top level, where nothing reads it.
	legacy := "      - name: psql\n        patches:\n        - patch: |\n" +
		"            apiVersion: helm.toolkit.fluxcd.io/v2\n            kind: HelmRelease\n" +
		"            metadata:\n              name: psql-gosec-agent\n" +
		"          target:\n            kind: HelmRelease\n"
	data = []byte(strings.Replace(string(data), "      - name: psql\n", legacy, 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := loadTenantDoc(t, base, "eosdev", "stratio")
	if err != nil {
		t.Fatal(err)
	}

	agent := config.App{ID: "psql-gosec-agent", Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent"}
	if got := legacyAgentPatches(doc, cat, agent); got != 1 {
		t.Errorf("legacyAgentPatches(agent) = %d, want 1", got)
	}
	// The parent's own app is anchored at the entry itself: its top-level
	// patches are where its patch belongs, never a legacy placement.
	if got := legacyAgentPatches(doc, cat, config.App{ID: "psql", Kustomization: "apps-psql", Object: "psql"}); got != 0 {
		t.Errorf("legacyAgentPatches(parent) = %d, want 0", got)
	}
}

// legacyAgentPatches never fails a plan: what it can't resolve counts none.
func TestLegacyAgentPatches_UnresolvableCountsNone(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	doc, err := loadTenantDoc(t, base, "eosdev", "stratio")
	if err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct {
		cat *catalog.Catalog
		app config.App
	}{
		"no catalog":             {nil, config.App{ID: "psql-gosec-agent", Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent"}},
		"unknown kustomization":  {cat, config.App{ID: "nope", Kustomization: "apps-nope", Object: "nope"}},
		"parent not in the file": {cat, config.App{ID: "x-gosec-agent", Kustomization: "apps-x-gosec-agent", Object: "x-gosec-agent"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := legacyAgentPatches(doc, tt.cat, tt.app); got != 0 {
				t.Errorf("legacyAgentPatches = %d, want 0", got)
			}
		})
	}
}

// TestDependencies_NamesEachAppsDependencies: the same tenant-file edges
// OrderApps sorts by, keyed by app ID.
func TestDependencies_NamesEachAppsDependencies(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	cat := loadCatalog(t, base)
	doc, err := loadTenantDoc(t, base, "eosdev", "stratio")
	if err != nil {
		t.Fatal(err)
	}
	apps := []config.App{
		{ID: "pool-psql", Kustomization: "apps-pool-psql", Object: "pool-psql"},
		{ID: "psql", Kustomization: "apps-psql", Object: "psql"},
	}
	got := Dependencies(doc, cat, apps)
	if want := map[string][]string{"pool-psql": {"psql"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Dependencies = %v, want %v", got, want)
	}
}
