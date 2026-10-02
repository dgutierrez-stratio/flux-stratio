package config

import (
	"strings"
	"testing"
)

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
	env := SeedEnvironment("/stratio/gitops", "eosdev", "stratio", nil)
	if err := env.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if env.Repos != nil || env.Repo(RepoCharts) != "/stratio/gitops/charts" {
		t.Errorf("unexpected repos: %+v", env)
	}
}

func TestSeedCatalog_MemoryEnvNamesTheLegacySizeVariables(t *testing.T) {
	want := map[string]string{
		"bdl-datarest": "VR_POD_MEMORY_LIMIT", "eureka-agent": "VR_POD_MEMORY_LIMIT",
		"virtualizer": "VR_POD_MEMORY_LIMIT", "discovery": "MEMORY",
	}
	for _, ty := range SeedCatalog().Types {
		if name, ok := want[ty.Type]; ok && (len(ty.MemoryEnv) != 1 || ty.MemoryEnv[0] != name) {
			t.Errorf("%s memoryEnv = %v, want [%s]", ty.Type, ty.MemoryEnv, name)
		}
	}
}

func TestCatalogValidate_RejectsABadMemoryEnvName(t *testing.T) {
	cat := SeedCatalog()
	cat.Types[0].MemoryEnv = []string{"OK_NAME", "has space"}
	if err := cat.validate(); err == nil || !strings.Contains(err.Error(), `memoryEnv[1]: "has space" is not an env var name`) {
		t.Errorf("validate = %v, want it to reject the second name", err)
	}
}
