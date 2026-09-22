package diff

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// EnvVarEntry is one environment variable found while scanning a chart's
// rendered manifests.
type EnvVarEntry struct {
	Name, Value, Source string
}

// CollectRenderedEnvVars scans docs — the output of `helm template` — for
// ConfigMap data entries and container env entries.
//
// Two things are deliberately simpler here than internal/envvars.Extract,
// which resolves a *live* workload's env vars: envFrom is not expanded
// (only a container's own env[] entries are read), and a valueFrom
// reference is rendered as a "<...>" placeholder string rather than
// resolved, exactly like an unresolved fieldRef already is — there is no
// live cluster to resolve a rendered chart's secretKeyRef/configMapKeyRef
// against, so this side of a diff can only ever compare "does the chart
// still reference the same key", never the key's actual value. Ported
// from the Python client's own _collect_env_vars, which has the same
// asymmetry for the same reason.
func CollectRenderedEnvVars(docs []*unstructured.Unstructured) []EnvVarEntry {
	var out []EnvVarEntry
	for _, d := range docs {
		switch d.GetKind() {
		case "ConfigMap":
			data, _, _ := unstructured.NestedStringMap(d.Object, "data")
			for k, v := range data {
				out = append(out, EnvVarEntry{Name: k, Value: v, Source: d.GetName()})
			}
		case "Deployment", "StatefulSet", "DaemonSet":
			out = append(out, collectContainerEnv(d)...)
		}
	}
	return out
}

func collectContainerEnv(d *unstructured.Unstructured) []EnvVarEntry {
	containers, _, _ := unstructured.NestedSlice(d.Object, "spec", "template", "spec", "containers")
	var out []EnvVarEntry
	for _, c := range containers {
		container, ok := c.(map[string]any)
		if !ok {
			continue
		}
		name, _ := container["name"].(string)
		source := "container:" + name

		env, _, _ := unstructured.NestedSlice(container, "env")
		for _, e := range env {
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			varName, _ := entry["name"].(string)
			if varName == "" {
				continue
			}
			out = append(out, EnvVarEntry{Name: varName, Value: renderedEnvValue(entry), Source: source})
		}
	}
	return out
}

func renderedEnvValue(entry map[string]any) string {
	if v, ok := entry["value"].(string); ok {
		return v
	}
	valueFrom, _ := entry["valueFrom"].(map[string]any)
	if valueFrom == nil {
		return ""
	}
	if ref, ok := valueFrom["fieldRef"].(map[string]any); ok {
		path, _ := ref["fieldPath"].(string)
		return "<fieldRef:" + path + ">"
	}
	if ref, ok := valueFrom["secretKeyRef"].(map[string]any); ok {
		return "<secret:" + refKey(ref) + ">"
	}
	if ref, ok := valueFrom["configMapKeyRef"].(map[string]any); ok {
		return "<configMap:" + refKey(ref) + ">"
	}
	return "<valueFrom:unknown>"
}

func refKey(ref map[string]any) string {
	name, _ := ref["name"].(string)
	key, _ := ref["key"].(string)
	return name + "/" + key
}
