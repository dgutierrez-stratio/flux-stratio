package backup

import (
	"bytes"
	"context"
	"sort"
	"testing"

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
	if !equalStrings(result.Files, []string{"deployment.yaml", "env-vars.env"}) {
		t.Errorf("Files = %v, want the Deployment's capture, not the PgDatabase's cr.yaml", result.Files)
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
