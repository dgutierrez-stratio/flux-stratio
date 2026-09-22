package appdiff

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
)

func TestDiff_ManifestMode_Baseline(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}

	baseline := t.TempDir()
	cr := "apiVersion: postgres.stratio.com/v1\nkind: PgCluster\nmetadata:\n  name: psql\n  namespace: stratio-datastores\nspec:\n  instances: 7\n"
	if err := os.WriteFile(filepath.Join(baseline, "cr.yaml"), []byte(cr), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Base: base, Cluster: "eosdev", Tenant: "stratio",
		App:      config.App{ID: "psql", Rset: "apps/components/resourceset-apps-datastores.yaml", Kustomization: "apps-psql", Object: "psql"},
		Baseline: baseline,
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Client: fake.NewClientBuilder().WithScheme(mustScheme(t)).Build(), // never touched: baseline mode reads files, not the cluster
		Log:    log.New(io.Discard, false),
	}

	result, err := Diff(context.Background(), opts)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if result.Patch == nil {
		t.Fatal("Patch = nil, want a patch for the changed instances count (baseline had 7, rendered has 1)")
	}
}

func TestDiff_ManifestMode_BaselineFileMissing(t *testing.T) {
	base := fixtureBase(t)
	if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "postgres", "app", "overlays", "S"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Base: base, Cluster: "eosdev", Tenant: "stratio",
		App:      config.App{ID: "psql", Rset: "apps/components/resourceset-apps-datastores.yaml", Kustomization: "apps-psql", Object: "psql"},
		Baseline: t.TempDir(), // empty: no cr.yaml
		Runner: &runner.Fake{Responses: map[string]runner.FakeResponse{
			"flux-operator": {Stdout: []byte(rsetOutputPgCluster)},
			"flux":          {Stdout: []byte(kustomizationBuildOutputPgCluster)},
		}},
		Log: log.New(io.Discard, false),
	}
	if _, err := Diff(context.Background(), opts); err == nil {
		t.Fatal("Diff with a missing baseline cr.yaml: got nil error, want non-nil")
	}
}

func TestReadBaselineEnvFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "env-vars.env"), []byte("A=1\nB=hello world\n\nMALFORMED_LINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readBaselineEnvFile(dir, "env-vars.env")
	if err != nil {
		t.Fatalf("readBaselineEnvFile returned error: %v", err)
	}
	if got["A"] != "1" || got["B"] != "hello world" {
		t.Errorf("got = %v", got)
	}
	if _, ok := got["MALFORMED_LINE"]; ok {
		t.Error("a line with no '=' should be skipped, not produce a spurious key")
	}
}

func TestReadBaselineEnvFile_MissingFile(t *testing.T) {
	if _, err := readBaselineEnvFile(t.TempDir(), "env-vars.env"); err == nil {
		t.Fatal("readBaselineEnvFile on a missing file: got nil error, want non-nil")
	}
}

func TestReadBaselineYAML_MissingFile(t *testing.T) {
	if _, err := readBaselineYAML(t.TempDir(), "cr.yaml"); err == nil {
		t.Fatal("readBaselineYAML on a missing file: got nil error, want non-nil")
	}
}
