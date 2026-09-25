package config

import "testing"

func TestSeed_ValidAndComplete(t *testing.T) {
	cfg := Seed("/stratio/gitops", "eosdev", "stratio", "")

	if cfg.Base != "/stratio/gitops" || cfg.Cluster != "eosdev" || cfg.Tenant != "stratio" {
		t.Errorf("unexpected top-level fields: %+v", cfg)
	}
	if cfg.ChartsBase != "" {
		t.Errorf("ChartsBase = %q, want empty when not passed", cfg.ChartsBase)
	}
	if len(cfg.Apps) != 16 {
		t.Fatalf("len(Apps) = %d, want 16", len(cfg.Apps))
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate() = %v, want nil", err)
	}
}

func TestSeed_ChartsBaseRoundTrips(t *testing.T) {
	cfg := Seed("/stratio/gitops", "eosdev", "stratio", "/stratio/charts/charts")

	if cfg.ChartsBase != "/stratio/charts/charts" {
		t.Errorf("ChartsBase = %q, want %q", cfg.ChartsBase, "/stratio/charts/charts")
	}
}

func TestSeed_GatedAppsDeclarePrepare(t *testing.T) {
	cfg := Seed("/stratio/gitops", "eosdev", "stratio", "")

	cases := []struct {
		id      string
		prepare string
	}{
		{"genai", "prepare-genai"},
		{"datamarket-agent", "prepare-datamarket-agent"},
		{"dlc-entity", "prepare-dlc"},
		{"dg-datarest-pgi", "prepare-datarest"},
	}
	for _, c := range cases {
		app := cfg.Find(c.id)
		if app == nil {
			t.Fatalf("Find(%q) = nil", c.id)
		}
		if app.Prepare != c.prepare {
			t.Errorf("%s: Prepare = %q, want %q", c.id, app.Prepare, c.prepare)
		}
	}
}

func TestSeed_PreviousNamespaceScopedToTenant(t *testing.T) {
	cfg := Seed("/stratio/gitops", "eosdev", "acme", "")

	app := cfg.Find("datamarket-agent")
	if app == nil {
		t.Fatal(`Find("datamarket-agent") = nil`)
	}
	if want := "acme-datastores"; app.PreviousNamespace != want {
		t.Errorf("PreviousNamespace = %q, want %q", app.PreviousNamespace, want)
	}
}

func TestSeed_GosecAgentsHaveNoExplicitAnchor(t *testing.T) {
	cfg := Seed("/stratio/gitops", "eosdev", "stratio", "")

	for _, id := range []string{"psql-gosec-agent", "opensearch1-gosec-agent"} {
		app := cfg.Find(id)
		if app == nil {
			t.Fatalf("Find(%q) = nil", id)
		}
		if app.Anchor != "" {
			t.Errorf("%s: Anchor = %q, want unset (the catalog derives it)", id, app.Anchor)
		}
		if app.ChartPath == "" || app.Renamed == "" {
			t.Errorf("%s: expected ChartPath and Renamed to be set: %+v", id, app)
		}
	}
}
