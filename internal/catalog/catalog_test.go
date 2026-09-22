package catalog

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func loadBasic(t *testing.T) *Catalog {
	t.Helper()
	cat, err := Load("testdata/basic")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	return cat
}

func TestLoad_Schemas(t *testing.T) {
	cat := loadBasic(t)

	postgres, ok := cat.Schemas["postgres"]
	if !ok {
		t.Fatal("Schemas[\"postgres\"] missing")
	}
	if len(postgres.Dependencies) != 1 || postgres.Dependencies[0].Key != "pgbackuprepository" {
		t.Errorf("postgres.Dependencies = %+v", postgres.Dependencies)
	}
	if postgres.ChartName != "postgres" {
		t.Errorf("postgres.ChartName = %q, want %q", postgres.ChartName, "postgres")
	}
	if len(postgres.Kustomizations) != 2 {
		t.Fatalf("postgres.Kustomizations = %+v, want 2 entries", postgres.Kustomizations)
	}

	rocket, ok := cat.Schemas["rocket"]
	if !ok {
		t.Fatal("Schemas[\"rocket\"] missing")
	}
	if len(rocket.Kustomizations) != 2 {
		t.Fatalf("rocket.Kustomizations = %+v, want [default, -postrequisites]", rocket.Kustomizations)
	}
}

func TestLoad_CRDs(t *testing.T) {
	cat := loadBasic(t)
	got, ok := cat.CRDs["pgclusters.postgres.stratio.com"]
	if !ok {
		t.Fatal("CRDs[pgclusters...] missing")
	}
	if got.ComponentKey != "postgres" {
		t.Errorf("ComponentKey = %q, want %q", got.ComponentKey, "postgres")
	}
	wantGVK := schema.GroupVersionKind{Group: "postgres.stratio.com", Version: "v1", Kind: "PgCluster"}
	if got.GVK != wantGVK {
		t.Errorf("GVK = %+v, want %+v", got.GVK, wantGVK)
	}
}

func TestLoad_UnambiguousChart(t *testing.T) {
	cat := loadBasic(t)
	m, ok := cat.Charts["rocket"]
	if !ok || m.Key != "rocket" || len(m.Ambiguous) != 0 {
		t.Errorf("Charts[\"rocket\"] = %+v (ok=%v), want unambiguous Key=rocket", m, ok)
	}
}

func TestLoad_AmbiguousChartAcrossFiles(t *testing.T) {
	cat := loadBasic(t)
	m, ok := cat.Charts["shared-chart"]
	if !ok {
		t.Fatal("Charts[\"shared-chart\"] missing")
	}
	if m.Key != "" {
		t.Errorf("Charts[\"shared-chart\"].Key = %q, want empty (ambiguous)", m.Key)
	}
	want := map[string]bool{"fooLogical": true, "fooPhysical": true}
	if len(m.Ambiguous) != 2 || !want[m.Ambiguous[0]] || !want[m.Ambiguous[1]] {
		t.Errorf("Charts[\"shared-chart\"].Ambiguous = %v, want [fooLogical fooPhysical] in some order", m.Ambiguous)
	}
}

func TestResolveAnchor_Default(t *testing.T) {
	cat := loadBasic(t)
	a, err := cat.ResolveAnchor("apps-psql")
	if err != nil {
		t.Fatalf("ResolveAnchor returned error: %v", err)
	}
	if a.Kind != AnchorDefault {
		t.Errorf("Kind = %v, want AnchorDefault", a.Kind)
	}
}

func TestResolveAnchor_NestedGosecAgent(t *testing.T) {
	cat := loadBasic(t)
	a, err := cat.ResolveAnchor("apps-psql-gosec-agent")
	if err != nil {
		t.Fatalf("ResolveAnchor returned error: %v", err)
	}
	if a.Kind != AnchorNested || a.Field != "config.agent" {
		t.Errorf("a = %+v, want AnchorNested at config.agent", a)
	}
}

func TestResolveAnchor_NotPatchable(t *testing.T) {
	cat := loadBasic(t)
	a, err := cat.ResolveAnchor("apps-rocket-postrequisites")
	if err != nil {
		t.Fatalf("ResolveAnchor returned error: %v", err)
	}
	if a.Kind != AnchorNotPatchable {
		t.Errorf("Kind = %v, want AnchorNotPatchable", a.Kind)
	}
}

func TestResolveAnchor_PrefersLongestSuffixOverDefault(t *testing.T) {
	cat := loadBasic(t)
	// "apps-rocket-postrequisites" must resolve via the "-postrequisites"
	// suffix, not fall back to the trivially-matching "" (default) suffix.
	a, err := cat.ResolveAnchor("apps-rocket-postrequisites")
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind == AnchorDefault {
		t.Error("resolved to AnchorDefault, want the more specific -postrequisites match")
	}
}

func TestResolveAnchor_MissingPrefix(t *testing.T) {
	cat := loadBasic(t)
	if _, err := cat.ResolveAnchor("not-apps-prefixed"); err == nil {
		t.Error("ResolveAnchor on a name without the apps- prefix: got nil error, want non-nil")
	}
}

func TestLoad_NoTemplatesFound(t *testing.T) {
	_, err := Load("testdata/empty")
	if err == nil || !strings.Contains(err.Error(), "no resourceset-apps") {
		t.Errorf("Load on an empty dir: err = %v, want a \"no templates found\" error", err)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	_, err := Load("testdata/malformed")
	if err == nil {
		t.Error("Load on malformed YAML: got nil error, want non-nil")
	}
}

func TestLoad_MissingDirectory(t *testing.T) {
	_, err := Load("testdata/does-not-exist")
	if err == nil {
		t.Error("Load on a nonexistent directory: got nil error, want non-nil")
	}
}

func TestBuildAnchorIndex_InconsistentSuffixErrors(t *testing.T) {
	schemas := map[string]Schema{
		"a": {Key: "a", Kustomizations: []KustomizationAnchor{{Suffix: "-x", Kind: AnchorDefault}}},
		"b": {Key: "b", Kustomizations: []KustomizationAnchor{{Suffix: "-x", Kind: AnchorNested, Field: "config.y"}}},
	}
	if _, err := buildAnchorIndex(schemas); err == nil {
		t.Error("buildAnchorIndex with two schemas disagreeing on the same suffix: got nil error, want non-nil")
	}
}

func TestAnchorKind_String(t *testing.T) {
	cases := map[AnchorKind]string{
		AnchorDefault:      "default",
		AnchorNested:       "nested",
		AnchorNotPatchable: "not-patchable",
		AnchorUnknown:      "unknown",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", kind, got, want)
		}
	}
}

func TestResolveAnchor_SuffixExposedForParentNameDerivation(t *testing.T) {
	cat := loadBasic(t)
	a, err := cat.ResolveAnchor("apps-psql-gosec-agent")
	if err != nil {
		t.Fatal(err)
	}
	if a.Suffix != "-gosec-agent" {
		t.Errorf("Suffix = %q, want %q", a.Suffix, "-gosec-agent")
	}

	def, err := cat.ResolveAnchor("apps-psql")
	if err != nil {
		t.Fatal(err)
	}
	if def.Suffix != "" {
		t.Errorf("default Suffix = %q, want empty", def.Suffix)
	}
}
