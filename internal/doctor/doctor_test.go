package doctor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/reporequire"
	"github.com/Stratio/flux-stratio/internal/runner"
)

// fixtureBase creates a temp directory with the four sibling repo checkouts
// and, when tenant is non-empty, a tenant RSIP file at the expected path.
func fixtureBase(t *testing.T, cluster, tenant string) string {
	t.Helper()
	base := t.TempDir()
	for _, dir := range reporequire.Dirs {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if tenant != "" {
		tenantDir := filepath.Join(base, "keos-fleet", "clusters", cluster, "tenants", "config")
		if err := os.MkdirAll(tenantDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tenantDir, tenant+".yaml"), []byte("spec: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

func writeConfig(t *testing.T, base, cluster, tenant string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flux-stratio.yaml")
	body := "base: " + base + "\ncluster: " + cluster + "\ntenant: " + tenant + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func passingRunner() *runner.Fake {
	return &runner.Fake{Responses: map[string]runner.FakeResponse{
		"flux-operator": {Stdout: []byte("flux-operator version 0.45.1\n")},
		"flux":          {Stdout: []byte("flux version 2.9.2\n")},
		"helm":          {Stdout: []byte("v3.16.2\n")},
		"kubectl":       {Stdout: []byte("Client Version: v1.34.2\n")},
	}}
}

func fakeClientFactory(objs ...client.Object) func(*genericclioptions.ConfigFlags) (client.Client, error) {
	return func(*genericclioptions.ConfigFlags) (client.Client, error) {
		return fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithObjects(objs...).Build(), nil
	}
}

func baseOptions() Options {
	return Options{
		KubeconfigArgs: genericclioptions.NewConfigFlags(false),
		Runner:         passingRunner(),
		NewClient:      fakeClientFactory(),
		Log:            log.New(io.Discard, false),
	}
}

func TestRun_AllChecksPass(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	opts := baseOptions()
	opts.ConfigFlag = writeConfig(t, base, "eosdev", "stratio")

	report := Run(context.Background(), opts)

	if !report.OK() {
		t.Fatalf("report.OK() = false, want true; error: %v", report.Err())
	}
	if len(report.Checks) != 5 {
		t.Errorf("len(Checks) = %d, want 5", len(report.Checks))
	}
}

func TestRun_MissingBinaries_OtherChecksStillRun(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	opts := baseOptions()
	opts.Runner = &runner.Fake{} // every binary unconfigured -> Fake errors for all
	opts.ConfigFlag = writeConfig(t, base, "eosdev", "stratio")

	report := Run(context.Background(), opts)

	if report.OK() {
		t.Fatal("report.OK() = true, want false")
	}
	if len(report.Checks) != 5 {
		t.Fatalf("len(Checks) = %d, want 5 (downstream checks must still run)", len(report.Checks))
	}
	if report.Checks[0].Name != CheckBinaries || report.Checks[0].OK {
		t.Errorf("Checks[0] = %+v, want a failing binaries check", report.Checks[0])
	}
	for _, c := range report.Checks[1:] {
		if !c.OK {
			t.Errorf("%s: OK = false, want true (only binaries should fail here); detail: %s", c.Name, c.Detail)
		}
	}
}

func TestRun_InvalidConfig_SkipsDownstreamChecks(t *testing.T) {
	opts := baseOptions()
	opts.ConfigFlag = filepath.Join(t.TempDir(), "does-not-exist.yaml")

	report := Run(context.Background(), opts)

	if report.OK() {
		t.Fatal("report.OK() = true, want false")
	}
	if len(report.Checks) != 2 {
		t.Fatalf("len(Checks) = %d, want 2 (binaries + config only, no cfg to check further)", len(report.Checks))
	}
	if report.Checks[1].Name != CheckConfig || report.Checks[1].OK {
		t.Errorf("Checks[1] = %+v, want a failing config check", report.Checks[1])
	}
}

func TestRun_RepoLayoutMissingDirs(t *testing.T) {
	base := t.TempDir() // no keos-* subdirectories created
	opts := baseOptions()
	opts.ConfigFlag = writeConfig(t, base, "eosdev", "stratio")

	report := Run(context.Background(), opts)

	if report.OK() {
		t.Fatal("report.OK() = true, want false")
	}
	var repoCheck Check
	for _, c := range report.Checks {
		if c.Name == CheckRepoLayout {
			repoCheck = c
		}
	}
	if repoCheck.OK {
		t.Error("repo layout check passed, want it to fail")
	}
	for _, want := range reporequire.Dirs {
		if !strings.Contains(repoCheck.Detail, want) {
			t.Errorf("repo layout detail %q should mention missing dir %q", repoCheck.Detail, want)
		}
	}
}

func TestRun_ClusterUnreachable(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	opts := baseOptions()
	opts.NewClient = func(*genericclioptions.ConfigFlags) (client.Client, error) {
		return nil, errors.New("connection refused")
	}
	opts.ConfigFlag = writeConfig(t, base, "eosdev", "stratio")

	report := Run(context.Background(), opts)

	if report.OK() {
		t.Fatal("report.OK() = true, want false")
	}
	var clusterCheck Check
	for _, c := range report.Checks {
		if c.Name == CheckCluster {
			clusterCheck = c
		}
	}
	if clusterCheck.OK || !strings.Contains(clusterCheck.Detail, "connection refused") {
		t.Errorf("cluster check = %+v, want a failure mentioning the connection error", clusterCheck)
	}
}

func TestRun_TenantFileMissing(t *testing.T) {
	base := fixtureBase(t, "eosdev", "" /* no tenant file written */)
	opts := baseOptions()
	opts.ConfigFlag = writeConfig(t, base, "eosdev", "stratio")

	report := Run(context.Background(), opts)

	if report.OK() {
		t.Fatal("report.OK() = true, want false")
	}
	var tenantCheck Check
	for _, c := range report.Checks {
		if c.Name == CheckTenant {
			tenantCheck = c
		}
	}
	if tenantCheck.OK || !strings.Contains(tenantCheck.Detail, "tenant import") {
		t.Errorf("tenant check = %+v, want a failure suggesting `tenant import`", tenantCheck)
	}
}

func TestRun_OverridesTakePrecedenceOverConfig(t *testing.T) {
	// Config points at a bogus base/cluster/tenant; the CLI overrides point
	// at the real fixture. Every downstream check must use the overrides.
	base := fixtureBase(t, "real-cluster", "real-tenant")
	opts := baseOptions()
	opts.ConfigFlag = writeConfig(t, "/does/not/exist", "bogus-cluster", "bogus-tenant")
	opts.BaseOverride = base
	opts.ClusterOverride = "real-cluster"
	opts.TenantOverride = "real-tenant"

	report := Run(context.Background(), opts)

	if !report.OK() {
		t.Fatalf("report.OK() = false, want true; error: %v", report.Err())
	}
}

func TestReport_Err_AggregatesFailingChecks(t *testing.T) {
	r := Report{Checks: []Check{
		{Name: CheckBinaries, OK: true},
		{Name: CheckConfig, OK: false, Detail: "boom"},
		{Name: CheckCluster, OK: false, Detail: "unreachable"},
	}}
	err := r.Err()
	if err == nil {
		t.Fatal("Err() = nil, want non-nil")
	}
	for _, want := range []string{"config", "boom", "cluster access", "unreachable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Err() = %q, missing %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "binaries") {
		t.Errorf("Err() = %q, should not mention the passing binaries check", err)
	}
}

func TestReport_OK_NoChecksIsVacuouslyOK(t *testing.T) {
	var r Report
	if !r.OK() || r.Err() != nil {
		t.Error("an empty Report should be OK with a nil Err")
	}
}

func TestRun_NarratesThroughLog(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	var buf bytes.Buffer
	opts := baseOptions()
	opts.Log = log.New(&buf, false)
	opts.ConfigFlag = writeConfig(t, base, "eosdev", "stratio")

	Run(context.Background(), opts)

	out := buf.String()
	for _, want := range []string{"checking required binaries", "loading config", "checking cluster access"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q; got:\n%s", want, out)
		}
	}
}
