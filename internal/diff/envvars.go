package diff

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// workloadKinds are the rendered kinds chart mode compares env vars of.
var workloadKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true}

// renderedVar is one env var a rendered workload's containers would see:
// its value, and where it came from — the rendered ConfigMap it was read
// from and its key there (before any envFrom prefix), or no ConfigMap for
// a container's own env value, which then names its Container.
type renderedVar struct {
	Value     string
	ConfigMap string
	Key       string
	Container string
}

// inline reports whether v is a container's own env value rather than a
// ConfigMap's: no chart env file sets it, and it overrides any envFrom
// ConfigMap setting the same name.
func (v renderedVar) inline() bool { return v.ConfigMap == "" }

// renderedEnv is a chart's rendered output, indexed for resolving each
// workload's env vars.
type renderedEnv struct {
	workloads  []*unstructured.Unstructured
	configMaps map[string]map[string]string
	// orphans are the ConfigMaps (in doc order) no rendered workload
	// references by name.
	orphans []string
}

func indexRendered(docs []*unstructured.Unstructured) renderedEnv {
	r := renderedEnv{configMaps: map[string]map[string]string{}}
	var cmOrder []string
	for _, d := range docs {
		switch {
		case d.GetKind() == "ConfigMap":
			data, _, _ := unstructured.NestedStringMap(d.Object, "data")
			r.configMaps[d.GetName()] = data
			cmOrder = append(cmOrder, d.GetName())
		case workloadKinds[d.GetKind()]:
			r.workloads = append(r.workloads, d)
		}
	}
	referenced := map[string]bool{}
	for _, w := range r.workloads {
		for _, name := range referencedConfigMaps(w) {
			referenced[name] = true
		}
	}
	for _, name := range cmOrder {
		if !referenced[name] {
			r.orphans = append(r.orphans, name)
		}
	}
	return r
}

// workloadEnv resolves the env vars workload's containers would see, with
// the same precedence internal/envvars.Extract resolves a live workload's
// with — containers in order, each one's envFrom and then its env, a later
// entry overwriting an earlier one — reading ConfigMaps from the rendered
// output instead of the cluster.
//
// A chart that renders a single workload also gets every ConfigMap no
// workload references, applied first: chart mode always compared every
// rendered ConfigMap's data, and a lone workload is the only one those
// values can belong to. With sibling workloads they're left out, since
// nothing says which sibling they belong to.
//
// Secrets aren't rendered with real values, so envFrom secretRef isn't
// expanded and a secretKeyRef stays a "<secret:...>" placeholder, like a
// fieldRef — there's no value to compare, only whether the chart still
// references the same key. A concrete value is never replaced by a later
// placeholder.
func (r renderedEnv) workloadEnv(workload *unstructured.Unstructured) map[string]renderedVar {
	out := map[string]renderedVar{}
	set := func(name string, v renderedVar) {
		if prev, ok := out[name]; ok && !isPlaceholder(prev.Value) && isPlaceholder(v.Value) {
			return
		}
		out[name] = v
	}
	applyConfigMap := func(cm, prefix string) {
		for k, v := range r.configMaps[cm] {
			set(prefix+k, renderedVar{Value: v, ConfigMap: cm, Key: k})
		}
	}

	if len(r.workloads) == 1 {
		for _, cm := range r.orphans {
			applyConfigMap(cm, "")
		}
	}
	for _, container := range containersOf(workload) {
		envFrom, _, _ := unstructured.NestedSlice(container, "envFrom")
		for _, e := range envFrom {
			entry, _ := e.(map[string]any)
			ref, _ := entry["configMapRef"].(map[string]any)
			if name, _ := ref["name"].(string); name != "" {
				prefix, _ := entry["prefix"].(string)
				applyConfigMap(name, prefix)
			}
		}
		env, _, _ := unstructured.NestedSlice(container, "env")
		for _, e := range env {
			entry, _ := e.(map[string]any)
			name, _ := entry["name"].(string)
			if name == "" {
				continue
			}
			v := r.envEntryValue(name, entry)
			if v.inline() {
				v.Container, _ = container["name"].(string)
			}
			set(name, v)
		}
	}
	return out
}

// envEntryValue resolves one container env entry: a direct value, a
// configMapKeyRef into a rendered ConfigMap, or a "<...>" placeholder for
// anything the rendered output can't resolve.
func (r renderedEnv) envEntryValue(name string, entry map[string]any) renderedVar {
	valueFrom, _ := entry["valueFrom"].(map[string]any)
	if ref, ok := valueFrom["configMapKeyRef"].(map[string]any); ok {
		cm, _ := ref["name"].(string)
		key, _ := ref["key"].(string)
		if v, ok := r.configMaps[cm][key]; ok {
			return renderedVar{Value: v, ConfigMap: cm, Key: key}
		}
	}
	return renderedVar{Value: renderedEnvValue(entry), Key: name}
}

func containersOf(workload *unstructured.Unstructured) []map[string]any {
	containers, _, _ := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", "containers")
	out := make([]map[string]any, 0, len(containers))
	for _, c := range containers {
		if container, ok := c.(map[string]any); ok {
			out = append(out, container)
		}
	}
	return out
}

// referencedConfigMaps names every ConfigMap workload's containers read
// env vars from (envFrom configMapRef, env configMapKeyRef).
func referencedConfigMaps(workload *unstructured.Unstructured) []string {
	var names []string
	for _, container := range containersOf(workload) {
		envFrom, _, _ := unstructured.NestedSlice(container, "envFrom")
		for _, e := range envFrom {
			entry, _ := e.(map[string]any)
			ref, _ := entry["configMapRef"].(map[string]any)
			if name, _ := ref["name"].(string); name != "" {
				names = append(names, name)
			}
		}
		env, _, _ := unstructured.NestedSlice(container, "env")
		for _, e := range env {
			entry, _ := e.(map[string]any)
			valueFrom, _ := entry["valueFrom"].(map[string]any)
			ref, _ := valueFrom["configMapKeyRef"].(map[string]any)
			if name, _ := ref["name"].(string); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

func isPlaceholder(v string) bool {
	return strings.HasPrefix(v, "<")
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
