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

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/reporequire"
	"github.com/Stratio/flux-stratio/internal/runner"
)

// fixtureTemplate is a minimal keos-use-cases ResourceSet template
// declaring one component key, "postgres" — enough for checkTypes.
const fixtureTemplate = `spec:
  resourcesTemplate: |
    <<- range $component := $postgres >>
    ---
    apiVersion: kustomize.toolkit.fluxcd.io/v1
    kind: Kustomization
    metadata:
      name: apps-<< get $component "name" >>
    <<- end >>
`

// fixtureBase creates a temp directory with the four sibling repo
// checkouts (keos-use-cases carrying fixtureTemplate) and, when tenant is
// non-empty, a tenant RSIP file at the expected path.
func fixtureBase(t *testing.T, cluster, tenant string) string {
	t.Helper()
	base := t.TempDir()
	for _, dir := range reporequire.Dirs {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	componentsDir := filepath.Join(base, "keos-use-cases", "apps", "components")
	if err := os.MkdirAll(componentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(componentsDir, "resourceset-apps-fixture.yaml"), []byte(fixtureTemplate), 0o644); err != nil {
		t.Fatal(err)
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

// setConfig writes a one-type catalog (chart-mode when chartPath is set)
// and an environment file, pointing opts at both.
func setConfig(t *testing.T, opts *Options, base, cluster, tenant, chartsBase, chartPath string) {
	t.Helper()
	dir := t.TempDir()

	catalogBody := "types:\n" +
		"  - type: postgres\n" +
		"    name: Postgres\n" +
		"    component: postgres\n" +
		"    rset: apps/components/resourceset-apps-fixture.yaml\n" +
		"    match:\n" +
		"      kinds: [postgres.stratio.com/v1/PgCluster]\n"
	if chartPath != "" {
		catalogBody += "    chart:\n      path: " + chartPath + "\n"
	}
	opts.ConfigFlag = filepath.Join(dir, "catalog.yaml")
	if err := os.WriteFile(opts.ConfigFlag, []byte(catalogBody), 0o644); err != nil {
		t.Fatal(err)
	}

	envBody := "base: " + base + "\ncluster: " + cluster + "\ntenant: " + tenant + "\n"
	if chartsBase != "" {
		envBody += "chartsBase: " + chartsBase + "\n"
	}
	opts.EnvConfigFlag = filepath.Join(dir, "environment.yaml")
	if err := os.WriteFile(opts.EnvConfigFlag, []byte(envBody), 0o644); err != nil {
		t.Fatal(err)
	}
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
	setConfig(t, &opts, base, "eosdev", "stratio", "", "")

	report := Run(context.Background(), opts)

	if !report.OK() {
		t.Fatalf("report.OK() = false, want true; error: %v", report.Err())
	}
	if len(report.Checks) != 9 {
		t.Errorf("len(Checks) = %d, want 9", len(report.Checks))
	}
}

func TestRun_MissingBinaries_OtherChecksStillRun(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	opts := baseOptions()
	opts.Runner = &runner.Fake{} // every binary unconfigured -> Fake errors for all
	setConfig(t, &opts, base, "eosdev", "stratio", "", "")

	report := Run(context.Background(), opts)

	if report.OK() {
		t.Fatal("report.OK() = true, want false")
	}
	if len(report.Checks) != 9 {
		t.Fatalf("len(Checks) = %d, want 9 (downstream checks must still run)", len(report.Checks))
	}
	if report.Checks[0].Name != CheckBinaries || report.Checks[0].OK {
		t.Errorf("Checks[0] = %+v, want a failing binaries check", report.Checks[0])
	}
	for _, c := range report.Checks[1:] {
		if !c.OK && !c.Optional {
			t.Errorf("%s: OK = false, want true (only binaries should fail here); detail: %s", c.Name, c.Detail)
		}
	}
}

func TestRun_InvalidEnvironment_SkipsDownstreamChecks(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	opts := baseOptions()
	setConfig(t, &opts, base, "eosdev", "stratio", "", "")
	opts.EnvConfigFlag = filepath.Join(t.TempDir(), "does-not-exist.yaml")

	report := Run(context.Background(), opts)

	if report.OK() {
		t.Fatal("report.OK() = true, want false")
	}
	if len(report.Checks) != 4 {
		t.Fatalf("len(Checks) = %d, want 4 (binaries + meld + catalog + environment only)", len(report.Checks))
	}
	if report.Checks[3].Name != CheckEnvironment || report.Checks[3].OK {
		t.Errorf("Checks[3] = %+v, want a failing environment check", report.Checks[3])
	}
}

func TestRun_InvalidCatalog_SkipsCatalogDependentChecks(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	opts := baseOptions()
	setConfig(t, &opts, base, "eosdev", "stratio", "", "")
	opts.ConfigFlag = filepath.Join(t.TempDir(), "does-not-exist.yaml")

	report := Run(context.Background(), opts)

	if report.OK() {
		t.Fatal("report.OK() = true, want false")
	}
	var names []string
	for _, c := range report.Checks {
		names = append(names, string(c.Name))
	}
	want := "binaries,meld (optional),catalog,environment,repo layout,cluster access,tenant file"
	if strings.Join(names, ",") != want {
		t.Errorf("checks = %s, want %s", strings.Join(names, ","), want)
	}
}

func TestRun_UnknownComponentKeyOrPrepareStep(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	opts := baseOptions()
	setConfig(t, &opts, base, "eosdev", "stratio", "", "")
	body := "types:\n" +
		"  - type: kafka\n    name: Kafka\n    component: kafka\n    rset: r.yaml\n" +
		"    prepare: prepare-nothing\n    match:\n      kinds: [kafka.stratio.com/v1/KafkaCluster]\n"
	if err := os.WriteFile(opts.ConfigFlag, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	report := Run(context.Background(), opts)

	var typesCheck Check
	for _, c := range report.Checks {
		if c.Name == CheckTypes {
			typesCheck = c
		}
	}
	if typesCheck.OK || !strings.Contains(typesCheck.Detail, `component "kafka"`) || !strings.Contains(typesCheck.Detail, "prepare-nothing") {
		t.Errorf("catalog types check = %+v, want it to flag the unknown component and prepare step", typesCheck)
	}
}

func TestRun_RepoLayoutMissingDirs(t *testing.T) {
	base := t.TempDir() // no keos-* subdirectories created
	opts := baseOptions()
	setConfig(t, &opts, base, "eosdev", "stratio", "", "")

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

func TestRun_ChartPathMissing(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	opts := baseOptions()
	// No charts/virtualizer directory created under base.
	setConfig(t, &opts, base, "eosdev", "stratio", "", "charts/virtualizer")

	report := Run(context.Background(), opts)

	if report.OK() {
		t.Fatal("report.OK() = true, want false")
	}
	var chartCheck Check
	for _, c := range report.Checks {
		if c.Name == CheckChartPaths {
			chartCheck = c
		}
	}
	if chartCheck.OK {
		t.Error("chart paths check passed, want it to fail")
	}
	if !strings.Contains(chartCheck.Detail, "postgres") {
		t.Errorf("chart paths detail %q should mention the type missing its chart", chartCheck.Detail)
	}
}

func TestRun_ChartPathsUsesChartsBaseOverride(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	chartsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(chartsRoot, "charts", "virtualizer"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := baseOptions()
	// The chart lives only under chartsRoot, never under base — the check
	// must resolve against chartsBase, not base, once it's set.
	setConfig(t, &opts, base, "eosdev", "stratio", chartsRoot, "charts/virtualizer")

	report := Run(context.Background(), opts)

	if !report.OK() {
		t.Fatalf("report.OK() = false, want true; error: %v", report.Err())
	}
}

func TestRun_ClusterUnreachable(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	opts := baseOptions()
	opts.NewClient = func(*genericclioptions.ConfigFlags) (client.Client, error) {
		return nil, errors.New("connection refused")
	}
	setConfig(t, &opts, base, "eosdev", "stratio", "", "")

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
	setConfig(t, &opts, base, "eosdev", "stratio", "", "")

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
	setConfig(t, &opts, "/does/not/exist", "bogus-cluster", "bogus-tenant", "", "")
	opts.Overrides = config.Environment{Base: base, Cluster: "real-cluster", Tenant: "real-tenant"}

	report := Run(context.Background(), opts)

	if !report.OK() {
		t.Fatalf("report.OK() = false, want true; error: %v", report.Err())
	}
}

func TestReport_Err_AggregatesFailingChecks(t *testing.T) {
	r := Report{Checks: []Check{
		{Name: CheckBinaries, OK: true},
		{Name: CheckCatalog, OK: false, Detail: "boom"},
		{Name: CheckCluster, OK: false, Detail: "unreachable"},
	}}
	err := r.Err()
	if err == nil {
		t.Fatal("Err() = nil, want non-nil")
	}
	for _, want := range []string{"catalog", "boom", "cluster access", "unreachable"} {
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

func TestReport_OptionalFailure_NeverFailsTheReport(t *testing.T) {
	r := Report{Checks: []Check{
		{Name: CheckBinaries, OK: true},
		{Name: CheckMeld, OK: false, Optional: true, Detail: "not found"},
	}}
	if !r.OK() {
		t.Error("OK() = false, want true (an Optional failure must not fail the report)")
	}
	if err := r.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

func TestRun_NarratesThroughLog(t *testing.T) {
	base := fixtureBase(t, "eosdev", "stratio")
	var buf bytes.Buffer
	opts := baseOptions()
	opts.Log = log.New(&buf, false)
	setConfig(t, &opts, base, "eosdev", "stratio", "", "")

	Run(context.Background(), opts)

	out := buf.String()
	for _, want := range []string{"checking required binaries", "loading component catalog", "loading environment", "checking cluster access"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q; got:\n%s", want, out)
		}
	}
}
