package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Stratio/flux-stratio/internal/config"
)

// runInit runs config init into dir with the given root flags.
func runInit(t *testing.T, dir, base string, repos map[string]string, force bool) error {
	t.Helper()
	baseFlag, clusterFlag, tenantFlag, repoFlag = base, "eosdev", "stratio", repos
	t.Cleanup(func() { baseFlag, clusterFlag, tenantFlag, repoFlag = "", "", "", nil })
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	return runConfigInit(cmd, dir, force)
}

// TestConfigInit_WritesAbsolutePaths: a relative --base is written as the
// absolute path it meant when given.
func TestConfigInit_WritesAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(t, dir, "relative-base", nil, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, config.EnvironmentFile))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs("relative-base")
	if !strings.Contains(string(data), "base: "+want) {
		t.Errorf("environment file lacks the absolute base %s:\n%s", want, data)
	}
}

// TestConfigInit_ForceBacksUpAndKeepsRepos: --force copies each file it
// replaces aside, and keeps a repos entry no --repo replaces.
func TestConfigInit_ForceBacksUpAndKeepsRepos(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(t, dir, "/gitops", map[string]string{"charts": "/wt/charts", "keos-fleet": "/wt/fleet"}, false); err != nil {
		t.Fatal(err)
	}
	if err := runInit(t, dir, "/gitops", nil, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second init without --force: err = %v, want it refused", err)
	}
	if err := runInit(t, dir, "/gitops2", map[string]string{"keos-fleet": "/wt/fleet-2"}, true); err != nil {
		t.Fatal(err)
	}

	env, err := config.LoadEnvironment(filepath.Join(dir, config.EnvironmentFile), config.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	if got := env.RepoPaths(); got.Charts != "/wt/charts" || got.Fleet != "/wt/fleet-2" || got.Apps != "/gitops2/keos-apps" {
		t.Errorf("RepoPaths() = %+v, want charts kept, keos-fleet replaced, base updated", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	backups := 0
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak-") {
			backups++
		}
	}
	if backups != 2 {
		t.Errorf("backups = %d, want one per replaced file: %v", backups, entries)
	}
}
