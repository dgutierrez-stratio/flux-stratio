package tenantimport

import (
	"context"
	"io"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
)

func seedConfig() *config.Catalog {
	cat := config.SeedCatalog()
	return &cat
}

// A catalog.yaml seeded before chart.enabledBy existed still derives it.
func TestScanEnabledBy_CatalogSeededBeforeTheFieldStillWorks(t *testing.T) {
	cat := config.SeedCatalog()
	for i := range cat.Types {
		if cat.Types[i].Chart != nil {
			chart := *cat.Types[i].Chart
			chart.EnabledBy = ""
			cat.Types[i].Chart = &chart
		}
	}
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(deployment("virtualizer-ui", "stratio-apps")).Build()
	live, err := scanEnabledBy(context.Background(), c, &cat, "stratio")
	if err != nil {
		t.Fatal(err)
	}
	if live.Values["virtualizer"]["uiEnabled"] != true {
		t.Errorf("uiEnabled = %v, want true (virtualizer-ui runs)", live.Values["virtualizer"]["uiEnabled"])
	}
}

func TestScanEnabledBy(t *testing.T) {
	owned := deployment("virtualizer-ui", "stratio-apps")
	owned.OwnerReferences = []metav1.OwnerReference{{Name: "operator"}}

	tests := []struct {
		name         string
		objects      []*appsv1.Deployment
		cfg          *config.Catalog
		wantEnabled  bool
		wantDerived  bool
		wantSiblings []string
	}{
		{name: "legacy or Helm-managed monitor", objects: []*appsv1.Deployment{deployment("virtualizer-monitor", "stratio-apps")}, cfg: seedConfig(), wantEnabled: true, wantDerived: true, wantSiblings: []string{"virtualizer-monitor"}},
		{name: "both siblings", objects: []*appsv1.Deployment{deployment("virtualizer-ui", "stratio-apps"), deployment("virtualizer-monitor", "stratio-apps")}, cfg: seedConfig(), wantEnabled: true, wantDerived: true, wantSiblings: []string{"virtualizer-monitor", "virtualizer-ui"}},
		{name: "only the server runs", objects: []*appsv1.Deployment{deployment("virtualizer", "stratio-apps")}, cfg: seedConfig(), wantDerived: true},
		{name: "a sibling of another tenant does not count", objects: []*appsv1.Deployment{deployment("virtualizer-ui", "other-apps")}, cfg: seedConfig(), wantDerived: true},
		{name: "an operator-owned Deployment is not a sibling", objects: []*appsv1.Deployment{owned}, cfg: seedConfig(), wantDerived: true},
		{name: "no component catalog derives nothing", objects: []*appsv1.Deployment{deployment("virtualizer-ui", "stratio-apps")}, cfg: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(mustScheme(t))
			for _, o := range tt.objects {
				b = b.WithObjects(o)
			}
			live, err := scanEnabledBy(context.Background(), b.Build(), tt.cfg, "stratio")
			if err != nil {
				t.Fatal(err)
			}
			got, derived := live.Values["virtualizer"]["uiEnabled"]
			if derived != tt.wantDerived || (derived && got != tt.wantEnabled) {
				t.Errorf("uiEnabled = %v (derived=%v), want %v (derived=%v)", got, derived, tt.wantEnabled, tt.wantDerived)
			}
			if strings.Join(live.Siblings["virtualizer"], ",") != strings.Join(tt.wantSiblings, ",") {
				t.Errorf("Siblings = %v, want %v", live.Siblings["virtualizer"], tt.wantSiblings)
			}
		})
	}
}

// The scan feeds Create and Complete alike: Create gets uiEnabled in the
// entry it renders, Complete gets no skeleton entries.
func TestScan_CreateDerivesUIEnabledAndCompleteAddsNoSkeleton(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		deployment("virtualizer", "stratio-apps"),
		deployment("virtualizer-monitor", "stratio-apps"),
	).Build()
	opts := Options{TenantName: "stratio", Catalog: cat, Config: seedConfig(), Client: c, Log: log.New(io.Discard, false)}

	out, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "uiEnabled: true") {
		t.Errorf("Create did not scaffold uiEnabled: true:\n%s", out)
	}

	components, live, err := Scan(context.Background(), opts, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(components["virtualizer"]) != 1 || len(components["pgbouncer"]) != 0 || len(components["postgres"]) != 0 {
		t.Errorf("Scan without expansion = %v, want the virtualizer alone", keysOfComponents(components))
	}
	if live.Values["virtualizer"]["uiEnabled"] != true {
		t.Errorf("live = %+v", live)
	}
}

func keysOfComponents(c Components) []string {
	var out []string
	for k := range c {
		out = append(out, k)
	}
	return out
}
