package diff

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func configMapDoc(name string, data map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": name},
		"data":     data,
	}}
}

func deploymentDoc(name string, containers []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{"containers": containers},
			},
		},
	}}
}

func workloadEnvOf(docs []*unstructured.Unstructured, name string) map[string]renderedVar {
	r := indexRendered(docs)
	for _, w := range r.workloads {
		if w.GetName() == name {
			return r.workloadEnv(w)
		}
	}
	return nil
}

func TestWorkloadEnv_EnvFromThenEnv(t *testing.T) {
	docs := []*unstructured.Unstructured{
		configMapDoc("base", map[string]any{"A": "from-base", "B": "from-base"}),
		configMapDoc("override", map[string]any{"B": "from-override"}),
		deploymentDoc("app", []any{map[string]any{"name": "main",
			"envFrom": []any{
				map[string]any{"configMapRef": map[string]any{"name": "base"}},
				map[string]any{"configMapRef": map[string]any{"name": "override"}},
				map[string]any{"configMapRef": map[string]any{"name": "base"}, "prefix": "P_"},
			},
			"env": []any{map[string]any{"name": "A", "value": "direct"}},
		}}),
	}
	want := map[string]renderedVar{
		"A":   {Value: "direct", Key: "A"},
		"B":   {Value: "from-override", ConfigMap: "override", Key: "B"},
		"P_A": {Value: "from-base", ConfigMap: "base", Key: "A"},
		"P_B": {Value: "from-base", ConfigMap: "base", Key: "B"},
	}
	if got := workloadEnvOf(docs, "app"); !reflect.DeepEqual(got, want) {
		t.Errorf("workloadEnv = %+v, want %+v", got, want)
	}
}

func TestWorkloadEnv_ValueFromVariants(t *testing.T) {
	docs := []*unstructured.Unstructured{
		configMapDoc("cm", map[string]any{"k2": "resolved"}),
		deploymentDoc("app", []any{map[string]any{"name": "main", "env": []any{
			map[string]any{"name": "A", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "status.podIP"}}},
			map[string]any{"name": "B", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "s", "key": "k"}}},
			map[string]any{"name": "C", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "cm", "key": "k2"}}},
			map[string]any{"name": "D", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "not-rendered", "key": "k"}}},
		}}}),
	}
	got := workloadEnvOf(docs, "app")
	want := map[string]renderedVar{
		"A": {Value: "<fieldRef:status.podIP>", Key: "A"},
		"B": {Value: "<secret:s/k>", Key: "B"},
		"C": {Value: "resolved", ConfigMap: "cm", Key: "k2"},
		"D": {Value: "<configMap:not-rendered/k>", Key: "D"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("workloadEnv = %+v, want %+v", got, want)
	}
}

func TestWorkloadEnv_ConcreteNotReplacedByPlaceholder(t *testing.T) {
	docs := []*unstructured.Unstructured{
		configMapDoc("cm", map[string]any{"X": "concrete"}),
		deploymentDoc("app", []any{map[string]any{"name": "main",
			"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "cm"}}},
			"env":     []any{map[string]any{"name": "X", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "s", "key": "k"}}}},
		}}),
	}
	if got := workloadEnvOf(docs, "app")["X"].Value; got != "concrete" {
		t.Errorf("X = %q, want the concrete value preserved", got)
	}
}

func TestWorkloadEnv_UnreferencedConfigMaps(t *testing.T) {
	orphan := configMapDoc("orphan", map[string]any{"O": "v"})

	// A lone workload gets them: they can only be its.
	single := []*unstructured.Unstructured{orphan, deploymentDoc("app", []any{map[string]any{"name": "main"}})}
	if got := workloadEnvOf(single, "app")["O"]; got.Value != "v" || got.ConfigMap != "orphan" {
		t.Errorf("single workload: O = %+v, want the unreferenced ConfigMap's value", got)
	}

	// Siblings don't: nothing says whose they are.
	siblings := []*unstructured.Unstructured{orphan,
		deploymentDoc("a", []any{map[string]any{"name": "main"}}),
		deploymentDoc("b", []any{map[string]any{"name": "main"}}),
	}
	if got, ok := workloadEnvOf(siblings, "a")["O"]; ok {
		t.Errorf("sibling workloads: O = %+v, want it left out", got)
	}
}

func TestIndexRendered_IgnoresOtherKinds(t *testing.T) {
	docs := []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "x"},
	}}}
	if r := indexRendered(docs); len(r.workloads) != 0 || len(r.configMaps) != 0 {
		t.Errorf("indexRendered = %+v, want empty", r)
	}
}
