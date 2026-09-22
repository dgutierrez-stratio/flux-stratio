package diff

import (
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

func TestCollectRenderedEnvVars_ConfigMapData(t *testing.T) {
	docs := []*unstructured.Unstructured{configMapDoc("app-config", map[string]any{"FOO": "bar", "BAZ": "qux"})}
	got := CollectRenderedEnvVars(docs)
	byName := map[string]EnvVarEntry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if byName["FOO"].Value != "bar" || byName["FOO"].Source != "app-config" {
		t.Errorf("FOO entry = %+v", byName["FOO"])
	}
	if byName["BAZ"].Value != "qux" {
		t.Errorf("BAZ entry = %+v", byName["BAZ"])
	}
}

func TestCollectRenderedEnvVars_ContainerDirectValue(t *testing.T) {
	docs := []*unstructured.Unstructured{deploymentDoc("app", []any{
		map[string]any{"name": "main", "env": []any{
			map[string]any{"name": "FOO", "value": "bar"},
		}},
	})}
	got := CollectRenderedEnvVars(docs)
	if len(got) != 1 || got[0].Name != "FOO" || got[0].Value != "bar" || got[0].Source != "container:main" {
		t.Errorf("got = %+v", got)
	}
}

func TestCollectRenderedEnvVars_EnvFromNotExpanded(t *testing.T) {
	// Deliberate asymmetry vs internal/envvars.Extract — see package doc.
	docs := []*unstructured.Unstructured{deploymentDoc("app", []any{
		map[string]any{"name": "main", "envFrom": []any{
			map[string]any{"configMapRef": map[string]any{"name": "should-be-ignored"}},
		}},
	})}
	if got := CollectRenderedEnvVars(docs); len(got) != 0 {
		t.Errorf("got = %+v, want empty (envFrom is not expanded on the rendered side)", got)
	}
}

func TestCollectRenderedEnvVars_ValueFromVariants(t *testing.T) {
	docs := []*unstructured.Unstructured{deploymentDoc("app", []any{
		map[string]any{"name": "main", "env": []any{
			map[string]any{"name": "A", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "status.podIP"}}},
			map[string]any{"name": "B", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "s", "key": "k"}}},
			map[string]any{"name": "C", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "cm", "key": "k2"}}},
		}},
	})}
	got := CollectRenderedEnvVars(docs)
	byName := map[string]string{}
	for _, e := range got {
		byName[e.Name] = e.Value
	}
	if byName["A"] != "<fieldRef:status.podIP>" {
		t.Errorf("A = %q", byName["A"])
	}
	if byName["B"] != "<secret:s/k>" {
		t.Errorf("B = %q", byName["B"])
	}
	if byName["C"] != "<configMap:cm/k2>" {
		t.Errorf("C = %q", byName["C"])
	}
}

func TestCollectRenderedEnvVars_IgnoresOtherKinds(t *testing.T) {
	docs := []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "x"},
	}}}
	if got := CollectRenderedEnvVars(docs); len(got) != 0 {
		t.Errorf("got = %+v, want empty", got)
	}
}
