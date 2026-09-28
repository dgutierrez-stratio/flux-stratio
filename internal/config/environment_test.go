package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvironment_FileWithOverrides(t *testing.T) {
	t.Setenv(EnvEnvironmentFile, "")
	path := writeFile(t, "environment.yaml", "base: /stratio/gitops\nchartsBase: /stratio/charts\ncluster: eosdev\ntenant: stratio\n")

	env, err := LoadEnvironment(path, Environment{Tenant: "acme"})
	if err != nil {
		t.Fatalf("LoadEnvironment returned error: %v", err)
	}
	want := Environment{Base: "/stratio/gitops", ChartsBase: "/stratio/charts", Cluster: "eosdev", Tenant: "acme"}
	if env != want {
		t.Errorf("env = %+v, want %+v", env, want)
	}
	if env.ChartsRoot() != "/stratio/charts" {
		t.Errorf("ChartsRoot() = %q, want chartsBase", env.ChartsRoot())
	}
}

func TestLoadEnvironment_NoFileButOverridesSufficient(t *testing.T) {
	t.Setenv(EnvEnvironmentFile, "")
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "no-home"))
	t.Chdir(dir)

	env, err := LoadEnvironment("", Environment{Base: "/b", Cluster: "c", Tenant: "t"})
	if err != nil {
		t.Fatalf("LoadEnvironment returned error: %v", err)
	}
	if env.ChartsRoot() != "/b" {
		t.Errorf("ChartsRoot() = %q, want base when chartsBase unset", env.ChartsRoot())
	}
}

func TestLoadEnvironment_NoFileAndMissingFieldsIsActionable(t *testing.T) {
	t.Setenv(EnvEnvironmentFile, "")
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "no-home"))
	t.Chdir(dir)

	_, err := LoadEnvironment("", Environment{Base: "/b"})
	if err == nil {
		t.Fatal("LoadEnvironment returned nil error, want missing cluster/tenant")
	}
	for _, want := range []string{"cluster", "tenant", "config init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestLoadEnvironment_ExplicitMissingFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.yaml")
	_, err := LoadEnvironment(path, Environment{Base: "/b", Cluster: "c", Tenant: "t"})
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("LoadEnvironment error = %v, want it to name %s", err, path)
	}
}

func TestLoadEnvironment_UnknownKeyRejected(t *testing.T) {
	path := writeFile(t, "environment.yaml", "base: /b\ncluster: c\ntenant: t\napps: []\n")
	if _, err := LoadEnvironment(path, Environment{}); err == nil {
		t.Error("LoadEnvironment accepted an unknown key, want an error")
	}
}
