package backup

import (
	"bytes"
	"context"
	"sort"
	"testing"

	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/discovery"
	"github.com/Stratio/flux-stratio/internal/log"
)

func TestDiscoveredApps_CatalogMatchRoutesToRealApp(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	// psql-gosec-agent's live name is "psql-agent" (Renamed), the same
	// convention liveName uses everywhere else.
	catalogApp := config.App{
		ID: "psql-gosec-agent", Name: "Postgres gosec agent",
		Rset:          "apps/components/resourceset-apps-datastores.yaml",
		Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent",
		ChartPath: "charts/gosec-agent", Renamed: "psql-agent",
	}
	cfg := &config.Config{Apps: []config.App{catalogApp}}

	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("helm.toolkit.fluxcd.io/v2", "HelmRelease", "stratio-datastores", "psql-agent", nil),
		obj("apps/v1", "Deployment", "keos-core", "capsule", nil),
	).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	apps := DiscoveredApps(cfg, idx)
	var names []string
	for _, a := range apps {
		names = append(names, a.ID)
	}
	sort.Strings(names)
	if want := []string{"capsule", "psql-gosec-agent"}; !equalStrings(names, want) {
		t.Fatalf("IDs = %v, want %v", names, want)
	}

	for _, a := range apps {
		switch a.ID {
		case "psql-gosec-agent":
			if a.ChartPath != "charts/gosec-agent" || a.Renamed != "psql-agent" {
				t.Errorf("psql-gosec-agent resolved to a non-catalog App: %+v", a)
			}
			if !isCatalogApp(a) {
				t.Errorf("isCatalogApp(psql-gosec-agent) = false, want true")
			}
		case "capsule":
			if a.ChartPath != "" || isCatalogApp(a) {
				t.Errorf("capsule should be synthetic: %+v", a)
			}
			if a.Object != "capsule" {
				t.Errorf("capsule Object = %q, want %q", a.Object, "capsule")
			}
		}
	}
}

// TestDiscoveredApps_RenamedAppsNewNameAlsoLive_IDsDisambiguated covers a
// mid-migration state: a Renamed catalog app's legacy live name AND its
// new/Object name (which is conventionally also the catalog App's own
// ID) are both live at once. Without disambiguation, the second identity
// would silently reuse the first's App.ID and both would be captured
// under the same backups/<id>/<timestamp>/ directory.
func TestDiscoveredApps_RenamedAppsNewNameAlsoLive_IDsDisambiguated(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	catalogApp := config.App{
		ID: "psql-gosec-agent", Name: "Postgres gosec agent",
		Rset:          "apps/components/resourceset-apps-datastores.yaml",
		Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent",
		ChartPath: "charts/gosec-agent", Renamed: "psql-agent",
	}
	cfg := &config.Config{Apps: []config.App{catalogApp}}

	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		// The legacy (pre-migration) live object, matched via Renamed.
		obj("helm.toolkit.fluxcd.io/v2", "HelmRelease", "stratio-datastores", "psql-agent", nil),
		// The app's own catalog ID/Object also live at once, mid-migration.
		obj("apps/v1", "Deployment", "stratio-datastores", "psql-gosec-agent", nil),
	).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	apps := DiscoveredApps(cfg, idx)
	if len(apps) != 2 {
		t.Fatalf("len(apps) = %d, want 2 (both live identities captured separately)", len(apps))
	}

	ids := map[string]config.App{}
	for _, a := range apps {
		ids[a.ID] = a
	}
	if len(ids) != 2 {
		t.Fatalf("apps share a colliding ID: %+v", apps)
	}
	real, ok := ids["psql-gosec-agent"]
	if !ok || !isCatalogApp(real) || real.ChartPath != "charts/gosec-agent" {
		t.Errorf("psql-gosec-agent should still resolve to the real catalog App: %+v", ids)
	}
	synthetic, ok := ids["psql-gosec-agent-live"]
	if !ok || isCatalogApp(synthetic) || synthetic.Object != "psql-gosec-agent" {
		t.Errorf("the colliding synthetic entry should be disambiguated: %+v", ids)
	}
}

func TestDiscoveredApps_Deduplicated(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	cfg := &config.Config{}
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("helm.toolkit.fluxcd.io/v2", "HelmRelease", "ns", "shared", nil),
		obj("apps/v1", "Deployment", "ns", "shared", nil),
	).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	apps := DiscoveredApps(cfg, idx)
	if len(apps) != 1 {
		t.Fatalf("DiscoveredApps returned %d apps, want 1 (same name across kinds must not duplicate)", len(apps))
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

func TestRun_SyntheticApp_NoMisleadingMismatchWarning(t *testing.T) {
	var logbuf bytes.Buffer
	logger := log.New(&logbuf, false)
	live := deploymentWithEnv("capsule", "keos-core", "INFO")

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(live).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	// The exact shape DiscoveredApps synthesizes for a non-catalog object.
	synthetic := config.App{ID: "capsule", Name: "capsule", Object: "capsule"}

	opts := Options{
		Base:  fixtureBase(t),
		App:   synthetic,
		Index: idx,
		Dir:   t.TempDir(),
		Clock: fixedClock,
		Log:   logger,
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.Files) != 2 {
		t.Fatalf("Files = %v, want deployment.yaml+env-vars.env", result.Files)
	}
	if bytes.Contains(logbuf.Bytes(), []byte("is configured as manifest-mode")) {
		t.Errorf("synthetic app should never trigger the catalog mismatch warning, got: %s", logbuf.String())
	}
}
