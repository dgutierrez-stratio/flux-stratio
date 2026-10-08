package tenantimport

import (
	"context"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
)

func helmSpec(values map[string]any) map[string]any {
	return map[string]any{"values": values}
}

func usesCatalog(uses ...config.Use) *config.Catalog {
	return &config.Catalog{Types: []config.ComponentType{{
		Type: "virtualizer", Component: "virtualizer", Chart: &config.Chart{Path: "virtualizer", Uses: uses},
	}}}
}

func TestUseEvidence(t *testing.T) {
	spec := helmSpec(map[string]any{
		"on":      map[string]any{"enabled": true},
		"off":     map[string]any{"enabled": false},
		"host":    map[string]any{"name": "virtualizer.stratio-apps"},
		"empty":   map[string]any{"name": ""},
		"textoff": map[string]any{"enabled": "false"},
		"zero":    map[string]any{"n": int64(0)},
		"null":    map[string]any{"n": nil},
	})
	for _, tc := range []struct {
		paths []string
		want  string
	}{
		{[]string{"on.enabled"}, "on.enabled"},
		{[]string{"off.enabled"}, ""},
		{[]string{"host.name"}, "host.name"},
		{[]string{"empty.name"}, ""},
		{[]string{"textoff.enabled"}, ""},
		{[]string{"zero.n"}, ""},
		{[]string{"null.n"}, ""},
		{[]string{"missing.path"}, ""},
		{[]string{"off.enabled", "on.enabled"}, "on.enabled"},
		{[]string{"on.enabled.deeper"}, ""},
	} {
		if got := useEvidence(spec, tc.paths); got != tc.want {
			t.Errorf("useEvidence(%v) = %q, want %q", tc.paths, got, tc.want)
		}
	}
}

func TestScanUses_EvidenceUnknownAndAbsent(t *testing.T) {
	cfg := usesCatalog(
		config.Use{Dependency: "postgres", Values: []string{"pg.enabled"}},
		config.Use{Dependency: "connectors", Values: []string{"conn.enabled"}},
	)
	migrated := &Entry{Name: "migrated", HelmReleaseSpec: helmSpec(map[string]any{
		"pg":   map[string]any{"enabled": true},
		"conn": map[string]any{"enabled": false},
	})}
	legacy := &Entry{Name: "legacy"}
	components := Components{"virtualizer": {migrated, legacy}}

	scanUses(Options{Config: cfg}, components)

	if got := migrated.DepEnabled["postgres"]; got != "pg.enabled" {
		t.Errorf("migrated postgres evidence = %q, want pg.enabled", got)
	}
	if _, ok := migrated.DepEnabled["connectors"]; ok || len(migrated.DepUnknown) != 0 {
		t.Errorf("migrated connectors: DepEnabled=%v DepUnknown=%v, want off and decided", migrated.DepEnabled, migrated.DepUnknown)
	}
	if len(legacy.DepEnabled) != 0 || !legacy.DepUnknown["postgres"] || !legacy.DepUnknown["connectors"] {
		t.Errorf("legacy: DepEnabled=%v DepUnknown=%v, want both undecided", legacy.DepEnabled, legacy.DepUnknown)
	}
}

func TestUsesRules_OldCatalogInheritsTheSeedsRules(t *testing.T) {
	old := &config.Catalog{Types: []config.ComponentType{
		{Type: "rocket", Component: "rocket", Chart: &config.Chart{Path: "rocket"}},
		{Type: "custom", Component: "custom", Chart: &config.Chart{Path: "custom"}},
	}}
	rules := usesRules(old)
	if len(rules["rocket"]) == 0 {
		t.Errorf("rocket rules = %v, want the seed's", rules["rocket"])
	}
	if len(rules["custom"]) != 0 {
		t.Errorf("custom rules = %v, want none", rules["custom"])
	}
	if usesRules(nil) != nil {
		t.Error("a nil catalog derives rules")
	}
}

func TestRender_WritesEnabledOnlyWithEvidence(t *testing.T) {
	entry := &Entry{
		Name:       "virtualizer",
		Deps:       map[string]string{"postgres": "psql", "connectors": "connectors", "hdfs": ""},
		DepEnabled: map[string]string{"postgres": "pg.enabled", "hdfs": "x"},
	}
	out, err := Render("stratio", "S", Components{"virtualizer": {entry}})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Spec struct {
			DefaultValues struct {
				Components map[string][]struct {
					Config struct {
						Dependencies map[string]map[string]any `yaml:"dependencies"`
					} `yaml:"config"`
				} `yaml:"components"`
			} `yaml:"defaultValues"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	deps := doc.Spec.DefaultValues.Components["virtualizer"][0].Config.Dependencies
	if deps["postgres"]["enabled"] != true {
		t.Errorf("postgres = %v, want enabled: true", deps["postgres"])
	}
	if _, ok := deps["connectors"]["enabled"]; ok {
		t.Errorf("connectors = %v, want no enabled (no evidence)", deps["connectors"])
	}
	if _, ok := deps["hdfs"]["enabled"]; ok {
		t.Errorf("hdfs = %v, want no enabled (unresolved name)", deps["hdfs"])
	}
}

const usesTenant = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  type: Static
  defaultValues:
    tenantName: stratio
    size: S
    sopsSecretProvided: false
    internalS3BucketName: ""
    userEmail: ""
    userId: ""
    userName: ""
    components:
      virtualizer:
      - name: virtualizer
        config:
          dependencies:
            postgres:
              name: psql
            connectors:
              name: connectors
          uiEnabled: false
`

func usesScanned(enabled map[string]string, unknown map[string]bool) Components {
	return Components{"virtualizer": {{
		Name: "virtualizer", DepEnabled: enabled, DepUnknown: unknown,
	}}}
}

func TestComplete_AddsEnabledToADeclaredDependencyAndIsIdempotent(t *testing.T) {
	doc := reload(t, []byte(usesTenant))
	scanned := usesScanned(map[string]string{"postgres": "pg.enabled"}, nil)
	r, out := complete(t, doc, scanned, Live{}, CompleteOptions{})

	if !contains(r.Added, "config.dependencies.postgres.enabled: true (spec.values.pg.enabled") {
		t.Errorf("Added = %v, want postgres enabled", r.Added)
	}
	want := strings.Replace(usesTenant, "              name: psql\n", "              name: psql\n              enabled: true\n", 1)
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}

	r2, out2 := complete(t, reload(t, []byte(out)), scanned, Live{}, CompleteOptions{})
	if contains(r2.Added, "enabled") || out2 != out {
		t.Errorf("second run added %v and changed the file:\n%s", r2.Added, out2)
	}
}

func TestComplete_KeepsAHandWrittenFalseAndWarns(t *testing.T) {
	src := strings.Replace(usesTenant, "              name: psql\n", "              name: psql\n              enabled: false\n", 1)
	r, out := complete(t, reload(t, []byte(src)), usesScanned(map[string]string{"postgres": "pg.enabled"}, nil), Live{}, CompleteOptions{})
	if out != src {
		t.Errorf("file changed:\n%s", out)
	}
	if !contains(r.Warnings, "config.dependencies.postgres.enabled is false but spec.values.pg.enabled is set") {
		t.Errorf("Warnings = %v, want the disagreement", r.Warnings)
	}
}

func TestComplete_NoEvidenceLeavesItOffAndUndecidedWarns(t *testing.T) {
	// A HelmRelease that does not show the use: nothing, no warning.
	r, out := complete(t, reload(t, []byte(usesTenant)), usesScanned(nil, nil), Live{}, CompleteOptions{})
	if out != usesTenant || contains(r.Warnings, "cannot tell") {
		t.Errorf("decided-off dependency changed the file or warned: %v\n%s", r.Warnings, out)
	}

	// No HelmRelease at all: warn, write nothing.
	r, out = complete(t, reload(t, []byte(usesTenant)), usesScanned(nil, map[string]bool{"postgres": true}), Live{}, CompleteOptions{})
	if out != usesTenant {
		t.Errorf("undecided dependency changed the file:\n%s", out)
	}
	if !contains(r.Warnings, `cannot tell whether it uses dependency "postgres"`) {
		t.Errorf("Warnings = %v, want the undecided warning", r.Warnings)
	}
}

func TestComplete_FlowStyleDependencyWarnsInsteadOfEditing(t *testing.T) {
	src := strings.Replace(usesTenant, "            postgres:\n              name: psql\n", "            postgres: {name: psql}\n", 1)
	r, out := complete(t, reload(t, []byte(src)), usesScanned(map[string]string{"postgres": "pg.enabled"}, nil), Live{}, CompleteOptions{})
	if out != src {
		t.Errorf("file changed:\n%s", out)
	}
	if !contains(r.Warnings, "not a block mapping") || contains(r.Added, "postgres.enabled") {
		t.Errorf("Warnings = %v, Added = %v, want a hand-edit warning and no addition", r.Warnings, r.Added)
	}
}

func TestComplete_ANewDependencyIsAddedEnabledInOneGo(t *testing.T) {
	src := strings.Replace(usesTenant, "            postgres:\n              name: psql\n", "", 1)
	scanned := usesScanned(map[string]string{"postgres": "pg.enabled"}, nil)
	scanned["postgres"] = []*Entry{{Name: "psql"}}
	r, out := complete(t, reload(t, []byte(src)), scanned, Live{}, CompleteOptions{})
	if !strings.Contains(out, "            postgres:\n              enabled: true\n              name: psql\n") {
		t.Errorf("postgres not added enabled:\n%s", out)
	}
	if !contains(r.Added, "postgres.name: psql, enabled: true") {
		t.Errorf("Added = %v", r.Added)
	}
}

func TestRun_WritesEnabledFromTheLiveHelmRelease(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		pgCluster("psql", "stratio-datastores"),
		helmRelease("virtualizer", "stratio-apps", "virtualizer", map[string]any{"pg": map[string]any{"enabled": true}}),
	).Build()
	cfg := usesCatalog(
		config.Use{Dependency: "postgres", Values: []string{"pg.enabled"}},
		config.Use{Dependency: "pgbouncer", Values: []string{"pgb.enabled"}},
	)

	out, err := Run(context.Background(), Options{
		TenantName: "stratio", Catalog: loadFixtureCatalog(t), Config: cfg, Client: c, Log: log.New(io.Discard, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	comps := doc["spec"].(map[string]any)["defaultValues"].(map[string]any)["components"].(map[string]any)
	deps := comps["virtualizer"].([]any)[0].(map[string]any)["config"].(map[string]any)["dependencies"].(map[string]any)
	if pg := deps["postgres"].(map[string]any); pg["enabled"] != true || pg["name"] != "psql" {
		t.Errorf("postgres = %v, want enabled: true, name: psql", pg)
	}
	if pgb := deps["pgbouncer"].(map[string]any); pgb["enabled"] != nil {
		t.Errorf("pgbouncer = %v, want no enabled (HelmRelease does not show the use)", pgb)
	}
}

func TestTruthy_StringSpellingsOfOffAreNotEvidence(t *testing.T) {
	for _, off := range []string{"", " ", "false", "False", "FALSE", " false ", "0", "no", "No", "off", "OFF"} {
		if truthy(off) {
			t.Errorf("truthy(%q) = true, want false", off)
		}
	}
	for _, on := range []string{"true", "True", "yes", "1", "on", "virtualizer.stratio-apps"} {
		if !truthy(on) {
			t.Errorf("truthy(%q) = false, want true", on)
		}
	}
	spec := helmSpec(map[string]any{"genai": map[string]any{"genaiEnabled": "False"}, "n": map[string]any{"c": "0"}})
	if got := useEvidence(spec, []string{"genai.genaiEnabled", "n.c"}); got != "" {
		t.Errorf("useEvidence = %q, want none for string spellings of off", got)
	}
}
