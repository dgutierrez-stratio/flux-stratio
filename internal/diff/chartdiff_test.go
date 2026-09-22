package diff

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestChartDiff_ProducesMappedPatch(t *testing.T) {
	docs := []*unstructured.Unstructured{
		configMapDoc("app-config", map[string]any{"API_LOG_LEVEL": "INFO"}),
	}
	liveEnv := map[string]string{"API_LOG_LEVEL": "DEBUG"}
	valuesMap := map[string]string{"API_LOG_LEVEL": "genaiDeveloperProxy.general.log.apiLogLevel"}

	result := ChartDiff(docs, liveEnv, valuesMap, "genai-developer-proxy", nil)

	if result.Patch == nil {
		t.Fatal("Patch = nil, want a patch for the changed value")
	}
	body, _ := result.Patch.Patch.(map[string]any)
	spec, _ := body["spec"].(map[string]any)
	values, _ := spec["values"].(map[string]any)
	genai, _ := values["genaiDeveloperProxy"].(map[string]any)
	general, _ := genai["general"].(map[string]any)
	log, _ := general["log"].(map[string]any)
	if log["apiLogLevel"] != "DEBUG" {
		t.Errorf("apiLogLevel = %v, want %q", log["apiLogLevel"], "DEBUG")
	}
	if result.Patch.TargetKind != "HelmRelease" {
		t.Errorf("TargetKind = %q, want HelmRelease", result.Patch.TargetKind)
	}
}

func TestChartDiff_NoDifferenceReturnsNilPatch(t *testing.T) {
	docs := []*unstructured.Unstructured{configMapDoc("app-config", map[string]any{"FOO": "same"})}
	liveEnv := map[string]string{"FOO": "same"}
	result := ChartDiff(docs, liveEnv, map[string]string{"FOO": "a.b"}, "hr", nil)
	if result.Patch != nil {
		t.Errorf("Patch = %+v, want nil", result.Patch)
	}
}

func TestChartDiff_UnmappedDiffReported(t *testing.T) {
	docs := []*unstructured.Unstructured{configMapDoc("app-config", map[string]any{"UNMAPPED": "rendered-value"})}
	liveEnv := map[string]string{"UNMAPPED": "live-value"}
	result := ChartDiff(docs, liveEnv, map[string]string{}, "hr", nil)

	if result.Patch != nil {
		t.Errorf("Patch = %+v, want nil (nothing mapped)", result.Patch)
	}
	want := []UnmappedDiff{{Name: "UNMAPPED", Rendered: "rendered-value", Live: "live-value"}}
	if !reflect.DeepEqual(result.UnmappedDiffs, want) {
		t.Errorf("UnmappedDiffs = %+v, want %+v", result.UnmappedDiffs, want)
	}
}

func TestChartDiff_LiveOnlyAndRenderedOnlyCounted(t *testing.T) {
	docs := []*unstructured.Unstructured{configMapDoc("app-config", map[string]any{"RENDERED_ONLY": "x"})}
	liveEnv := map[string]string{"LIVE_ONLY": "y"}
	result := ChartDiff(docs, liveEnv, nil, "hr", nil)

	if result.LiveOnlyCount != 1 {
		t.Errorf("LiveOnlyCount = %d, want 1", result.LiveOnlyCount)
	}
	if result.RenderedOnlyCount != 1 {
		t.Errorf("RenderedOnlyCount = %d, want 1", result.RenderedOnlyCount)
	}
}

func TestChartDiff_PlaceholderValuesSkipped(t *testing.T) {
	docs := []*unstructured.Unstructured{deploymentDoc("app", []any{
		map[string]any{"name": "main", "env": []any{
			map[string]any{"name": "SECRET_VAL", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "s", "key": "k"}}},
		}},
	})}
	liveEnv := map[string]string{"SECRET_VAL": "an-actual-secret"}
	result := ChartDiff(docs, liveEnv, map[string]string{"SECRET_VAL": "a.b"}, "hr", nil)

	if result.Patch != nil {
		t.Errorf("Patch = %+v, want nil (rendered side is an unresolvable placeholder)", result.Patch)
	}
	if len(result.UnmappedDiffs) != 0 {
		t.Errorf("UnmappedDiffs = %+v, want empty (placeholder comparisons are skipped, not reported as unmapped)", result.UnmappedDiffs)
	}
}

func TestChartDiff_ExcludePathsApplied(t *testing.T) {
	docs := []*unstructured.Unstructured{configMapDoc("app-config", map[string]any{
		"APPROLENAME": "rendered",
		"OTHER":       "rendered-other",
	})}
	liveEnv := map[string]string{"APPROLENAME": "live-secret", "OTHER": "live-other"}
	valuesMap := map[string]string{
		"APPROLENAME": "datarestPgInternal.general.identity.approlename",
		"OTHER":       "datarestPgInternal.general.other",
	}
	result := ChartDiff(docs, liveEnv, valuesMap, "hr", []string{"spec.values.datarestPgInternal.general.identity"})

	if result.Patch == nil {
		t.Fatal("Patch = nil, want a patch for OTHER")
	}
	body, _ := result.Patch.Patch.(map[string]any)
	spec, _ := body["spec"].(map[string]any)
	values, _ := spec["values"].(map[string]any)
	datarest, _ := values["datarestPgInternal"].(map[string]any)
	general, _ := datarest["general"].(map[string]any)
	if _, excluded := general["identity"]; excluded {
		t.Errorf("general.identity should have been excluded and pruned, got %v", general["identity"])
	}
	if general["other"] != "live-other" {
		t.Errorf("general.other = %v, want %q", general["other"], "live-other")
	}
}

func TestDedupeRenderedEnv_ConcreteBeatsPlaceholder(t *testing.T) {
	entries := []EnvVarEntry{
		{Name: "X", Value: "concrete"},
		{Name: "X", Value: "<secret:s/k>"},
	}
	got := dedupeRenderedEnv(entries)
	if got["X"] != "concrete" {
		t.Errorf("X = %q, want the concrete value preserved", got["X"])
	}
}

func TestDedupeRenderedEnv_LaterConcreteOverwritesEarlierConcrete(t *testing.T) {
	entries := []EnvVarEntry{
		{Name: "X", Value: "first"},
		{Name: "X", Value: "second"},
	}
	got := dedupeRenderedEnv(entries)
	if got["X"] != "second" {
		t.Errorf("X = %q, want %q (last concrete value wins)", got["X"], "second")
	}
}

func TestSetDotPath(t *testing.T) {
	root := map[string]any{}
	setDotPath(root, "a.b.c", "value")
	want := map[string]any{"a": map[string]any{"b": map[string]any{"c": "value"}}}
	if !reflect.DeepEqual(root, want) {
		t.Errorf("root = %+v, want %+v", root, want)
	}
}

func TestSetDotPath_ReusesExistingIntermediateMaps(t *testing.T) {
	root := map[string]any{"a": map[string]any{"existing": "keep"}}
	setDotPath(root, "a.b", "value")
	want := map[string]any{"a": map[string]any{"existing": "keep", "b": "value"}}
	if !reflect.DeepEqual(root, want) {
		t.Errorf("root = %+v, want %+v", root, want)
	}
}
