package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/discovery"
	"github.com/Stratio/flux-stratio/internal/log"
)

var deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

// gosecAgentApp is the classified instance internal/components produces
// for a legacy "psql-agent" Deployment migrating into psql-gosec-agent.
func gosecAgentApp() config.App {
	return config.App{
		ID: "psql-gosec-agent", Name: "Postgres gosec agent psql-gosec-agent", Type: "postgres-gosec-agent",
		Rset:          "apps/components/resourceset-apps-datastores.yaml",
		Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent", Entry: "psql",
		ChartPath: "gosec-agent",
		Live:      []config.ObjectRef{{GVK: deploymentGVK, Namespace: "stratio-datastores", Name: "psql-agent"}},
	}
}

func TestDiscoveredApps_CatalogAppsKeptAndTheirLiveNamesCovered(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("apps/v1", "Deployment", "stratio-datastores", "psql-agent", nil),
		obj("apps/v1", "Deployment", "keos-core", "capsule", nil),
	).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	apps := DiscoveredApps([]config.App{gosecAgentApp()}, idx)
	var ids []string
	for _, a := range apps {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	if want := []string{"capsule", "psql-gosec-agent"}; !equalStrings(ids, want) {
		t.Fatalf("IDs = %v, want %v (psql-agent is covered by the catalog app, not captured twice)", ids, want)
	}

	for _, a := range apps {
		switch a.ID {
		case "psql-gosec-agent":
			if !isCatalogApp(a) || a.ChartPath != "gosec-agent" {
				t.Errorf("psql-gosec-agent lost its catalog facts: %+v", a)
			}
		case "capsule":
			if a.ChartPath != "" || isCatalogApp(a) || a.Object != "capsule" {
				t.Errorf("capsule should be synthetic: %+v", a)
			}
		}
	}
}

// TestDiscoveredApps_SameNameUnrelatedObjectStillCapturedSeparately is the
// regression test for the name-collision bug the catalog's selectors fix:
// a "genai" PgDatabase isn't the genai chart app (anchored on genai-api),
// so it gets its own synthetic App — and, sharing the catalog app's ID, a
// disambiguated one — rather than being captured *as* the genai app.
func TestDiscoveredApps_SameNameUnrelatedObjectStillCapturedSeparately(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("apps/v1", "Deployment", "stratio-genai", "genai-api", nil),
		obj("postgres.stratio.com/v1", "PgDatabase", "stratio-datastores", "genai", nil),
	).Build()
	pgdb := schema.GroupVersionKind{Group: "postgres.stratio.com", Version: "v1", Kind: "PgDatabase"}
	idx, err := discovery.Scan(context.Background(), c, logger, pgdb)
	if err != nil {
		t.Fatal(err)
	}

	genai := config.App{
		ID: "genai", Type: "genai", Object: "genai", ChartPath: "genai",
		Live: []config.ObjectRef{{GVK: deploymentGVK, Namespace: "stratio-genai", Name: "genai-api"}},
	}
	apps := DiscoveredApps([]config.App{genai}, idx)

	ids := map[string]config.App{}
	for _, a := range apps {
		ids[a.ID] = a
	}
	if len(apps) != 2 || len(ids) != 2 {
		t.Fatalf("apps = %+v, want the catalog genai app plus one disambiguated synthetic", apps)
	}
	if synthetic, ok := ids["genai-live"]; !ok || isCatalogApp(synthetic) || synthetic.Object != "genai" {
		t.Errorf("the PgDatabase should be a separate, disambiguated synthetic app: %+v", ids)
	}
}

func TestDiscoveredApps_Deduplicated(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("helm.toolkit.fluxcd.io/v2", "HelmRelease", "ns", "shared", nil),
		obj("apps/v1", "Deployment", "ns", "shared", nil),
	).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	apps := DiscoveredApps(nil, idx)
	if len(apps) != 1 {
		t.Fatalf("DiscoveredApps returned %d apps, want 1 (same name across kinds must not duplicate)", len(apps))
	}
}

// TestDiscoveredApps_CoverageIsPerObject: a PgDatabase named like the
// Deployment a catalog app anchors on (rocket, discovery, intelligence...)
// is not covered by it, and a same-named object in a second namespace (the
// keos-core opensearch1 next to stratio-datastores') is captured on its own.
func TestDiscoveredApps_CoverageIsPerObject(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("apps/v1", "Deployment", "stratio-rocket", "rocket", nil),
		obj("postgres.stratio.com/v1", "PgDatabase", "stratio-datastores", "rocket", nil),
		obj("apps/v1", "Deployment", "stratio-datastores", "opensearch1-agent", nil),
		obj("apps/v1", "Deployment", "keos-core", "opensearch1-agent", nil),
	).Build()
	pgdb := schema.GroupVersionKind{Group: "postgres.stratio.com", Version: "v1", Kind: "PgDatabase"}
	idx, err := discovery.Scan(context.Background(), c, logger, pgdb)
	if err != nil {
		t.Fatal(err)
	}
	catalog := []config.App{
		{ID: "rocket", Type: "rocket", Object: "rocket", Live: []config.ObjectRef{{GVK: deploymentGVK, Namespace: "stratio-rocket", Name: "rocket"}}},
		{ID: "opensearch1-gosec-agent", Type: "opensearch-gosec-agent", Object: "opensearch1-gosec-agent",
			Live: []config.ObjectRef{{GVK: deploymentGVK, Namespace: "stratio-datastores", Name: "opensearch1-agent"}}},
	}

	got := map[string]config.App{}
	for _, a := range DiscoveredApps(catalog, idx) {
		got[a.ID] = a
	}
	if len(got) != 4 {
		t.Fatalf("apps = %v, want the 2 catalog apps plus the PgDatabase and the keos-core agent", got)
	}
	pg, ok := got["rocket-live"]
	if !ok || len(pg.Live) != 1 || pg.Live[0].GVK.Kind != "PgDatabase" || pg.Live[0].Namespace != "stratio-datastores" {
		t.Errorf("rocket-live = %+v, want the PgDatabase pinned as its live object", pg)
	}
	other, ok := got["opensearch1-agent"]
	if !ok || len(other.Live) != 1 || other.Live[0].Namespace != "keos-core" {
		t.Errorf("opensearch1-agent = %+v, want the keos-core one pinned", other)
	}
}

// TestDiscoveredApps_SameNameInTwoNamespacesGetsDistinctIDs: with no catalog
// app involved, two uncovered objects of one name must not share a backup
// directory.
func TestDiscoveredApps_SameNameInTwoNamespacesGetsDistinctIDs(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("apps/v1", "Deployment", "a", "dup", nil),
		obj("apps/v1", "Deployment", "b", "dup", nil),
	).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range DiscoveredApps(nil, idx) {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	if want := []string{"dup", "dup-b"}; !equalStrings(ids, want) {
		t.Errorf("IDs = %v, want %v", ids, want)
	}
}

// TestDiscoveredApps_FirstObjectKeepsTheBareName: a CR and the workload that
// share a name used to be one capture, the CR (the legacy cascade's first
// choice) under the bare name; it keeps that ID, so its earlier backups are
// still found, and the workload gets its own.
func TestDiscoveredApps_FirstObjectKeepsTheBareName(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("apps/v1", "StatefulSet", "keos-core", "postgreskeos", nil),
		obj("postgres.stratio.com/v1", "PgCluster", "keos-core", "postgreskeos", nil),
	).Build()
	pgc := schema.GroupVersionKind{Group: "postgres.stratio.com", Version: "v1", Kind: "PgCluster"}
	idx, err := discovery.Scan(context.Background(), c, logger, pgc)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range DiscoveredApps(nil, idx) {
		got[a.ID] = a.Live[0].GVK.Kind
	}
	if want := map[string]string{"postgreskeos": "PgCluster", "postgreskeos-keos-core": "StatefulSet"}; !reflect.DeepEqual(got, want) {
		t.Errorf("IDs = %v, want %v", got, want)
	}
}

// TestDiscoveredApps_KustomizationOnlyNameKept: a name only a Kustomization
// carries still yields a (Live-less) app, so Run can report it has nothing
// to back up; a Kustomization sharing its name with a workload yields none.
func TestDiscoveredApps_KustomizationOnlyNameKept(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "only-ks", nil),
		obj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "both", nil),
		obj("apps/v1", "Deployment", "ns", "both", nil),
	).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]config.App{}
	for _, a := range DiscoveredApps(nil, idx) {
		got[a.ID] = a
	}
	if len(got) != 2 {
		t.Fatalf("apps = %v, want only-ks and both", got)
	}
	if len(got["only-ks"].Live) != 0 {
		t.Errorf("only-ks = %+v, want no pinned live object (the cascade reports it)", got["only-ks"])
	}
	if live := got["both"].Live; len(live) != 1 || live[0].GVK.Kind != "Deployment" {
		t.Errorf("both = %+v, want the Deployment", got["both"])
	}
}

// TestRun_ClassifiedAppCapturesItsExactLiveObject: a catalog instance is
// captured from the exact object it was classified from, never the
// name-only cascade — whose manifest-mode order (CR first) would pick the
// same-named "rocket" PgDatabase over the Deployment the instance
// actually is.
func TestRun_ClassifiedAppCapturesItsExactLiveObject(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		deploymentWithEnv("rocket", "stratio-rocket", "INFO"),
		obj("postgres.stratio.com/v1", "PgDatabase", "stratio-datastores", "rocket", nil),
	).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	app := config.App{
		ID: "rocket", Type: "rocket", Object: "rocket",
		Live: []config.ObjectRef{{GVK: deploymentGVK, Namespace: "stratio-rocket", Name: "rocket"}},
	}
	result, err := Run(context.Background(), Options{
		Repos: config.ReposUnder(fixtureBase(t)), App: app, Index: idx, Client: c, Dir: t.TempDir(), Clock: fixedClock, Log: logger,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !equalStrings(result.Files, []string{"deployment.yaml", "env-vars.env", "env-vars.deployment.rocket.env"}) {
		t.Errorf("Files = %v, want the Deployment's capture, not the PgDatabase's cr.yaml", result.Files)
	}
}

// TestRun_ClassifiedChartAppCapturesItsSiblings covers a legacy CCT genai:
// no HelmRelease to enumerate the chart's workloads by, so the siblings
// classified with it (genai-ui) are captured with the genai-api anchor,
// each into its own env file. A classified sibling no longer live
// (genai-developer-proxy) is skipped, and no synthetic app captures
// genai-ui a second time.
func TestRun_ClassifiedChartAppCapturesItsSiblings(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	api := deploymentWithEnv("genai-api", "stratio-genai", "legacy-api-role")
	ui := deploymentWithEnv("genai-ui", "stratio-genai", "legacy-ui-role")
	api.Spec.Template.Spec.Containers[0].Env[0].Name = "VAULT_ROLE"
	ui.Spec.Template.Spec.Containers[0].Env[0].Name = "VAULT_ROLE"
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(api, ui).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	app := config.App{
		ID: "genai", Type: "genai", Object: "genai", ChartPath: "genai",
		Live: []config.ObjectRef{
			{GVK: deploymentGVK, Namespace: "stratio-genai", Name: "genai-api"},
			{GVK: deploymentGVK, Namespace: "stratio-genai", Name: "genai-developer-proxy"},
			{GVK: deploymentGVK, Namespace: "stratio-genai", Name: "genai-ui"},
		},
	}
	result, err := Run(context.Background(), Options{
		Repos: config.ReposUnder(fixtureBase(t)), App: app, Index: idx, Client: c, Dir: t.TempDir(), Clock: fixedClock, Log: logger,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	wantFiles := []string{"deployment.yaml", "workload.deployment.genai-ui.yaml", "env-vars.env", "env-vars.deployment.genai-api.env", "env-vars.deployment.genai-ui.env"}
	if !equalStrings(result.Files, wantFiles) {
		t.Errorf("Files = %v, want %v", result.Files, wantFiles)
	}
	for file, want := range map[string]string{
		"env-vars.deployment.genai-api.env": "VAULT_ROLE=legacy-api-role\n",
		"env-vars.deployment.genai-ui.env":  "VAULT_ROLE=legacy-ui-role\n",
	} {
		data, err := os.ReadFile(filepath.Join(result.Dir, file))
		if err != nil {
			t.Fatalf("%s not written: %v", file, err)
		}
		if string(data) != want {
			t.Errorf("%s = %q, want %q", file, data, want)
		}
	}
	depData, err := os.ReadFile(filepath.Join(result.Dir, "deployment.yaml"))
	if err != nil || !strings.Contains(string(depData), "name: genai-api") {
		t.Errorf("deployment.yaml = %q (err %v), want the anchor's manifest", depData, err)
	}

	for _, a := range DiscoveredApps([]config.App{app}, idx) {
		if a.ID == "genai-ui" {
			t.Errorf("DiscoveredApps = %+v: genai-ui is captured with genai, not as an app of its own", a)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRun_EnvResolutionWarningsAreShown: a variable whose ConfigMap can't be
// read is captured as <unresolved:...>, and the capture says so — a later
// --baseline can't carry a variable the backup silently lost.
func TestRun_EnvResolutionWarningsAreShown(t *testing.T) {
	var out bytes.Buffer
	logger := log.New(&out, false)
	wl := deploymentWithEnv("app", "ns", "x")
	wl.Spec.Template.Spec.Containers[0].Env = append(wl.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{
		Name: "FROM_CM",
		ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "missing-cm"}, Key: "k",
		}},
	})
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(wl).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}
	app := config.App{ID: "app", Object: "app", Live: []config.ObjectRef{{GVK: deploymentGVK, Namespace: "ns", Name: "app"}}}
	if _, err := Run(context.Background(), Options{
		Repos: config.ReposUnder(fixtureBase(t)), App: app, Index: idx, Client: c, Dir: t.TempDir(), Clock: fixedClock, Log: logger,
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(out.String(), "missing-cm") {
		t.Errorf("the unresolvable ConfigMap isn't mentioned in the output: %q", out.String())
	}
}

// TestRun_TwoCapturesWithinOneSecondDontCollide: the timestamp has
// one-second resolution; a second capture of an app in the same second (the
// backup before a prepare step, right after a backup) gets the next free
// second instead of failing to rename onto the first.
func TestRun_TwoCapturesWithinOneSecondDontCollide(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(deploymentWithEnv("app", "ns", "INFO")).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}
	app := config.App{ID: "app", Object: "app", Live: []config.ObjectRef{{GVK: deploymentGVK, Namespace: "ns", Name: "app"}}}
	opts := Options{Repos: config.ReposUnder(fixtureBase(t)), App: app, Index: idx, Client: c, Dir: t.TempDir(), Clock: fixedClock, Log: logger}

	first, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("second capture in the same second failed: %v", err)
	}
	if first.Dir == second.Dir || second.Dir == "" {
		t.Errorf("dirs %q and %q: want two distinct captures", first.Dir, second.Dir)
	}
	if latest, err := ResolveBaseline(opts.Dir, "app"); err != nil || latest != second.Dir {
		t.Errorf("latest = %q (err %v), want the second capture %q", latest, err, second.Dir)
	}
}
