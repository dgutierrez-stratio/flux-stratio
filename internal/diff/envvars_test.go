package diff

import (
	"reflect"
	"strings"
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
		"A":   {Value: "direct", Key: "A", Container: "main"},
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
		"A": {Value: "<fieldRef:status.podIP>", Key: "A", Container: "main"},
		"B": {Value: "<secret:s/k>", Key: "B", Container: "main"},
		"C": {Value: "resolved", ConfigMap: "cm", Key: "k2"},
		"D": {Value: "<configMap:not-rendered/k>", Key: "D", Container: "main"},
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

func TestIsPlaceholder(t *testing.T) {
	for v, want := range map[string]bool{
		"<secret:db-creds/password>":     true,
		"<fieldRef:status.podIP>":        true,
		"<unresolved:ConfigMap-ns/cm/k>": true,
		"<status.podIP>":                 true,
		"<root><child>1</child></root>":  false,
		"<?xml version=\"1.0\"?><a/>":    false,
		"<not closed":                    false,
		"":                               false,
		"<>":                             true,
		"plain":                          false,
		"x<secret:a/b>":                  false,
	} {
		if got := isPlaceholder(v); got != want {
			t.Errorf("isPlaceholder(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestRenderedEnvValue_NonStringScalars(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  string
	}{{"x", "x"}, {int64(8080), "8080"}, {float64(1.5), "1.5"}, {true, "true"}, {nil, ""}} {
		entry := map[string]any{"name": "N"}
		if tc.value != nil {
			entry["value"] = tc.value
		}
		if got := renderedEnvValue(entry); got != tc.want {
			t.Errorf("renderedEnvValue(value=%#v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

// A chart renders `KEY: {{ .Values.x | quote }}` as `KEY:` (null) when
// .Values.x is nil. The API server stores "" for it, so the plugin must read
// it as "" and keep the ConfigMap's other keys (the genai-ui-config case).
func TestIndexRendered_NullConfigMapValue(t *testing.T) {
	docs := []*unstructured.Unstructured{
		configMapDoc("ui-config", map[string]any{"A": "a", "TRANSLATE_LITELLM_MODEL": nil, "EMPTY": ""}),
		deploymentDoc("ui", []any{map[string]any{"name": "ui",
			"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "ui-config"}}},
		}}),
	}
	got := workloadEnvOf(docs, "ui")
	want := map[string]renderedVar{
		"A":                       {Value: "a", ConfigMap: "ui-config", Key: "A"},
		"TRANSLATE_LITELLM_MODEL": {Value: "", ConfigMap: "ui-config", Key: "TRANSLATE_LITELLM_MODEL"},
		"EMPTY":                   {Value: "", ConfigMap: "ui-config", Key: "EMPTY"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("workloadEnv = %+v, want %+v", got, want)
	}
}

func TestStringMapAt(t *testing.T) {
	obj := func(data any) map[string]any { return map[string]any{"data": data} }
	cases := []struct {
		name      string
		obj       map[string]any
		want      map[string]string
		wantFound bool
		wantErr   bool
	}{
		{"strings", obj(map[string]any{"A": "a"}), map[string]string{"A": "a"}, true, false},
		{"null is empty", obj(map[string]any{"A": "a", "B": nil}), map[string]string{"A": "a", "B": ""}, true, false},
		{"scalars keep their text", obj(map[string]any{"N": int64(8080), "F": 1.5, "T": true}), map[string]string{"N": "8080", "F": "1.5", "T": "true"}, true, false},
		{"nested map is an error", obj(map[string]any{"A": map[string]any{"x": "y"}}), nil, false, true},
		{"absent", map[string]any{}, nil, false, false},
		{"data itself null", obj(nil), nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := stringMapAt(tc.obj, "data")
			if (err != nil) != tc.wantErr || found != tc.wantFound || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v found=%v err=%v, want %v found=%v wantErr=%v", got, found, err, tc.want, tc.wantFound, tc.wantErr)
			}
		})
	}
}

func TestNullKeys(t *testing.T) {
	obj := map[string]any{"data": map[string]any{"B": nil, "A": nil, "C": "c"}}
	if got, want := nullKeys(obj, "data"), []string{"A", "B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nullKeys = %v, want %v", got, want)
	}
}

func TestNullConfigMapKeys(t *testing.T) {
	docs := []*unstructured.Unstructured{
		configMapDoc("ui-config", map[string]any{"B": nil, "A": nil, "C": "c"}),
		configMapDoc("clean", map[string]any{"X": "x", "EMPTY": ""}),
		deploymentDoc("ui", nil),
	}
	got := NullConfigMapKeys(docs)
	want := map[string][]string{"ui-config": {"A", "B"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NullConfigMapKeys = %v, want %v", got, want)
	}
}

// A map or a list as a ConfigMap value is rejected by the API server: the
// chart would not install, so it is an error rather than an empty ConfigMap.
func TestRenderedConfigMapErrors(t *testing.T) {
	good := configMapDoc("ok", map[string]any{"A": "a", "N": nil, "P": int64(8080), "T": true})
	bad := configMapDoc("bad", map[string]any{"A": map[string]any{"nested": "x"}})
	if errs := RenderedConfigMapErrors([]*unstructured.Unstructured{good}); len(errs) != 0 {
		t.Errorf("errors for a valid ConfigMap = %v, want none", errs)
	}
	errs := RenderedConfigMapErrors([]*unstructured.Unstructured{good, bad})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "rendered ConfigMap bad") || !strings.Contains(errs[0].Error(), "API server rejects it") {
		t.Errorf("errors = %v, want one naming ConfigMap bad and the API server", errs)
	}
}
