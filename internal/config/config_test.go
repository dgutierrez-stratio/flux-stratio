package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `
base: /stratio/gitops
cluster: eosdev
tenant: stratio
apps:
  - id: psql
    name: Postgres psql
    rset: apps/components/resourceset-apps-datastores.yaml
    kustomization: apps-psql
    object: psql
    exclude:
      - spec.bootstrap.pgBackup
  - id: psql-gosec-agent
    name: Postgres gosec agent
    rset: apps/components/resourceset-apps-datastores.yaml
    kustomization: apps-psql-gosec-agent
    object: psql-gosec-agent
    anchor: config.agent
    chartPath: charts/gosec-agent
    renamed: psql-agent
`

func TestLoad_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flux-stratio.yaml")
	if err := os.WriteFile(path, []byte(validYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Base != "/stratio/gitops" || cfg.Cluster != "eosdev" || cfg.Tenant != "stratio" {
		t.Errorf("unexpected top-level fields: %+v", cfg)
	}
	if len(cfg.Apps) != 2 {
		t.Fatalf("len(Apps) = %d, want 2", len(cfg.Apps))
	}

	gosec := cfg.Find("psql-gosec-agent")
	if gosec == nil {
		t.Fatal("Find(\"psql-gosec-agent\") = nil")
	}
	if gosec.Anchor != "config.agent" {
		t.Errorf("Anchor = %q", gosec.Anchor)
	}
	if gosec.ChartPath != "charts/gosec-agent" || gosec.Renamed != "psql-agent" {
		t.Errorf("unexpected app fields: %+v", gosec)
	}
}

func TestLoad_UnknownTopLevelKeyRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flux-stratio.yaml")
	body := "base: /x\ncluster: c\ntenant: t\nnotAField: true\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load with an unknown top-level key: got nil error, want non-nil")
	}
}

func TestLoad_UnknownAppKeyRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flux-stratio.yaml")
	body := "base: /x\ncluster: c\ntenant: t\napps:\n  - id: a\n    name: A\n    rset: r\n    kustomization: k\n    object: o\n    namespace: dead-field\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load with an unknown app-level key (namespace): got nil error, want non-nil")
	}
}

func TestLoad_MissingRequiredFieldsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flux-stratio.yaml")
	body := "apps:\n  - id: a\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load with missing base/cluster/tenant and app fields: got nil error, want non-nil")
	}
	for _, want := range []string{"base is required", "cluster is required", "tenant is required", "name is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing expected substring %q", err, want)
		}
	}
}

func TestLoad_DuplicateAppIDRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flux-stratio.yaml")
	body := `
base: /x
cluster: c
tenant: t
apps:
  - id: a
    name: A
    rset: r
    kustomization: k
    object: o
  - id: a
    name: A2
    rset: r2
    kustomization: k2
    object: o2
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Errorf("Load error = %v, want it to mention duplicate id", err)
	}
}

func TestLoad_MissingFileReportsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("Load error = %v, want it to mention %q", err, path)
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
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env-config.yaml")
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
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-user-config-here"))
	cwdConfig := filepath.Join(dir, "flux-stratio.yaml")
	if err := os.WriteFile(cwdConfig, []byte("base: /x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != cwdConfig {
		t.Errorf("Resolve = %q, want %q", got, cwdConfig)
	}
}

func TestResolve_NoCandidateExistsReturnsActionableError(t *testing.T) {
	t.Setenv(EnvConfigFile, "")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-user-config-here"))
	t.Chdir(dir) // empty dir: no ./flux-stratio.yaml here

	_, err := Resolve("")
	if err == nil {
		t.Fatal("Resolve with no candidate file present: got nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "--config") || !strings.Contains(err.Error(), EnvConfigFile) {
		t.Errorf("error %q should name --config and %s as remedies", err, EnvConfigFile)
	}
}

func TestFind_NoMatchReturnsNil(t *testing.T) {
	cfg := Config{Apps: []App{{ID: "a"}}}
	if got := cfg.Find("nope"); got != nil {
		t.Errorf("Find(\"nope\") = %+v, want nil", got)
	}
}

func TestConfig_Effective(t *testing.T) {
	cfg := Config{Base: "cfg-base", Cluster: "cfg-cluster", Tenant: "cfg-tenant"}

	base, cluster, tenant := cfg.Effective("", "", "")
	if base != "cfg-base" || cluster != "cfg-cluster" || tenant != "cfg-tenant" {
		t.Errorf("no overrides: got (%q, %q, %q), want config values", base, cluster, tenant)
	}

	base, cluster, tenant = cfg.Effective("flag-base", "", "flag-tenant")
	if base != "flag-base" || cluster != "cfg-cluster" || tenant != "flag-tenant" {
		t.Errorf("partial overrides: got (%q, %q, %q)", base, cluster, tenant)
	}
}
