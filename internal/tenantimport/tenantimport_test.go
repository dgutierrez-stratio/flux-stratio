package tenantimport

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/catalog"
)

func loadFixtureCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load("testdata/catalog")
	if err != nil {
		t.Fatalf("catalog.Load returned error: %v", err)
	}
	return cat
}

func mustScheme(t *testing.T) *apiruntime.Scheme {
	t.Helper()
	s := apiruntime.NewScheme()
	for _, add := range []func(*apiruntime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func pgCluster(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PgCluster",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec":     map[string]any{},
	}}
}

func pgBouncer(name, namespace, pgClusterName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgres.stratio.com/v1", "kind": "PGBouncer",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec":     map[string]any{"pgcluster": map[string]any{"name": pgClusterName}},
	}}
}

func deployment(name, namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
}

func TestInTenantNamespace(t *testing.T) {
	cases := []struct {
		ns, tenant string
		want       bool
	}{
		{"stratio", "stratio", true},
		{"stratio-datastores", "stratio", true},
		{"stratiotest-apps", "stratio", false},
		{"other", "stratio", false},
	}
	for _, c := range cases {
		if got := inTenantNamespace(c.ns, c.tenant); got != c.want {
			t.Errorf("inTenantNamespace(%q, %q) = %v, want %v", c.ns, c.tenant, got, c.want)
		}
	}
}

func TestCamelToKebab(t *testing.T) {
	cases := map[string]string{
		"postgres":           "postgres",
		"pgbackuprepository": "pgbackuprepository",
		"gosecAgentPostgres": "gosec-agent-postgres",
		"governancePostgres": "governance-postgres",
	}
	for in, want := range cases {
		if got := camelToKebab(in); got != want {
			t.Errorf("camelToKebab(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScanCRDs_DiscoversInTenantNamespaceOnly(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		pgCluster("psql", "stratio-datastores"),
		pgCluster("other-psql", "other-datastores"),
	).Build()

	components, err := scanCRDs(context.Background(), c, cat, "stratio")
	if err != nil {
		t.Fatal(err)
	}
	names := components.names("postgres")
	if len(names) != 1 || names[0] != "psql" {
		t.Errorf("postgres entries = %v, want [psql]", names)
	}
}

func TestScanCRDs_DepSpecPathResolvesDependency(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		pgBouncer("pool-psql", "stratio-datastores", "psql"),
	).Build()

	components, err := scanCRDs(context.Background(), c, cat, "stratio")
	if err != nil {
		t.Fatal(err)
	}
	entry := components.find("pgbouncer", "pool-psql")
	if entry == nil {
		t.Fatal("pgbouncer entry not discovered")
	}
	if entry.Deps["postgres"] != "psql" {
		t.Errorf("Deps[postgres] = %q, want %q", entry.Deps["postgres"], "psql")
	}
}

func TestScanCRDs_UnknownCRDSkippedWithoutError(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).Build() // no CRDs registered at all
	components, err := scanCRDs(context.Background(), c, cat, "stratio")
	if err != nil {
		t.Fatalf("scanCRDs: %v, want an uninstalled CRD skipped", err)
	}
	if len(components) != 0 {
		t.Errorf("components = %+v, want empty", components)
	}
}

func TestScanDeployments_ExactNameMatch(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		deployment("connectors", "stratio-apps"),
	).Build()

	components := Components{}
	if err := scanDeployments(context.Background(), c, cat, "stratio", components); err != nil {
		t.Fatal(err)
	}
	if names := components.names("connectors"); len(names) != 1 || names[0] != "connectors" {
		t.Errorf("connectors entries = %v", names)
	}
}

// TestScanDeployments_SameTenantRulesAsApps: a Deployment marked as
// another tenant's (CCT's annotation or keos's label), or owned by
// another object, is never imported — the same objects `apps` leaves out,
// so the generated file never declares an entry apps can't resolve.
func TestScanDeployments_SameTenantRulesAsApps(t *testing.T) {
	cat := loadFixtureCatalog(t)
	otherAnnotated := deployment("connectors", "keos-core")
	otherAnnotated.Annotations = map[string]string{"cct.stratio.com/application_tenant": "platform"}
	otherLabelled := deployment("connectors", "keos-apps")
	otherLabelled.Labels = map[string]string{"keos.stratio.com/tenant": "platform"}
	owned := deployment("connectors", "keos-owned")
	owned.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Foo", Name: "foo", UID: "uid"}}
	mine := deployment("connectors", "keos-mine")
	mine.Annotations = map[string]string{"cct.stratio.com/application_tenant": "keos"}
	for _, tc := range []struct {
		name string
		obj  *appsv1.Deployment
		want int
	}{
		{"another tenant's, by annotation", otherAnnotated, 0},
		{"another tenant's, by label", otherLabelled, 0},
		{"owned", owned, 0},
		{"the tenant's own", mine, 1},
	} {
		c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(tc.obj).Build()
		components := Components{}
		if err := scanDeployments(context.Background(), c, cat, "keos", components); err != nil {
			t.Fatal(err)
		}
		if got := len(components["connectors"]); got != tc.want {
			t.Errorf("%s: connectors entries = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestScanDeployments_NamespaceSuffixFallback(t *testing.T) {
	cat := loadFixtureCatalog(t)
	// Namespace "stratio-virtualizer" has no Deployment literally named
	// "virtualizer" — only a differently-named one — so discovery must
	// fall back to the namespace suffix.
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		deployment("virtualizer-worker", "stratio-virtualizer"),
	).Build()

	components := Components{}
	if err := scanDeployments(context.Background(), c, cat, "stratio", components); err != nil {
		t.Fatal(err)
	}
	if names := components.names("virtualizer"); len(names) != 1 || names[0] != "virtualizer" {
		t.Errorf("virtualizer entries = %v, want [virtualizer] (from the namespace suffix)", names)
	}
}

func TestScanDeployments_DoesNotDuplicateAlreadyDiscovered(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		deployment("postgres", "stratio-datastores"),
	).Build()
	// This chart name "postgres" happens to also be the component key
	// here; seed it as already discovered under a *different* live name
	// to confirm the exact-match path still only adds once per name.
	components := Components{"postgres": {{Name: "psql", Deps: map[string]string{}}}}

	if err := scanDeployments(context.Background(), c, cat, "stratio", components); err != nil {
		t.Fatal(err)
	}
	if len(components["postgres"]) != 2 {
		t.Fatalf("postgres entries = %+v, want 2 (psql kept, postgres added)", components["postgres"])
	}

	// Running again must not add a third.
	if err := scanDeployments(context.Background(), c, cat, "stratio", components); err != nil {
		t.Fatal(err)
	}
	if len(components["postgres"]) != 2 {
		t.Errorf("postgres entries after a second scan = %+v, want still 2", components["postgres"])
	}
}

func TestScanDeployments_OutsideTenantIgnored(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		deployment("connectors", "other-apps"),
	).Build()
	components := Components{}
	if err := scanDeployments(context.Background(), c, cat, "stratio", components); err != nil {
		t.Fatal(err)
	}
	if len(components) != 0 {
		t.Errorf("components = %+v, want empty", components)
	}
}
