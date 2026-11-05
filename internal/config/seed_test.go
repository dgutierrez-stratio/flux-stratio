package config

import (
	"strings"
	"testing"
)

func TestSeedCatalog_Valid(t *testing.T) {
	cat := SeedCatalog()
	if len(cat.Types) != 20 {
		t.Fatalf("len(Types) = %d, want 20", len(cat.Types))
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
		{"virtualizer", "prepare-virtualizer"},
		{"dg-agent", "prepare-dg-agent"},
		{"discovery", "prepare-discovery"},
		{"litellm", "prepare-litellm"},
		{"postgres-gosec-agent", "prepare-gosec-agent"},
		{"opensearch-gosec-agent", "prepare-gosec-agent"},
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

// A legacy governance agent's env says which GitOps overlay it is and what
// it points at; the seed declares it for dg-agent.
func TestSeedCatalog_DgAgentDerivesItsConfigFromTheLegacyEnv(t *testing.T) {
	typ := SeedCatalog().Find("dg-agent")
	if typ == nil || len(typ.ConfigFromEnv) != 3 {
		t.Fatalf("dg-agent configFromEnv = %+v, want type and the two governance dependencies", typ)
	}
	byKey := map[string]EnvConfig{}
	for _, c := range typ.ConfigFromEnv {
		byKey[c.Key] = c
	}
	if got, ok := byKey["type"].Apply("hdfs"); got != "hdfs" || !ok {
		t.Errorf("type from HDFS = %q, %v", got, ok)
	}
	if _, ok := byKey["type"].Apply("POSTGRESQL"); ok {
		t.Error("POSTGRESQL has no GitOps overlay but was accepted")
	}
	if got, _ := byKey["dependencies.governancePgbouncer.name"].Apply("pgbouncer-postgreskeos-governance.keos-core"); got != "pgbouncer-postgreskeos-governance" {
		t.Errorf("governance pgbouncer = %q", got)
	}
}

func TestEnvConfig_Validate(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    EnvConfig
		want string // "" = valid
	}{
		{"type", EnvConfig{Key: "type", Env: "X", Values: map[string]string{"A": "a"}}, ""},
		{"dependency", EnvConfig{Key: "dependencies.governancePostgres.name", Env: "X"}, ""},
		{"pattern", EnvConfig{Key: "dependencies.d.name", Env: "X", Pattern: "^([^.]+)"}, ""},
		{"config field", EnvConfig{Key: "postgresDatabase", Env: "X"}, ""},
		{"not a key", EnvConfig{Key: "config.size", Env: "X"}, "must be"},
		{"dependency without name", EnvConfig{Key: "dependencies.d", Env: "X"}, "must be"},
		{"no env", EnvConfig{Key: "type"}, "not an env var name"},
		{"both", EnvConfig{Key: "type", Env: "X", Values: map[string]string{"A": "a"}, Pattern: "(a)"}, "exclusive"},
		{"no group", EnvConfig{Key: "type", Env: "X", Pattern: "a"}, "exactly one capture group"},
		{"bad regexp", EnvConfig{Key: "type", Env: "X", Pattern: "("}, "pattern"},
	} {
		got := strings.Join(tc.c.validate("f"), "; ")
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: validate = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEnvConfig_Apply(t *testing.T) {
	values := EnvConfig{Key: "type", Env: "X", Values: map[string]string{"HDFS": "hdfs"}}
	if got, ok := values.Apply(" hdfs "); got != "hdfs" || !ok {
		t.Errorf("Apply = %q, %v, want a case- and space-insensitive match", got, ok)
	}
	pattern := EnvConfig{Key: "dependencies.d.name", Env: "X", Pattern: "^([a-z]+)-"}
	if got, ok := pattern.Apply("ABC"); got != "" || !ok {
		t.Errorf("a pattern that does not match = %q, %v, want nothing written but known", got, ok)
	}
	if (EnvConfig{Key: "dependencies.d.name"}).DependencyKey() != "d" || (EnvConfig{Key: "type"}).DependencyKey() != "" {
		t.Error("DependencyKey")
	}
	if (EnvConfig{Key: "postgresDatabase"}).Field() != "postgresDatabase" || (EnvConfig{Key: "type"}).Field() != "" || (EnvConfig{Key: "dependencies.d.name"}).Field() != "" {
		t.Error("Field")
	}
}

func TestSeedCatalog_DatarestDerivesItsDatabaseFromTheLegacyEnv(t *testing.T) {
	typ := SeedCatalog().Find("bdl-datarest")
	if typ == nil || len(typ.ConfigFromEnv) != 1 || typ.ConfigFromEnv[0].Field() != "postgresDatabase" || typ.ConfigFromEnv[0].Env != "JDBC_DATABASE" {
		t.Fatalf("bdl-datarest configFromEnv = %+v, want postgresDatabase from JDBC_DATABASE", typ)
	}
}
