package tenantimport

import (
	"context"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
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

// cctDeployment is a legacy CCT workload: what CCT stamped on it is all that
// says what it is, its name is whatever the environment gave it.
func cctDeployment(name, namespace, service, model string) *appsv1.Deployment {
	d := deployment(name, namespace)
	d.Annotations = map[string]string{
		"cct.stratio.com/application_service": service,
		"cct.stratio.com/application_model":   model,
		"cct.stratio.com/application_tenant":  "stratio",
	}
	return d
}

func scanClassifiedFor(t *testing.T, tenant string, found Components, objs ...*appsv1.Deployment) Components {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(mustScheme(t))
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	var warnings []string
	warn := func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) }
	if err := scanClassified(context.Background(), b.Build(), seedConfig(), loadFixtureCatalog(t), tenant, found, warn); err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Logf("warnings: %v", warnings)
	}
	return found
}

// A legacy app's workload is not named like its chart: the entry is what the
// catalog says the GitOps component is called.
func TestScanClassified_LegacyAppUnderItsGitOpsEntryName(t *testing.T) {
	found := scanClassifiedFor(t, "stratio", Components{},
		cctDeployment("connectors-management", "stratio-apps", "connectors-management", "connectors-management-service-default"),
		cctDeployment("virtualizer", "stratio-apps", "virtualizer", "default"),
	)
	if names := found.names("connectors"); len(names) != 1 || names[0] != "connectors" {
		t.Errorf("connectors entries = %v, want [connectors] (connectors-management minus its suffix)", names)
	}
	if names := found.names("virtualizer"); len(names) != 1 || names[0] != "virtualizer" {
		t.Errorf("virtualizer entries = %v", names)
	}
}

// A bare Deployment that merely carries a chart's name is not a legacy app.
func TestScanClassified_NameAloneIsNotEvidence(t *testing.T) {
	found := scanClassifiedFor(t, "stratio", Components{}, deployment("connectors", "stratio-apps"))
	if len(found) != 0 {
		t.Errorf("components = %+v, want none: no CCT annotations", found)
	}
}

// TestScanClassified_SameTenantRulesAsApps: a Deployment marked as another
// tenant's (CCT's annotation or keos's label), or owned by another object, is
// never imported — the same objects `apps` leaves out.
func TestScanClassified_SameTenantRulesAsApps(t *testing.T) {
	const svc, model = "connectors-management", "connectors-management-service-default"
	otherAnnotated := cctDeployment("connectors-management", "keos-core", svc, model)
	otherAnnotated.Annotations["cct.stratio.com/application_tenant"] = "platform"
	otherLabelled := cctDeployment("connectors-management", "keos-apps", svc, model)
	otherLabelled.Labels = map[string]string{"keos.stratio.com/tenant": "platform"}
	owned := cctDeployment("connectors-management", "keos-owned", svc, model)
	owned.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Foo", Name: "foo", UID: "uid"}}
	mine := cctDeployment("connectors-management", "keos-mine", svc, model)
	mine.Annotations["cct.stratio.com/application_tenant"] = "keos"
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
		found := scanClassifiedFor(t, "keos", Components{}, tc.obj)
		if got := len(found["connectors"]); got != tc.want {
			t.Errorf("%s: connectors entries = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestScanClassified_DoesNotDuplicateAnEntryAlreadyDiscovered(t *testing.T) {
	obj := cctDeployment("connectors-management", "stratio-apps", "connectors-management", "connectors-management-service-default")
	found := Components{"connectors": {{Name: "connectors", Deps: map[string]string{}}}}
	found = scanClassifiedFor(t, "stratio", found, obj)
	found = scanClassifiedFor(t, "stratio", found, obj)
	if len(found["connectors"]) != 1 {
		t.Errorf("connectors entries = %+v, want the one already discovered", found["connectors"])
	}
}

func TestScanClassified_OutsideTenantIgnored(t *testing.T) {
	found := scanClassifiedFor(t, "stratio", Components{},
		cctDeployment("connectors-management", "other-apps", "connectors-management", "connectors-management-service-default"))
	if len(found) != 0 {
		t.Errorf("components = %+v, want empty", found)
	}
}

// A postgres' gosec agent is part of its postgres entry's config, not a
// component: with no PgCluster live it must not invent a postgres entry.
func TestScanClassified_NestedAgentIsNotAnEntry(t *testing.T) {
	found := scanClassifiedFor(t, "stratio", Components{},
		cctDeployment("psql-agent", "stratio-datastores", "pg-gosec-agent", ""))
	if len(found) != 0 {
		t.Errorf("components = %+v, want none: the agent belongs to a postgres entry the cluster does not have", found)
	}
}

// A type whose component no ResourceSet declares is reported, not imported
// under a key the render ignores.
func TestScanClassified_UnknownComponentKeyWarns(t *testing.T) {
	cfg := &config.Catalog{Types: []config.ComponentType{{
		Type: "mystery", Name: "Mystery", Component: "mystery", Rset: "x",
		Match: config.Match{
			Kinds:       []string{"apps/v1/Deployment"},
			Annotations: &config.Selector{MatchLabels: map[string]string{"cct.stratio.com/application_service": "mystery"}},
		},
	}}}
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(cctDeployment("mystery", "stratio-apps", "mystery", "")).Build()
	var warnings []string
	warn := func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) }
	found := Components{}
	if err := scanClassified(context.Background(), c, cfg, loadFixtureCatalog(t), "stratio", found, warn); err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], `component "mystery"`) {
		t.Errorf("components = %+v, warnings = %v, want one warning and nothing imported", found, warnings)
	}
}

// legacyAgent is a legacy governance agent: its env says which flavor it is
// and what governance datastore it talks to.
func legacyAgent(name, extractor string) *appsv1.Deployment {
	d := cctDeployment(name, "stratio-datastores", "connectors-dfs", "agent-dfs-hdfs-internal")
	d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "agent", Env: []corev1.EnvVar{
		{Name: "CUSTOM_SERVICE_EXTRACTOR_TYPE", Value: extractor},
		{Name: "DATASTORE_HOST", Value: "pgbouncer-postgreskeos-governance.keos-core"},
		{Name: "DATASTORE_DATABASE", Value: "postgreskeos"},
		// Never read: a secret's value is not the scan's to see.
		{Name: "CUSTOM_STRATIO_CREDENTIALS", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Key: "k"}}},
	}}}
	return d
}

// What a legacy agent was configured with says which GitOps flavor it is, and
// what it points at; one with no GitOps equivalent is left out and said so.
func TestScanClassified_EnvDerivesTheTypeAndGovernanceDependencies(t *testing.T) {
	var warnings []string
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		legacyAgent("dg-hdfs-agent", "HDFS"),
		legacyAgent("dg-postgresql-internal-agent", "POSTGRESQL"),
	).Build()
	found := Components{}
	warn := func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) }
	if err := scanClassified(context.Background(), c, seedConfig(), loadFixtureCatalog(t), "stratio", found, warn); err != nil {
		t.Fatal(err)
	}

	if names := found.names("dgAgent"); len(names) != 1 || names[0] != "dg-hdfs-agent" {
		t.Fatalf("dgAgent entries = %v, want only dg-hdfs-agent (the POSTGRESQL agent has no overlay)", names)
	}
	agent := found.find("dgAgent", "dg-hdfs-agent")
	if agent.Type != "hdfs" {
		t.Errorf("type = %q, want hdfs from CUSTOM_SERVICE_EXTRACTOR_TYPE=HDFS", agent.Type)
	}
	if agent.Deps["governancePgbouncer"] != "pgbouncer-postgreskeos-governance" || agent.Deps["governancePostgres"] != "postgreskeos" {
		t.Errorf("Deps = %v, want the governance pgbouncer (host's first label) and postgres", agent.Deps)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], `CUSTOM_SERVICE_EXTRACTOR_TYPE is "POSTGRESQL"`) || !strings.Contains(warnings[0], "not imported") {
		t.Errorf("warnings = %v, want one saying the POSTGRESQL agent is not imported", warnings)
	}
}

func TestScan_ScanWarningsReachComplete(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		legacyAgent("dg-postgresql-internal-agent", "POSTGRESQL"),
	).Build()
	// Complete's scan is quiet (no Log); what it found must still be reported.
	scanned, live, err := Scan(context.Background(), Options{TenantName: "stratio", Catalog: loadFixtureCatalog(t), Config: seedConfig(), Client: c}, false)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := complete(t, loadDoc(t, "eosdev.yaml"), scanned, live, CompleteOptions{})
	if !contains(r.Warnings, "dg-postgresql-internal-agent") || !contains(r.Warnings, "not imported") {
		t.Errorf("Warnings = %v, want the skipped agent", r.Warnings)
	}
}

// Of several candidates, the one of the dependent's storage type is chosen;
// without one it is the first, with every candidate named.
func TestPickDependency(t *testing.T) {
	two := Components{"dgAgent": {{Name: "a-s3", Type: "s3"}, {Name: "b-hdfs", Type: "hdfs"}}}
	if name, amb := pickDependency(two, "dgAgent", "hdfs"); name != "b-hdfs" || len(amb) != 0 {
		t.Errorf("hdfs dependent: %q %v, want b-hdfs decided", name, amb)
	}
	if name, amb := pickDependency(two, "dgAgent", ""); name != "a-s3" || len(amb) != 0 {
		t.Errorf("no storage type, no hdfs live: %q %v, want the s3 one (the tenant's storage)", name, amb)
	}
	two["hdfs"] = []*Entry{{Name: "hdfs1"}}
	if name, amb := pickDependency(two, "dgAgent", ""); name != "b-hdfs" || len(amb) != 0 {
		t.Errorf("hdfs live: %q %v, want the hdfs one", name, amb)
	}
	untyped := Components{"postgres": {{Name: "pg-a"}, {Name: "pg-b"}}}
	if name, amb := pickDependency(untyped, "postgres", ""); name != "pg-a" || len(amb) != 2 {
		t.Errorf("untyped candidates: %q %v, want the first, ambiguous", name, amb)
	}
	if name, _ := pickDependency(untyped, "none", ""); name != "" {
		t.Errorf("unknown key resolved to %q", name)
	}
}

func legacyDatarest(database string) *appsv1.Deployment {
	d := cctDeployment("dg-datarest-pgi", "stratio-datastores", "bdl", "datarest-pginternal")
	d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "datarest", Env: []corev1.EnvVar{
		{Name: "JDBC_DATABASE", Value: database},
	}}}
	return d
}

// Any config field a ResourceSet reads can be derived from the legacy env:
// the database datarest is connected to, not the template's default.
func TestScanClassified_EnvDerivesAConfigField(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(legacyDatarest("data")).Build()
	found := Components{}
	if err := scanClassified(context.Background(), c, seedConfig(), loadFixtureCatalog(t), "stratio", found, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	e := found.find("bdlDatarest", "dg-datarest-pgi")
	if e == nil || e.FromEnv["postgresDatabase"] != "data" || e.ExtraConfig["postgresDatabase"] != "data" {
		t.Fatalf("entry = %+v, want postgresDatabase data from JDBC_DATABASE", e)
	}
}

const datarestTenant = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  defaultValues:
    tenantName: stratio
    size: S
    sopsSecretProvided: false
    internalS3BucketName: ""
    userEmail: ""
    userId: ""
    userName: ""
    components:
      bdlDatarest:
      - name: dg-datarest-pgi%s
`

func scannedDatarest(t *testing.T, database string) Components {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(legacyDatarest(database)).Build()
	scanned, _, err := Scan(context.Background(), Options{TenantName: "stratio", Catalog: loadFixtureCatalog(t), Config: seedConfig(), Client: c}, false)
	if err != nil {
		t.Fatal(err)
	}
	return scanned
}

// Complete writes what the legacy app runs with instead of the template default.
func TestComplete_AddsAConfigFieldFromTheLegacyEnv(t *testing.T) {
	doc := reload(t, []byte(strings.Replace(datarestTenant, "%s", "", 1)))
	r, got := complete(t, doc, scannedDatarest(t, "data"), Live{}, CompleteOptions{})
	cfg := entryConfigOf(t, got, "bdlDatarest", "dg-datarest-pgi")
	if cfg["postgresDatabase"] != "data" {
		t.Errorf("config = %v, want postgresDatabase data (not the template's governance)\n%s", cfg, got)
	}
	if !contains(r.Added, "config.postgresDatabase: \"data\"") {
		t.Errorf("Added = %v", r.Added)
	}
}

// A value in the file wins, and a disagreement with the legacy app is said.
func TestComplete_AConfigFieldTheFileSetsWinsOverTheLegacyEnvAndWarnsWhenTheyDiffer(t *testing.T) {
	src := strings.Replace(datarestTenant, "%s", "\n        config:\n          postgresDatabase: bdldatarest", 1)
	r, got := complete(t, reload(t, []byte(src)), scannedDatarest(t, "data"), Live{}, CompleteOptions{})
	if cfg := entryConfigOf(t, got, "bdlDatarest", "dg-datarest-pgi"); cfg["postgresDatabase"] != "bdldatarest" {
		t.Errorf("the file's value was changed: %v", cfg)
	}
	if !contains(r.Warnings, "config.postgresDatabase is bdldatarest but its legacy workload is configured with \"data\"") {
		t.Errorf("Warnings = %v, want the disagreement", r.Warnings)
	}

	// Agreeing: no warning.
	r, _ = complete(t, reload(t, []byte(src)), scannedDatarest(t, "bdldatarest"), Live{}, CompleteOptions{})
	if contains(r.Warnings, "legacy workload is configured") {
		t.Errorf("Warnings = %v, want none when they agree", r.Warnings)
	}
}
