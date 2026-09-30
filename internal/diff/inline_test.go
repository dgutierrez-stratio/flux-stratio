package diff

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// rocketShaped is rocket's shape: the server ConfigMap (built from a
// chart file mapping SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT to its
// rocketServer path) read through envFrom, and a size overlay's inline
// env setting the same name, which overrides it.
func rocketShaped(env []any) ([]*unstructured.Unstructured, []ChartFile) {
	docs := []*unstructured.Unstructured{
		configMapDoc("rocket-rocket-config", map[string]any{"SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT": "2"}),
		deploymentDoc("rocket", []any{map[string]any{
			"name":    "rocket",
			"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "rocket-rocket-config"}}},
			"env":     env,
		}}),
	}
	files := []ChartFile{{
		Path: "config/rocket_server_env_vars.yaml",
		Keys: map[string]bool{"SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT": true},
		Values: map[string]string{
			"SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT": "rocketServer.environment.workers.catalogService.catalogCpusLimit",
		},
	}}
	return docs, files
}

func overlayEnv() []any {
	return []any{
		map[string]any{"name": "SERVICE_ACCOUNT_NAME", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "spec.serviceAccountName"}}},
		map[string]any{"name": "SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT", "value": "1"},
		map[string]any{"name": "SPARTA_BOOTSTRAP_CATALOG_MEM_REQUEST", "value": "1536"},
	}
}

func TestChartDiff_InlineListEnvPatchedWhole(t *testing.T) {
	docs, files := rocketShaped([]any{map[string]any{"name": "SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT", "value": "1"}})
	values := map[string]any{"controllers": map[string]any{"rocket": map[string]any{
		"containers": map[string]any{"rocket": map[string]any{"env": overlayEnv()}},
	}}}

	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "rocket", Values: values,
		Live: []LiveWorkloadEnv{liveFor(docs, "rocket", map[string]string{"SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT": "2"})},
	})

	if len(result.UnmappedDiffs) != 0 {
		t.Fatalf("UnmappedDiffs = %+v, want none", result.UnmappedDiffs)
	}
	// The whole list, since a patch replaces it: the edited entry has the
	// live value, the others are kept as the overlay set them — and the
	// ConfigMap path the inline value shadows isn't patched.
	wantEnv := overlayEnv()
	wantEnv[1] = map[string]any{"name": "SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT", "value": "2"}
	want := map[string]any{"controllers": map[string]any{"rocket": map[string]any{
		"containers": map[string]any{"rocket": map[string]any{"env": wantEnv}},
	}}}
	if got := patchValues(t, result); !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
	if env := overlayEnv(); !reflect.DeepEqual(values["controllers"].(map[string]any)["rocket"].(map[string]any)["containers"].(map[string]any)["rocket"].(map[string]any)["env"], env) {
		t.Error("ChartDiff modified the input values")
	}
}

func TestChartDiff_InlineMapEnvPatchedByKey(t *testing.T) {
	docs, files := rocketShaped([]any{map[string]any{"name": "SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT", "value": "1"}})
	values := map[string]any{"controllers": map[string]any{"rocket": map[string]any{
		"containers": map[string]any{"rocket": map[string]any{"env": map[string]any{"SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT": 1}}},
	}}}

	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "rocket", Values: values,
		Live: []LiveWorkloadEnv{liveFor(docs, "rocket", map[string]string{"SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT": "2"})},
	})

	want := map[string]any{"controllers": map[string]any{"rocket": map[string]any{
		"containers": map[string]any{"rocket": map[string]any{"env": map[string]any{"SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT": "2"}}},
	}}}
	if got := patchValues(t, result); !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
}

func TestChartDiff_InlineEnvWithoutValuesSourceReported(t *testing.T) {
	// Hardcoded in a chart template: no values env renders it.
	docs, files := rocketShaped([]any{map[string]any{"name": "SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT", "value": "1"}})

	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "rocket",
		Live: []LiveWorkloadEnv{liveFor(docs, "rocket", map[string]string{"SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT": "2"})},
	})

	if result.Patch != nil {
		t.Errorf("Patch = %+v, want none: the ConfigMap path is shadowed by the inline value", result.Patch.Patch)
	}
	want := []UnmappedDiff{{Workload: "rocket", Name: "SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT", Rendered: "1", Live: "2", Reason: UnmappedInline}}
	if !reflect.DeepEqual(result.UnmappedDiffs, want) {
		t.Errorf("UnmappedDiffs = %+v, want %+v", result.UnmappedDiffs, want)
	}
}

func TestChartDiff_InlineEnvExcluded(t *testing.T) {
	docs, files := rocketShaped([]any{map[string]any{"name": "SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT", "value": "1"}})
	values := map[string]any{"controllers": map[string]any{"rocket": map[string]any{
		"containers": map[string]any{"rocket": map[string]any{"env": overlayEnv()}},
	}}}

	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "rocket", Values: values,
		Exclude: []string{"spec.values.controllers.rocket.containers.rocket.env"},
		Live:    []LiveWorkloadEnv{liveFor(docs, "rocket", map[string]string{"SPARTA_BOOTSTRAP_CATALOG_CPU_LIMIT": "2"})},
	})
	if result.Patch != nil {
		t.Errorf("Patch = %+v, want none for an excluded env", result.Patch.Patch)
	}
}

func TestChartDiff_SharedPathWouldChangeAMatchingVariable(t *testing.T) {
	// rocket's cluster.domain: KERBEROS_REALM_NAME differs live, but
	// PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN already matches through the
	// same path with another value.
	docs := singleWorkload(map[string]any{"KERBEROS_REALM_NAME": "eosdev.int", "PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN": "eosdev.int"})
	files := []ChartFile{{Path: "config/env_vars.yaml", Values: map[string]string{
		"KERBEROS_REALM_NAME":                   "cluster.domain",
		"PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN": "cluster.domain",
	}}}

	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "rocket",
		Live: []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{
			"KERBEROS_REALM_NAME": "EOSDEV.INT", "PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN": "eosdev.int",
		})},
	})

	if result.Patch != nil {
		t.Errorf("Patch = %+v, want none: patching cluster.domain would change the Pekko domain", result.Patch.Patch)
	}
	want := []UnmappedDiff{{
		Workload: "app", Name: "KERBEROS_REALM_NAME", Rendered: "eosdev.int", Live: "EOSDEV.INT",
		Reason: UnmappedConflict, Candidates: []string{"cluster.domain"},
		Shared: []string{"PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN=eosdev.int"},
	}}
	if !reflect.DeepEqual(result.UnmappedDiffs, want) {
		t.Errorf("UnmappedDiffs = %+v, want %+v", result.UnmappedDiffs, want)
	}
}

func TestChartDiff_SharedPathMatchingSameValueIsPatched(t *testing.T) {
	docs := singleWorkload(map[string]any{"A": "old", "B": "new"})
	files := []ChartFile{{Path: "config/env_vars.yaml", Values: map[string]string{"A": "x.v", "B": "x.v"}}}

	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "app",
		Live: []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{"A": "new", "B": "new"})},
	})
	if want := map[string]any{"x": map[string]any{"v": "new"}}; !reflect.DeepEqual(patchValues(t, result), want) {
		t.Errorf("values = %v, want %v", patchValues(t, result), want)
	}
}

func TestChartDiff_SharedPathPinsTheMatchingVariableInline(t *testing.T) {
	docs := []*unstructured.Unstructured{
		configMapDoc("rocket-common-config", map[string]any{"PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN": "eosdev.int"}),
		configMapDoc("rocket-hdfs-config", map[string]any{"KERBEROS_REALM_NAME": "eosdev.int"}),
		deploymentDoc("rocket", []any{map[string]any{
			"name": "rocket",
			"envFrom": []any{
				map[string]any{"configMapRef": map[string]any{"name": "rocket-common-config"}},
				map[string]any{"configMapRef": map[string]any{"name": "rocket-hdfs-config"}},
			},
		}}),
	}
	files := []ChartFile{
		{Path: "config/rocket_common_env_vars.yaml", Keys: map[string]bool{"PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN": true},
			Values: map[string]string{"PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN": "cluster.domain"}},
		{Path: "config/storage/rocket_hdfs_env_vars.yaml", Keys: map[string]bool{"KERBEROS_REALM_NAME": true},
			Values: map[string]string{"KERBEROS_REALM_NAME": "cluster.domain"}},
	}
	values := map[string]any{"controllers": map[string]any{"rocket": map[string]any{
		"containers": map[string]any{"rocket": map[string]any{"env": overlayEnv()}},
	}}}

	result := ChartDiff(ChartDiffInput{
		Rendered: docs, Files: files, HRName: "rocket", Values: values,
		Live: []LiveWorkloadEnv{liveFor(docs, "rocket", map[string]string{
			"KERBEROS_REALM_NAME": "EOSDEV.INT", "PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN": "eosdev.int",
		})},
	})

	if len(result.UnmappedDiffs) != 0 {
		t.Fatalf("UnmappedDiffs = %+v, want none", result.UnmappedDiffs)
	}
	// cluster.domain carries the realm's legacy case, and the Pekko domain
	// keeps its own through the container env, which overrides the
	// ConfigMap.
	wantEnv := append(overlayEnv(), map[string]any{"name": "PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN", "value": "eosdev.int"})
	want := map[string]any{
		"cluster":     map[string]any{"domain": "EOSDEV.INT"},
		"controllers": map[string]any{"rocket": map[string]any{"containers": map[string]any{"rocket": map[string]any{"env": wantEnv}}}},
	}
	if got := patchValues(t, result); !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
}
