package appmigrate

import (
	"reflect"
	"testing"

	"github.com/Stratio/flux-stratio/internal/config"
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
