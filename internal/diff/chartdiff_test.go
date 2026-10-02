package diff

import (
	"reflect"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// patchValues is result's patch spec.values, or nil without a patch.
func patchValues(t *testing.T, result *ChartDiffResult) map[string]any {
	t.Helper()
	if result.Patch == nil {
		return nil
	}
	body, _ := result.Patch.Patch.(map[string]any)
	spec, _ := body["spec"].(map[string]any)
	values, _ := spec["values"].(map[string]any)
	return values
}

// singleWorkload is a one-workload chart whose ConfigMap no container
// references — the shape chart mode always compared as a whole.
func singleWorkload(data map[string]any) []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		configMapDoc("app-config", data),
		deploymentDoc("app", []any{map[string]any{"name": "main"}}),
	}
}

func liveFor(docs []*unstructured.Unstructured, name string, env map[string]string) LiveWorkloadEnv {
	for _, d := range docs {
		if d.GetName() == name && workloadKinds[d.GetKind()] {
			return LiveWorkloadEnv{Rendered: d, Env: env}
		}
	}
	panic("no rendered workload " + name)
}

func TestChartDiff_ProducesMappedPatch(t *testing.T) {
	docs := singleWorkload(map[string]any{"API_LOG_LEVEL": "INFO"})
	files := []ChartFile{{Path: "config/env_vars.yaml", Values: map[string]string{"API_LOG_LEVEL": "genaiDeveloperProxy.general.log.apiLogLevel"}}}

	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "genai-developer-proxy",
		Live: []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"API_LOG_LEVEL": "DEBUG"})},
	})

	if result.Patch == nil {
		t.Fatal("Patch = nil, want a patch for the changed value")
	}
	want := map[string]any{"genaiDeveloperProxy": map[string]any{"general": map[string]any{"log": map[string]any{"apiLogLevel": "DEBUG"}}}}
	if got := patchValues(t, result); !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
	if result.Patch.TargetKind != "HelmRelease" {
		t.Errorf("TargetKind = %q, want HelmRelease", result.Patch.TargetKind)
	}
}

func TestChartDiff_NoDifferenceReturnsNilPatch(t *testing.T) {
	docs := singleWorkload(map[string]any{"FOO": "same"})
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, HRName: "hr",
		Files: []ChartFile{{Values: map[string]string{"FOO": "a.b"}}},
		Live:  []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"FOO": "same"})},
	})
	if result.Patch != nil {
		t.Errorf("Patch = %+v, want nil", result.Patch)
	}
}

func TestChartDiff_UnmappedDiffReported(t *testing.T) {
	docs := singleWorkload(map[string]any{"UNMAPPED": "rendered-value"})
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, HRName: "hr",
		Live: []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"UNMAPPED": "live-value"})},
	})

	if result.Patch != nil {
		t.Errorf("Patch = %+v, want nil (nothing mapped)", result.Patch)
	}
	want := []UnmappedDiff{{Workload: "app", Name: "UNMAPPED", Rendered: "rendered-value", Live: "live-value", Reason: UnmappedNoPath}}
	if !reflect.DeepEqual(result.UnmappedDiffs, want) {
		t.Errorf("UnmappedDiffs = %+v, want %+v", result.UnmappedDiffs, want)
	}
}

func TestChartDiff_LiveOnlyAndRenderedOnlyCounted(t *testing.T) {
	docs := singleWorkload(map[string]any{"RENDERED_ONLY": "x"})
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, HRName: "hr",
		Live: []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"LIVE_ONLY": "y"})},
	})

	if want := []LiveOnlyVar{{Workload: "app", Name: "LIVE_ONLY", Live: "y"}}; !slices.Equal(result.LiveOnly, want) {
		t.Errorf("LiveOnly = %+v, want %+v", result.LiveOnly, want)
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
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, HRName: "hr",
		Files: []ChartFile{{Values: map[string]string{"SECRET_VAL": "a.b"}}},
		Live:  []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"SECRET_VAL": "an-actual-secret"})},
	})

	if result.Patch != nil {
		t.Errorf("Patch = %+v, want nil (rendered side is an unresolvable placeholder)", result.Patch)
	}
	if len(result.UnmappedDiffs) != 0 {
		t.Errorf("UnmappedDiffs = %+v, want empty (placeholder comparisons are skipped, not reported as unmapped)", result.UnmappedDiffs)
	}
}

// TestChartDiff_SecretLiveValueReportedNotPatched: legacy read the
// variable from a Secret (internal/envvars resolves it to a placeholder)
// while the chart renders it from a ConfigMap. The value isn't known
// and must never reach the tenant file, so it's reported for review.
func TestChartDiff_SecretLiveValueReportedNotPatched(t *testing.T) {
	docs := singleWorkload(map[string]any{"DB_PASSWORD": "changeme"})
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, HRName: "hr",
		Files: []ChartFile{{Values: map[string]string{"DB_PASSWORD": "db.password"}}},
		Live:  []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"DB_PASSWORD": "<secret:db-creds/password>"})},
	})

	if result.Patch != nil {
		t.Errorf("Patch = %+v, want nil: a Secret-sourced value is never patched", result.Patch)
	}
	want := []UnmappedDiff{{Workload: "app", Name: "DB_PASSWORD", Rendered: "changeme", Live: "<secret:db-creds/password>", Reason: UnmappedLiveUnknown}}
	if !reflect.DeepEqual(result.UnmappedDiffs, want) {
		t.Errorf("UnmappedDiffs = %+v, want %+v", result.UnmappedDiffs, want)
	}
}

func TestChartDiff_ExcludePathsApplied(t *testing.T) {
	docs := singleWorkload(map[string]any{"APPROLENAME": "rendered", "OTHER": "rendered-other"})
	files := []ChartFile{{Values: map[string]string{
		"APPROLENAME": "datarestPgInternal.general.identity.approlename",
		"OTHER":       "datarestPgInternal.general.other",
	}}}
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "hr",
		Exclude: []string{"spec.values.datarestPgInternal.general.identity"},
		Live:    []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"APPROLENAME": "live-secret", "OTHER": "live-other"})},
	})

	want := map[string]any{"datarestPgInternal": map[string]any{"general": map[string]any{"other": "live-other"}}}
	if got := patchValues(t, result); !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v (general.identity excluded and pruned)", got, want)
	}
}

func TestChartDiff_ExcludedDifferenceIsReportedNotPatched(t *testing.T) {
	docs := singleWorkload(map[string]any{"APPROLENAME": "rendered", "OTHER": "rendered-other", "SAME": "same"})
	files := []ChartFile{{Values: map[string]string{
		"APPROLENAME": "datarestPgInternal.general.identity.approlename",
		"OTHER":       "datarestPgInternal.general.other",
		"SAME":        "datarestPgInternal.general.identity.same",
	}}}
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "hr",
		Exclude: []string{"spec.values.datarestPgInternal.general.identity"},
		Live: []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{
			"APPROLENAME": "live-role", "OTHER": "live-other", "SAME": "same",
		})},
	})

	want := []ExcludedDiff{{
		Workload: "app", Name: "APPROLENAME", Rendered: "rendered", Live: "live-role",
		Path: "datarestPgInternal.general.identity.approlename",
	}}
	if !reflect.DeepEqual(result.Excluded, want) {
		t.Errorf("Excluded = %+v, want %+v (only the excluded variable that differs)", result.Excluded, want)
	}
	// The policy is unchanged: the excluded value still never reaches the patch.
	wantValues := map[string]any{"datarestPgInternal": map[string]any{"general": map[string]any{"other": "live-other"}}}
	if got := patchValues(t, result); !reflect.DeepEqual(got, wantValues) {
		t.Errorf("values = %v, want %v", got, wantValues)
	}
}

// An excluded path two sibling workloads read comes out once per workload,
// ordered by workload then name — and a variable that is excluded for one
// workload's path only is reported there only.
func TestChartDiff_ExcludedIsPerWorkloadAndSorted(t *testing.T) {
	docs := siblingsRendered()
	files := scanFiles(t, "testdata/siblings")
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "rel",
		Exclude: []string{"spec.values.api.identity", "spec.values.ui.identity"},
		Live: []LiveWorkloadEnv{
			liveFor(docs, "rel-ui", map[string]string{"VAULT_ROLE": "ui-legacy"}),
			liveFor(docs, "rel-api", map[string]string{"VAULT_ROLE": "api-legacy"}),
		},
	})

	want := []ExcludedDiff{
		{Workload: "rel-api", Name: "VAULT_ROLE", Rendered: "rel_rel-api", Live: "api-legacy", Path: "api.identity.approlename"},
		{Workload: "rel-ui", Name: "VAULT_ROLE", Rendered: "rel_rel-ui", Live: "ui-legacy", Path: "ui.identity.approlename"},
	}
	if !reflect.DeepEqual(result.Excluded, want) {
		t.Errorf("Excluded = %+v, want %+v", result.Excluded, want)
	}
	if result.Patch != nil {
		t.Errorf("Patch = %+v, want nil: everything that differs is excluded", result.Patch)
	}
}

// Two workloads reading one excluded path are reported in workload order,
// whatever order the live side was given in.
func TestChartDiff_ExcludedIsSortedByWorkload(t *testing.T) {
	docs := siblingsRendered()
	files := scanFiles(t, "testdata/siblings")
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "rel", Exclude: []string{"spec.values.common"},
		Live: []LiveWorkloadEnv{
			liveFor(docs, "rel-ui", map[string]string{"TENANT": "globex"}),
			liveFor(docs, "rel-api", map[string]string{"TENANT": "acme"}),
		},
	})
	var workloads []string
	for _, e := range result.Excluded {
		workloads = append(workloads, e.Workload)
	}
	if !reflect.DeepEqual(workloads, []string{"rel-api", "rel-ui"}) {
		t.Errorf("Excluded workloads = %v, want [rel-api rel-ui]", workloads)
	}
}

// Excluded is ordered by workload and name, not by the order of the paths
// the variables map to.
func TestChartDiff_ExcludedIsSortedByNameNotPath(t *testing.T) {
	docs := singleWorkload(map[string]any{"A_VAR": "ra", "B_VAR": "rb"})
	files := []ChartFile{{Values: map[string]string{"A_VAR": "z.last", "B_VAR": "a.first"}}}
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "hr", Exclude: []string{"spec.values.z", "spec.values.a"},
		Live: []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"A_VAR": "la", "B_VAR": "lb"})},
	})
	var names []string
	for _, e := range result.Excluded {
		names = append(names, e.Name)
	}
	if !reflect.DeepEqual(names, []string{"A_VAR", "B_VAR"}) {
		t.Errorf("Excluded names = %v, want [A_VAR B_VAR]", names)
	}
}

// Excluding a path whose live value already equals the rendered one hides
// nothing, so nothing is reported.
func TestChartDiff_ExcludedPathThatMatchesIsNotReported(t *testing.T) {
	docs := singleWorkload(map[string]any{"APPROLENAME": "same"})
	files := []ChartFile{{Values: map[string]string{"APPROLENAME": "x.identity.approlename"}}}
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "hr", Exclude: []string{"spec.values.x.identity"},
		Live: []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"APPROLENAME": "same"})},
	})
	if len(result.Excluded) != 0 {
		t.Errorf("Excluded = %+v, want none", result.Excluded)
	}
}

func TestChartDiff_NothingExcludedLeavesExcludedEmpty(t *testing.T) {
	docs := singleWorkload(map[string]any{"OTHER": "rendered-other"})
	files := []ChartFile{{Values: map[string]string{"OTHER": "datarestPgInternal.general.other"}}}
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "hr",
		Exclude: []string{"spec.values.datarestPgInternal.general.identity"},
		Live:    []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"OTHER": "live-other"})},
	})
	if len(result.Excluded) != 0 {
		t.Errorf("Excluded = %+v, want none", result.Excluded)
	}
}

// siblingsRendered is a genai-shaped render: two sibling workloads, each
// reading its own ConfigMap (built from its own env-vars file, both
// setting VAULT_ROLE and LOG_LEVEL) plus one ConfigMap they share.
func siblingsRendered() []*unstructured.Unstructured {
	withConfig := func(name string, configMaps ...string) *unstructured.Unstructured {
		var envFrom []any
		for _, cm := range configMaps {
			envFrom = append(envFrom, map[string]any{"configMapRef": map[string]any{"name": cm}})
		}
		return deploymentDoc(name, []any{map[string]any{"name": "main", "envFrom": envFrom}})
	}
	return []*unstructured.Unstructured{
		configMapDoc("rel-api-config", map[string]any{"VAULT_ROLE": "rel_rel-api", "LOG_LEVEL": "INFO", "API_PORT": "8080"}),
		configMapDoc("rel-ui-config", map[string]any{"VAULT_ROLE": "rel_rel-ui", "LOG_LEVEL": "INFO", "UI_THEME": "light"}),
		configMapDoc("rel-shared-config", map[string]any{"TENANT": "default"}),
		withConfig("rel-api", "rel-shared-config", "rel-api-config"),
		withConfig("rel-ui", "rel-shared-config", "rel-ui-config"),
	}
}

func TestChartDiff_SiblingsPatchTheirOwnPaths(t *testing.T) {
	docs := siblingsRendered()
	files := scanFiles(t, "testdata/siblings")

	cases := []struct {
		name string
		live []LiveWorkloadEnv
		want map[string]any
	}{
		{
			name: "both siblings live",
			live: []LiveWorkloadEnv{
				liveFor(docs, "rel-api", map[string]string{"VAULT_ROLE": "legacy-api", "LOG_LEVEL": "INFO", "API_PORT": "8080", "TENANT": "default"}),
				liveFor(docs, "rel-ui", map[string]string{"VAULT_ROLE": "legacy-ui", "LOG_LEVEL": "DEBUG", "UI_THEME": "light", "TENANT": "default"}),
			},
			want: map[string]any{
				"api": map[string]any{"identity": map[string]any{"approlename": "legacy-api"}},
				"ui": map[string]any{
					"identity": map[string]any{"approlename": "legacy-ui"},
					"log":      map[string]any{"level": "DEBUG"},
				},
			},
		},
		{
			// The case that wrote genai-api's role into genaiUi: only one
			// sibling live, so nothing may land under the other's root.
			name: "only one sibling live",
			live: []LiveWorkloadEnv{
				liveFor(docs, "rel-api", map[string]string{"VAULT_ROLE": "legacy-api", "LOG_LEVEL": "WARN", "API_PORT": "8080", "TENANT": "default"}),
			},
			want: map[string]any{"api": map[string]any{
				"identity": map[string]any{"approlename": "legacy-api"},
				"log":      map[string]any{"level": "WARN"},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := ChartDiff(ChartDiffInput{Rendered: docs, Files: files, HRName: "rel", Live: tc.live})
			if got := patchValues(t, result); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("values = %v, want %v", got, tc.want)
			}
			if len(result.UnmappedDiffs) != 0 {
				t.Errorf("UnmappedDiffs = %+v, want none", result.UnmappedDiffs)
			}
		})
	}
}

func TestChartDiff_SharedConfigMap(t *testing.T) {
	docs := siblingsRendered()
	files := scanFiles(t, "testdata/siblings")

	agree := ChartDiff(ChartDiffInput{Rendered: docs, Files: files, HRName: "rel", Live: []LiveWorkloadEnv{
		liveFor(docs, "rel-api", map[string]string{"TENANT": "acme"}),
		liveFor(docs, "rel-ui", map[string]string{"TENANT": "acme"}),
	}})
	want := map[string]any{"common": map[string]any{"tenant": "acme"}}
	if got := patchValues(t, agree); !reflect.DeepEqual(got, want) {
		t.Errorf("agreeing siblings: values = %v, want %v", got, want)
	}

	disagree := ChartDiff(ChartDiffInput{Rendered: docs, Files: files, HRName: "rel", Live: []LiveWorkloadEnv{
		liveFor(docs, "rel-api", map[string]string{"TENANT": "acme"}),
		liveFor(docs, "rel-ui", map[string]string{"TENANT": "globex"}),
	}})
	if disagree.Patch != nil {
		t.Errorf("disagreeing siblings: Patch = %+v, want nil (one path can't carry both values)", disagree.Patch)
	}
	wantUnmapped := []UnmappedDiff{
		{Workload: "rel-api", Name: "TENANT", Rendered: "default", Live: "acme", Reason: UnmappedConflict, Candidates: []string{"common.tenant"}},
		{Workload: "rel-ui", Name: "TENANT", Rendered: "default", Live: "globex", Reason: UnmappedConflict, Candidates: []string{"common.tenant"}},
	}
	if !reflect.DeepEqual(disagree.UnmappedDiffs, wantUnmapped) {
		t.Errorf("UnmappedDiffs = %+v, want %+v", disagree.UnmappedDiffs, wantUnmapped)
	}
}

func TestChartDiff_FlatLiveSideReportsAmbiguity(t *testing.T) {
	docs := siblingsRendered()
	files := scanFiles(t, "testdata/siblings")
	// A live side that doesn't say which workload a value came from (a
	// legacy flat backup) is compared against every rendered workload.
	flat := []LiveWorkloadEnv{{Env: map[string]string{"VAULT_ROLE": "legacy", "TENANT": "acme", "UI_THEME": "dark"}}}

	result := ChartDiff(ChartDiffInput{Rendered: docs, Files: files, HRName: "rel", Live: flat})

	want := map[string]any{
		"common": map[string]any{"tenant": "acme"}, // one path whichever workload set it
		"ui":     map[string]any{"theme": "dark"},  // only one workload sets it
	}
	if got := patchValues(t, result); !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
	wantUnmapped := []UnmappedDiff{{
		Name: "VAULT_ROLE", Rendered: "rel_rel-ui", Live: "legacy", Reason: UnmappedAmbiguous,
		Candidates: []string{"api.identity.approlename", "ui.identity.approlename"},
	}}
	if !reflect.DeepEqual(result.UnmappedDiffs, wantUnmapped) {
		t.Errorf("UnmappedDiffs = %+v, want %+v", result.UnmappedDiffs, wantUnmapped)
	}

	// With every candidate excluded, the ambiguity is moot.
	excluded := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "rel", Live: flat,
		Exclude: []string{"spec.values.api.identity", "spec.values.ui.identity.approlename"},
	})
	if len(excluded.UnmappedDiffs) != 0 {
		t.Errorf("UnmappedDiffs = %+v, want none once every candidate is excluded", excluded.UnmappedDiffs)
	}
}

func TestChartDiff_AttributedFileWithoutMappingIsUnmapped(t *testing.T) {
	// OWN_LITERAL is a literal in its own file; the sibling file mapping a
	// same-named key belongs to a different ConfigMap and mustn't be used.
	files := []ChartFile{
		{Path: "a_env_vars.yaml", Keys: map[string]bool{"OWN_LITERAL": true}, Values: map[string]string{}},
		{Path: "b_env_vars.yaml", Keys: map[string]bool{"OWN_LITERAL": true, "B": true}, Values: map[string]string{"OWN_LITERAL": "b.value"}},
	}
	docs := []*unstructured.Unstructured{
		configMapDoc("a-config", map[string]any{"OWN_LITERAL": "rendered"}),
		deploymentDoc("a", []any{map[string]any{"name": "main", "envFrom": []any{
			map[string]any{"configMapRef": map[string]any{"name": "a-config"}},
		}}}),
		deploymentDoc("b", []any{map[string]any{"name": "main"}}),
	}
	result := ChartDiff(ChartDiffInput{Rendered: docs, Files: files, HRName: "hr",
		Live: []LiveWorkloadEnv{liveFor(docs, "a", map[string]string{"OWN_LITERAL": "live"})}})

	if result.Patch != nil {
		t.Errorf("Patch = %+v, want nil", result.Patch)
	}
	want := []UnmappedDiff{{Workload: "a", Name: "OWN_LITERAL", Rendered: "rendered", Live: "live", Reason: UnmappedNoPath}}
	if !reflect.DeepEqual(result.UnmappedDiffs, want) {
		t.Errorf("UnmappedDiffs = %+v, want %+v", result.UnmappedDiffs, want)
	}
}

func TestChartDiff_UnattributedAmbiguousKeyNotGuessed(t *testing.T) {
	// A single workload whose ConfigMap matches no file exactly: its keys
	// fall back to every file's mapping, and two different paths means
	// nothing may be guessed.
	docs := singleWorkload(map[string]any{"VAULT_ROLE": "rendered"})
	files := scanFiles(t, "testdata/siblings")

	result := ChartDiff(ChartDiffInput{Rendered: docs, Files: files, HRName: "hr",
		Live: []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"VAULT_ROLE": "live"})}})

	if result.Patch != nil {
		t.Errorf("Patch = %+v, want nil", result.Patch)
	}
	want := []UnmappedDiff{{
		Workload: "app", Name: "VAULT_ROLE", Rendered: "rendered", Live: "live", Reason: UnmappedAmbiguous,
		Candidates: []string{"api.identity.approlename", "ui.identity.approlename"},
	}}
	if !reflect.DeepEqual(result.UnmappedDiffs, want) {
		t.Errorf("UnmappedDiffs = %+v, want %+v", result.UnmappedDiffs, want)
	}

	// A preferred root settles it.
	rooted := ChartDiff(ChartDiffInput{Rendered: docs, Files: files, HRName: "hr", ValuesRoot: "ui",
		Live: []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"VAULT_ROLE": "live"})}})
	wantValues := map[string]any{"ui": map[string]any{"identity": map[string]any{"approlename": "live"}}}
	if got := patchValues(t, rooted); !reflect.DeepEqual(got, wantValues) {
		t.Errorf("with ValuesRoot: values = %v, want %v", got, wantValues)
	}
}

func TestChartDiff_WorkloadsExposeBothSides(t *testing.T) {
	docs := siblingsRendered()
	result := ChartDiff(ChartDiffInput{Rendered: docs, HRName: "rel", Live: []LiveWorkloadEnv{
		liveFor(docs, "rel-ui", map[string]string{"VAULT_ROLE": "legacy-ui"}),
	}})
	want := []WorkloadComparison{{
		Workload: "rel-ui",
		Rendered: map[string]string{"VAULT_ROLE": "rel_rel-ui", "LOG_LEVEL": "INFO", "UI_THEME": "light", "TENANT": "default"},
		Live:     map[string]string{"VAULT_ROLE": "legacy-ui"},
	}}
	if !reflect.DeepEqual(result.Workloads, want) {
		t.Errorf("Workloads = %+v, want %+v", result.Workloads, want)
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
