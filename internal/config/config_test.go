package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validCatalogYAML = `
types:
  - type: postgres
    name: Postgres
    component: postgres
    rset: apps/components/resourceset-apps-datastores.yaml
    match:
      kinds: [postgres.stratio.com/v1/PgCluster]
      annotations:
        matchLabels:
          cct.stratio.com/application_service: Postgres
    exclude:
      - spec.bootstrap.pgBackup
  - type: postgres-gosec-agent
    name: Postgres gosec agent
    component: postgres
    rset: apps/components/resourceset-apps-datastores.yaml
    anchor: config.agent
    entry: '{{ .Live.Name | trimSuffix "-agent" }}'
    object: '{{ .Entry }}-gosec-agent'
    chart:
      path: gosec-agent
    match:
      kinds: [apps/v1/Deployment]
      annotations:
        matchExpressions:
          - key: cct.stratio.com/application_service
            operator: In
            values: [pg-gosec-agent]
`

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_ValidCatalog(t *testing.T) {
	cat, err := Load(writeFile(t, "catalog.yaml", validCatalogYAML))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cat.Types) != 2 {
		t.Fatalf("len(Types) = %d, want 2", len(cat.Types))
	}

	gosec := cat.Find("postgres-gosec-agent")
	if gosec == nil {
		t.Fatal(`Find("postgres-gosec-agent") = nil`)
	}
	if gosec.Anchor != "config.agent" || gosec.ChartPath() != "gosec-agent" {
		t.Errorf("unexpected type fields: %+v", gosec)
	}
	if got := gosec.KustomizationTemplate(); got != DefaultKustomization {
		t.Errorf("KustomizationTemplate() = %q, want the default %q", got, DefaultKustomization)
	}
	if pg := cat.Find("postgres"); pg.EntryTemplate() != DefaultEntry || pg.ObjectTemplate() != DefaultObject {
		t.Errorf("postgres templates = (%q, %q), want defaults", pg.EntryTemplate(), pg.ObjectTemplate())
	}
}

func TestLoad_UnknownKeyRejected(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"top-level", validCatalogYAML + "bogus: true\n"},
		{"type-level", strings.Replace(validCatalogYAML, "    name: Postgres\n", "    name: Postgres\n    renamed: psql\n", 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Load(writeFile(t, "catalog.yaml", c.body)); err == nil {
				t.Error("Load accepted an unknown key, want an error")
			}
		})
	}
}

func TestLoad_LegacyConfigGetsActionableError(t *testing.T) {
	legacy := "base: /stratio/gitops\ncluster: eosdev\ntenant: stratio\napps:\n  - id: psql\n"
	_, err := Load(writeFile(t, "config.yaml", legacy))
	if err == nil || !strings.Contains(err.Error(), "config init") {
		t.Errorf("Load error = %v, want it to point at `config init`", err)
	}
}

func TestLoad_ValidationProblemsReported(t *testing.T) {
	body := `
types:
  - type: a
    match:
      kinds: [not-a-kind]
      labels:
        matchExpressions:
          - key: k
            operator: Maybe
  - type: a
    name: A
    component: a
    rset: r.yaml
    entry: '{{ .Live.Name'
    match:
      kinds: [v1/ConfigMap]
`
	_, err := Load(writeFile(t, "catalog.yaml", body))
	if err == nil {
		t.Fatal("Load returned nil error, want validation problems")
	}
	for _, want := range []string{
		"a: name is required", "a: component is required", "a: rset is required",
		`kind "not-a-kind"`, `unknown operator "Maybe"`, `duplicate type "a"`, "entry:",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q; got:\n%v", want, err)
		}
	}
}

// Chart paths used to start with the charts repo's own directory name
// (charts/gosec-agent); they're relative to the repo root now, and an old
// catalog fails saying so.
func TestLoad_ChartPathWithChartsPrefixRejected(t *testing.T) {
	body := strings.Replace(validCatalogYAML, "path: gosec-agent", "path: charts/gosec-agent", 1)
	_, err := Load(writeFile(t, "catalog.yaml", body))
	if err == nil {
		t.Fatal("Load accepted a charts/-prefixed chart.path, want an error")
	}
	for _, want := range []string{`"gosec-agent"`, "config init --force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestLoad_MissingFileReportsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("Load error = %v, want it to mention %q", err, path)
	}
}

func TestParseKind(t *testing.T) {
	cases := []struct {
		in      string
		group   string
		version string
		kind    string
		wantErr bool
	}{
		{in: "apps/v1/Deployment", group: "apps", version: "v1", kind: "Deployment"},
		{in: "v1/ConfigMap", version: "v1", kind: "ConfigMap"},
		{in: "Deployment", wantErr: true},
		{in: "apps//Deployment", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			gvk, err := ParseKind(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("ParseKind(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			}
			if !c.wantErr && (gvk.Group != c.group || gvk.Version != c.version || gvk.Kind != c.kind) {
				t.Errorf("ParseKind(%q) = %v", c.in, gvk)
			}
		})
	}
}

func TestCatalog_KindsDeduplicated(t *testing.T) {
	cat := Catalog{Types: []ComponentType{
		{Match: Match{Kinds: []string{"apps/v1/Deployment", "v1/ConfigMap"}}},
		{Match: Match{Kinds: []string{"apps/v1/Deployment"}}},
	}}
	if got := cat.Kinds(); len(got) != 2 || got[0].Kind != "Deployment" || got[1].Kind != "ConfigMap" {
		t.Errorf("Kinds() = %v, want [Deployment ConfigMap]", got)
	}
}

func TestResolve_ExplicitFlagWins(t *testing.T) {
	t.Setenv(EnvConfigFile, "/should/not/be/used.yaml")
	got, err := Resolve("./explicit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs("./explicit.yaml")
	if got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
}

func TestResolve_EnvVarUsedWhenNoFlag(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "env-catalog.yaml")
	t.Setenv(EnvConfigFile, envPath)

	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != envPath {
		t.Errorf("Resolve = %q, want %q", got, envPath)
	}
}

func TestResolve_FallsBackToCwdFileWhenItExists(t *testing.T) {
	t.Setenv(EnvConfigFile, "")
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "no-fluxcd-home-here"))
	cwdCatalog := filepath.Join(dir, "flux-stratio.yaml")
	if err := os.WriteFile(cwdCatalog, []byte("types: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != cwdCatalog {
		t.Errorf("Resolve = %q, want %q", got, cwdCatalog)
	}
}

func TestResolve_PrefersFluxcdHomeOverCwd(t *testing.T) {
	t.Setenv(EnvConfigFile, "")
	home := t.TempDir()
	userCatalog := filepath.Join(home, ".fluxcd", "flux-stratio", CatalogFile)
	if err := os.MkdirAll(filepath.Dir(userCatalog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userCatalog, []byte("types: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "flux-stratio.yaml"), []byte("types: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != userCatalog {
		t.Errorf("Resolve = %q, want %q (should prefer ~/.fluxcd/flux-stratio/catalog.yaml over ./flux-stratio.yaml)", got, userCatalog)
	}
}

func TestResolve_NoCandidateExistsReturnsActionableError(t *testing.T) {
	t.Setenv(EnvConfigFile, "")
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "no-fluxcd-home-here"))
	t.Chdir(dir)

	_, err := Resolve("")
	if err == nil {
		t.Fatal("Resolve with no candidate file present: got nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "config init") || !strings.Contains(err.Error(), EnvConfigFile) {
		t.Errorf("error %q should name config init and %s as remedies", err, EnvConfigFile)
	}
}

func TestResolve_LegacyConfigInHomeMentioned(t *testing.T) {
	t.Setenv(EnvConfigFile, "")
	home := t.TempDir()
	legacy := filepath.Join(home, ".fluxcd", "flux-stratio", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("apps: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	_, err := Resolve("")
	if err == nil || !strings.Contains(err.Error(), legacy) {
		t.Errorf("Resolve error = %v, want it to mention the legacy %s", err, legacy)
	}
}

func TestMarshal_UsesTwoSpaceIndent(t *testing.T) {
	// gopkg.in/yaml.v3's default Marshal would indent "name:" 4 spaces
	// deeper than the "- type:" it belongs under; this plugin's convention
	// (internal/tenantfile.Doc.Bytes, and every other generated YAML file)
	// is 2, including `flux stratio config init`'s own output.
	cat := Catalog{Types: []ComponentType{{Type: "postgres", Name: "Postgres"}}}
	out, err := Marshal(cat)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "\n  - type: postgres") || !strings.Contains(s, "\n    name: Postgres") {
		t.Errorf("output missing 2-space-indented types list; got:\n%s", s)
	}
}

func TestApp_LiveNameAndNamespace(t *testing.T) {
	plain := App{Object: "psql-gosec-agent"}
	if plain.LiveName() != "psql-gosec-agent" || plain.LiveNamespace() != "" {
		t.Errorf("no live ref: LiveName/LiveNamespace = %q/%q", plain.LiveName(), plain.LiveNamespace())
	}
	renamed := App{Object: "psql-gosec-agent", Live: []ObjectRef{{Namespace: "stratio-datastores", Name: "psql-agent"}}}
	if renamed.LiveName() != "psql-agent" || renamed.LiveNamespace() != "stratio-datastores" {
		t.Errorf("live ref: LiveName/LiveNamespace = %q/%q", renamed.LiveName(), renamed.LiveNamespace())
	}
}

func TestCatalog_FindNoMatchReturnsNil(t *testing.T) {
	cat := Catalog{Types: []ComponentType{{Type: "a"}}}
	if got := cat.Find("nope"); got != nil {
		t.Errorf(`Find("nope") = %+v, want nil`, got)
	}
}
