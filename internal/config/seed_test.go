package config

import "testing"

func TestSeedCatalog_Valid(t *testing.T) {
	cat := SeedCatalog()
	if len(cat.Types) != 19 {
		t.Fatalf("len(Types) = %d, want 19", len(cat.Types))
	}
	if err := cat.validate(); err != nil {
		t.Fatalf("validate() = %v, want nil", err)
	}
}

func TestSeedCatalog_GatedTypesDeclarePrepare(t *testing.T) {
	cat := SeedCatalog()

	cases := []struct {
		typ     string
		prepare string
	}{
		{"genai", "prepare-genai"},
		{"datamarket-agent", "prepare-datamarket-agent"},
		{"dlc-entity", "prepare-dlc"},
		{"bdl-datarest", "prepare-datarest"},
	}
	for _, c := range cases {
		typ := cat.Find(c.typ)
		if typ == nil {
			t.Fatalf("Find(%q) = nil", c.typ)
		}
		if typ.Prepare != c.prepare {
			t.Errorf("%s: Prepare = %q, want %q", c.typ, typ.Prepare, c.prepare)
		}
	}
}

func TestSeedCatalog_GosecAgentsHaveNoExplicitAnchor(t *testing.T) {
	cat := SeedCatalog()

	for _, id := range []string{"postgres-gosec-agent", "opensearch-gosec-agent"} {
		typ := cat.Find(id)
		if typ == nil {
			t.Fatalf("Find(%q) = nil", id)
		}
		if typ.Anchor != "" {
			t.Errorf("%s: Anchor = %q, want unset (the catalog derives it)", id, typ.Anchor)
		}
		if typ.ChartPath() == "" || typ.Entry == "" || typ.Object == "" {
			t.Errorf("%s: expected chart and entry/object templates to be set: %+v", id, typ)
		}
	}
}

func TestSeedCatalog_EveryTypeHasASelector(t *testing.T) {
	for _, typ := range SeedCatalog().Types {
		if typ.Match.Labels == nil && typ.Match.Annotations == nil {
			t.Errorf("%s: no label/annotation selector — it would match every %v", typ.Type, typ.Match.Kinds)
		}
	}
}

func TestSeedEnvironment(t *testing.T) {
	env := SeedEnvironment("/stratio/gitops", "eosdev", "stratio", "")
	if err := env.validate(); err != nil {
		t.Fatalf("validate() = %v", err)
	}
	if env.ChartsBase != "" || env.ChartsRoot() != "/stratio/gitops" {
		t.Errorf("unexpected charts fields: %+v", env)
	}
}
