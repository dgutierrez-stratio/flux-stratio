package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvironment_FileWithOverrides(t *testing.T) {
	t.Setenv(EnvEnvironmentFile, "")
	path := writeFile(t, "environment.yaml",
		"base: /stratio/gitops\nrepos:\n  charts: /stratio/charts/charts\n  keos-fleet: /wt/fleet\ncluster: eosdev\ntenant: stratio\n")

	env, err := LoadEnvironment(path, Environment{Tenant: "acme", Repos: map[string]string{RepoFleet: "/wt/fleet-2"}})
	if err != nil {
		t.Fatalf("LoadEnvironment returned error: %v", err)
	}
	if env.Cluster != "eosdev" || env.Tenant != "acme" {
		t.Errorf("env = %+v, want cluster from the file and tenant from the overrides", env)
	}
	want := RepoPaths{
		Apps: "/stratio/gitops/keos-apps", UseCases: "/stratio/gitops/keos-use-cases", Fleet: "/wt/fleet-2",
		SystemServices: "/stratio/gitops/keos-system-services", Charts: "/stratio/charts/charts",
	}
	if got := env.RepoPaths(); got != want {
		t.Errorf("RepoPaths() = %+v, want %+v", got, want)
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
	if env.Repo(RepoCharts) != "/b/charts" {
		t.Errorf("Repo(charts) = %q, want <base>/charts when repos.charts is unset", env.Repo(RepoCharts))
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

// Every repository pointed at directly: no base needed.
func TestLoadEnvironment_ReposWithoutBase(t *testing.T) {
	path := writeFile(t, "environment.yaml", "repos:\n  keos-apps: /a\n  keos-use-cases: /u\n  keos-fleet: /f\n"+
		"  keos-system-services: /s\n  charts: /c\ncluster: c\ntenant: t\n")
	env, err := LoadEnvironment(path, Environment{})
	if err != nil {
		t.Fatalf("LoadEnvironment returned error: %v", err)
	}
	if got := env.RepoPaths(); got != (RepoPaths{Apps: "/a", UseCases: "/u", Fleet: "/f", SystemServices: "/s", Charts: "/c"}) {
		t.Errorf("RepoPaths() = %+v", got)
	}
}

func TestLoadEnvironment_NoBaseNamesUnsetRepos(t *testing.T) {
	path := writeFile(t, "environment.yaml", "repos:\n  charts: /c\ncluster: c\ntenant: t\n")
	_, err := LoadEnvironment(path, Environment{})
	if err == nil || !strings.Contains(err.Error(), "keos-apps") || strings.Contains(err.Error(), "charts,") {
		t.Errorf("LoadEnvironment error = %v, want it to name the unset repos (not charts)", err)
	}
}

func TestLoadEnvironment_UnknownRepoRejected(t *testing.T) {
	path := writeFile(t, "environment.yaml", "base: /b\nrepos:\n  keos-appz: /x\ncluster: c\ntenant: t\n")
	if _, err := LoadEnvironment(path, Environment{}); err == nil || !strings.Contains(err.Error(), "keos-appz") {
		t.Errorf("LoadEnvironment error = %v, want it to name the unknown repo", err)
	}
}

// chartsBase was replaced by repos.charts: an old file fails saying what
// to set instead.
func TestLoadEnvironment_ChartsBaseRejectedWithGuidance(t *testing.T) {
	path := writeFile(t, "environment.yaml", "base: /b\nchartsBase: /stratio/charts\ncluster: c\ntenant: t\n")
	_, err := LoadEnvironment(path, Environment{})
	if err == nil || !strings.Contains(err.Error(), "repos.charts") || !strings.Contains(err.Error(), "/stratio/charts/charts") {
		t.Errorf("LoadEnvironment error = %v, want it to point at repos.charts with the old value's charts dir", err)
	}
}

// TestLoadEnvironment_PathsResolveAgainstTheFileAndExpandHome: a relative
// path in the file is relative to the file, never to the working
// directory of a later command; a relative flag is relative to the
// working directory; ~ is the home directory in both.
func TestLoadEnvironment_PathsResolveAgainstTheFileAndExpandHome(t *testing.T) {
	t.Setenv(EnvEnvironmentFile, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeFile(t, "environment.yaml",
		"base: gitops\nrepos:\n  charts: ~/charts\n  keos-fleet: ../fleet\ncluster: eosdev\ntenant: stratio\n")
	dir := filepath.Dir(path)

	env, err := LoadEnvironment(path, Environment{Repos: map[string]string{RepoApps: "apps-wt"}})
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	got := env.RepoPaths()
	want := RepoPaths{
		Apps:           filepath.Join(cwd, "apps-wt"),
		UseCases:       filepath.Join(dir, "gitops", "keos-use-cases"),
		Fleet:          filepath.Join(filepath.Dir(dir), "fleet"),
		SystemServices: filepath.Join(dir, "gitops", "keos-system-services"),
		Charts:         filepath.Join(home, "charts"),
	}
	if got != want {
		t.Errorf("RepoPaths() = %+v\nwant %+v", got, want)
	}
}

func TestParseEnvironmentRepos_RelativeDirStillGivesAbsolutePaths(t *testing.T) {
	t.Chdir(t.TempDir())
	got, err := ParseEnvironmentRepos([]byte("repos:\n  charts: ../charts\n"), "cfg")
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs("charts")
	if err != nil {
		t.Fatal(err)
	}
	if got["charts"] != want || !filepath.IsAbs(got["charts"]) {
		t.Errorf("charts = %q, want the absolute %q (cfg/../charts)", got["charts"], want)
	}
}
