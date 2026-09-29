package components

import (
	"os"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

func loadLiveFixture(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	data, err := os.ReadFile("testdata/live.yaml")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := yamldocs.Decode(data)
	if err != nil {
		t.Fatalf("decoding testdata/live.yaml: %v", err)
	}
	return objs
}

func seededCatalog() *config.Catalog {
	cat := config.SeedCatalog()
	return &cat
}

func instanceKeys(instances []Instance) []string {
	keys := make([]string, 0, len(instances))
	for _, inst := range instances {
		var names []string
		for _, l := range inst.Live {
			names = append(names, l.GetName())
		}
		keys = append(keys, inst.Type.Type+" "+inst.Primary().GetNamespace()+"/"+strings.Join(names, ",")+" -> "+inst.Entry)
	}
	sort.Strings(keys)
	return keys
}

// TestClassify_SeededCatalogAgainstRealLegacyObjects is the regression
// test for the seeded selectors: every real legacy object in the fixture
// either classifies as exactly the component it is, or — a same-named
// PgDatabase, an owned sub-workload, a sibling workload of a chart — as
// nothing at all.
func TestClassify_SeededCatalogAgainstRealLegacyObjects(t *testing.T) {
	instances, err := Classify(seededCatalog(), loadLiveFixture(t), "stratio")
	if err != nil {
		t.Fatalf("Classify returned error: %v", err)
	}

	want := []string{
		"bdl-datarest stratio-datastores/dg-datarest-pgi -> dg-datarest-pgi",
		"datamarket-agent stratio-datastores/datamarket-agent -> governance-datamarket-agent",
		"dg-agent stratio-datastores/dg-hdfs-agent -> dg-hdfs-agent",
		"dg-agent stratio-datastores/dg-postgresql-internal-agent -> dg-postgresql-internal-agent",
		"discovery stratio-apps/discovery -> discovery",
		"dlc-entity stratio-dlc/dlc-entity -> dlc-entity",
		"eureka-agent stratio-datastores/eureka-agent -> eureka-agent",
		"genai stratio-genai/genai-api -> genai",
		"hdfs stratio-datastores/hdfs1 -> hdfs1",
		"intelligence stratio-intelligence/intelligence -> intelligence",
		"kafka stratio-datastores/kafka1 -> kafka1",
		"litellm stratio-genai/genai-litellm -> genai-litellm",
		"opendashboards stratio-datastores/opensearch1-dashboards -> opensearch1-dashboards",
		"opensearch stratio-datastores/opensearch1 -> opensearch1",
		"opensearch-gosec-agent stratio-datastores/opensearch1-agent -> opensearch1",
		"pgbouncer stratio-datastores/pool-psql -> pool-psql",
		"postgres stratio-datastores/psql -> psql",
		"postgres-gosec-agent stratio-datastores/psql-agent -> psql",
		"rocket stratio-rocket/rocket -> rocket",
		"virtualizer stratio-apps/virtualizer -> virtualizer",
	}
	got := instanceKeys(instances)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Classify instances:\n got:\n  %s\n want:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	for _, inst := range instances {
		for _, l := range inst.Live {
			if l.GetKind() == "PgDatabase" {
				t.Errorf("%s classified a PgDatabase (%s) — a same-named database must never be taken for its app", inst.Type.Type, l.GetName())
			}
		}
	}
}

// TestClassify_OtherTenantsObjectsExcluded: eosdev runs the platform's own
// opensearch1 (tenant keos, in keos-core) beside the stratio tenant's
// (stratio-datastores). Only the run's own tenant's copy is an instance;
// switching tenant flips which one.
func TestClassify_OtherTenantsObjectsExcluded(t *testing.T) {
	for _, c := range []struct{ tenant, wantNS string }{
		{"stratio", "stratio-datastores"},
		{"keos", "keos-core"},
	} {
		instances, err := Classify(seededCatalog(), loadLiveFixture(t), c.tenant)
		if err != nil {
			t.Fatal(err)
		}
		var namespaces []string
		for _, inst := range instances {
			if inst.Type.Type == "opensearch" || inst.Type.Type == "opensearch-gosec-agent" {
				namespaces = append(namespaces, inst.Primary().GetNamespace())
			}
		}
		if len(namespaces) != 2 || namespaces[0] != c.wantNS || namespaces[1] != c.wantNS {
			t.Errorf("tenant %s: opensearch instances in %v, want both in %s", c.tenant, namespaces, c.wantNS)
		}
	}
}

func TestClassify_GroupsSameEntryInOneNamespace(t *testing.T) {
	cat := &config.Catalog{Types: []config.ComponentType{{
		Type: "genai", Name: "GenAI", Component: "genai", Rset: "r.yaml",
		Entry: "genai",
		Match: config.Match{Kinds: []string{"apps/v1/Deployment"}, Annotations: &config.Selector{
			MatchLabels: map[string]string{"svc": "genai"},
		}},
	}}}
	objs := []*unstructured.Unstructured{
		deployment("genai-litellm", "stratio-genai", map[string]string{"svc": "genai"}),
		deployment("genai-api", "stratio-genai", map[string]string{"svc": "genai"}),
		deployment("genai-api", "other-genai", map[string]string{"svc": "genai"}),
	}
	instances, err := Classify(cat, objs, "stratio")
	if err != nil {
		t.Fatal(err)
	}
	got := instanceKeys(instances)
	want := []string{
		"genai other-genai/genai-api -> genai",
		"genai stratio-genai/genai-api,genai-litellm -> genai",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("instances = %v, want %v", got, want)
	}
}

func TestMatches_SelectorOperators(t *testing.T) {
	obj := deployment("x", "ns", map[string]string{"svc": "a", "model": "m"})
	cases := []struct {
		name string
		sel  *config.Selector
		want bool
	}{
		{"nil selector", nil, true},
		{"matchLabels hit", &config.Selector{MatchLabels: map[string]string{"svc": "a"}}, true},
		{"matchLabels miss", &config.Selector{MatchLabels: map[string]string{"svc": "b"}}, false},
		{"matchLabels absent key", &config.Selector{MatchLabels: map[string]string{"other": "a"}}, false},
		{"In hit", expr("svc", config.OpIn, "a", "b"), true},
		{"In miss", expr("svc", config.OpIn, "c"), false},
		{"NotIn hit", expr("model", config.OpNotIn, "m"), false},
		{"NotIn absent key", expr("other", config.OpNotIn, "m"), true},
		{"Exists", expr("model", config.OpExists), true},
		{"Exists absent", expr("other", config.OpExists), false},
		{"DoesNotExist", expr("other", config.OpDoesNotExist), true},
		{"DoesNotExist present", expr("svc", config.OpDoesNotExist), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			typ := &config.ComponentType{Match: config.Match{Kinds: []string{"apps/v1/Deployment"}, Annotations: c.sel}}
			if got := Matches(typ, obj); got != c.want {
				t.Errorf("Matches = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMatches_KindComparesGroupAndKindNotVersion(t *testing.T) {
	obj := deployment("x", "ns", nil)
	cases := []struct {
		kind string
		want bool
	}{
		{"apps/v1/Deployment", true},
		{"apps/v2/Deployment", true},
		{"apps/v1/StatefulSet", false},
		{"other.io/v1/Deployment", false},
	}
	for _, c := range cases {
		typ := &config.ComponentType{Match: config.Match{Kinds: []string{c.kind}}}
		if got := Matches(typ, obj); got != c.want {
			t.Errorf("kind %s: Matches = %v, want %v", c.kind, got, c.want)
		}
	}
}

func TestClassify_TemplateCanReadAnnotations(t *testing.T) {
	cat := &config.Catalog{Types: []config.ComponentType{{
		Type: "t", Name: "T", Component: "c", Rset: "r.yaml",
		Entry: `{{ .Live.Annotation "cct.stratio.com/application_service" }}-{{ .Tenant }}`,
		Match: config.Match{Kinds: []string{"apps/v1/Deployment"}},
	}}}
	objs := []*unstructured.Unstructured{deployment("x", "ns", map[string]string{"cct.stratio.com/application_service": "rocket"})}
	instances, err := Classify(cat, objs, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].Entry != "rocket-acme" {
		t.Errorf("instances = %v, want one with entry rocket-acme", instanceKeys(instances))
	}
}

func TestClassify_EmptyRenderedEntryIsAnError(t *testing.T) {
	cat := &config.Catalog{Types: []config.ComponentType{{
		Type: "t", Name: "T", Component: "c", Rset: "r.yaml",
		Entry: `{{ .Live.Annotation "missing" }}`,
		Match: config.Match{Kinds: []string{"apps/v1/Deployment"}},
	}}}
	if _, err := Classify(cat, []*unstructured.Unstructured{deployment("x", "ns", nil)}, "t"); err == nil {
		t.Error("Classify returned nil error for an empty rendered entry, want an error")
	}
}

func deployment(name, namespace string, annotations map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps/v1")
	u.SetKind("Deployment")
	u.SetName(name)
	u.SetNamespace(namespace)
	if annotations != nil {
		u.SetAnnotations(annotations)
	}
	return u
}

func expr(key, op string, values ...string) *config.Selector {
	return &config.Selector{MatchExpressions: []config.SelectorRequirement{{Key: key, Operator: op, Values: values}}}
}

// managedDeployment is a Deployment as a Flux HelmRelease renders it: Helm
// labels and keos's tenant label, no CCT annotations.
func managedDeployment(name, namespace, helmRelease, tenant string) *unstructured.Unstructured {
	u := deployment(name, namespace, nil)
	u.SetLabels(map[string]string{
		helmReleaseNameLabel:      helmRelease,
		helmReleaseNamespaceLabel: namespace,
		keosTenantLabel:           tenant,
	})
	return u
}

func helmRelease(name, namespace, chart string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{}}
	u.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	u.SetKind("HelmRelease")
	u.SetName(name)
	u.SetNamespace(namespace)
	if chart != "" {
		_ = unstructured.SetNestedField(u.Object, chart, "spec", "chart", "spec", "chart")
	}
	return u
}

// migrated replaces the fixture's legacy Deployment name in namespace with
// what a same-named HelmRelease leaves after adopting it.
func migrated(objs []*unstructured.Unstructured, name, namespace, chart string) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, o := range objs {
		if o.GetKind() == "Deployment" && o.GetName() == name && o.GetNamespace() == namespace {
			continue
		}
		out = append(out, o)
	}
	return append(out, managedDeployment(name, namespace, name, "stratio"), helmRelease(name, namespace, chart))
}

func TestManagedMatches(t *testing.T) {
	chartType := &config.ComponentType{Type: "virtualizer", Chart: &config.Chart{Path: "virtualizer"},
		Match: config.Match{Kinds: []string{"apps/v1/Deployment"}}}
	manifestType := &config.ComponentType{Type: "virtualizer",
		Match: config.Match{Kinds: []string{"apps/v1/Deployment"}}}
	obj := managedDeployment("virtualizer", "stratio-apps", "virtualizer", "stratio")
	charts := helmReleaseCharts([]*unstructured.Unstructured{helmRelease("virtualizer", "stratio-apps", "virtualizer")})

	for _, c := range []struct {
		name   string
		t      *config.ComponentType
		obj    *unstructured.Unstructured
		charts map[string]string
		want   bool
	}{
		{"chart matches", chartType, obj, charts, true},
		{"other chart", chartType, obj, map[string]string{"stratio-apps/virtualizer": "discovery"}, false},
		{"HelmRelease not live", chartType, obj, map[string]string{}, false},
		{"not Helm-managed", chartType, deployment("virtualizer", "stratio-apps", nil), charts, false},
		{"manifest-mode type", manifestType, obj, charts, false},
		{"kind not selected", chartType, helmRelease("virtualizer", "stratio-apps", "virtualizer"), charts, false},
	} {
		if got := ManagedMatches(c.t, c.obj, c.charts); got != c.want {
			t.Errorf("%s: ManagedMatches = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestClassify_IgnoresMigratedWorkloads: the HelmRelease fallback is a
// named-lookup one (Resolve); Classify, and so --all, still selects by the
// catalog's selectors alone.
func TestClassify_IgnoresMigratedWorkloads(t *testing.T) {
	objs := migrated(loadLiveFixture(t), "virtualizer", "stratio-apps", "virtualizer")
	instances, err := Classify(seededCatalog(), objs, "stratio")
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range instances {
		if inst.Type.Type == "virtualizer" {
			t.Errorf("Classify returned %s for a migrated workload", inst.Label())
		}
	}
}
