package diff

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// inlineSource is the values entry a container's own env var is rendered
// from: the bjw-s controllers.<controller>.containers.<container>.env a
// HelmRelease sets, as a keos-apps size overlay does with rocket's
// SPARTA_BOOTSTRAP_* resources. No chart env file sets such a variable,
// and it overrides any ConfigMap setting the same name, so it's patched
// there or not at all.
type inlineSource struct {
	// path is the env's dot-path under spec.values.
	path string
	// list is set for env's list form ([{name, value}]), which a patch
	// can only replace whole; the map form ({NAME: value}) merges.
	list bool
}

// controllerLabels are the pod labels bjw-s names a workload's
// controller by, in the order they're trusted.
var controllerLabels = []string{"app.kubernetes.io/controller", "app.kubernetes.io/component"}

// inlineSourceFor finds the values env v was rendered from in workload:
// the controllers.*.containers.<v.Container>.env entry named v.Key with
// v's value exactly. A value the env templates (bjw-s tpl's env values)
// doesn't match and isn't found. Several matches are narrowed to the
// workload's own controller by its labels; nil unless exactly one is
// left.
func inlineSourceFor(values map[string]any, workload *unstructured.Unstructured, v renderedVar) *inlineSource {
	controllers, _ := values["controllers"].(map[string]any)
	matches := map[string]inlineSource{}
	for _, ctrl := range sortedMapKeys(controllers) {
		spec, _ := controllers[ctrl].(map[string]any)
		containers, _ := spec["containers"].(map[string]any)
		container, _ := containers[v.Container].(map[string]any)
		path := fmt.Sprintf("controllers.%s.containers.%s.env", ctrl, v.Container)
		switch env := container["env"].(type) {
		case []any:
			for _, e := range env {
				entry, _ := e.(map[string]any)
				if entry["name"] == v.Key && scalarEquals(entry["value"], v.Value) {
					matches[ctrl] = inlineSource{path: path, list: true}
				}
			}
		case map[string]any:
			if scalarEquals(env[v.Key], v.Value) {
				matches[ctrl] = inlineSource{path: path}
			}
		}
	}
	if len(matches) > 1 && workload != nil {
		labels, _, _ := unstructured.NestedStringMap(workload.Object, "spec", "template", "metadata", "labels")
		for _, label := range controllerLabels {
			if src, ok := matches[labels[label]]; ok {
				return &src
			}
		}
	}
	if len(matches) != 1 {
		return nil
	}
	for _, src := range matches {
		return &src
	}
	return nil
}

// scalarEquals reports whether a values scalar renders as s: an env
// value may be written unquoted (value: 2), which the container still
// sees as "2".
func scalarEquals(v any, s string) bool {
	switch v.(type) {
	case string, bool, int, int64, float64:
		return fmt.Sprint(v) == s
	}
	return false
}

// envSourceFor is the values env of workload's only container, where a
// variable can be pinned to its live value: inline env overrides the
// chart's ConfigMaps, so a variable sharing a .Values path with one the
// patch changes keeps its own value (rocket's cluster.domain feeds both
// KERBEROS_REALM_NAME and PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN, which
// legacy set to different cases). The container must be declared under
// the workload's own controller in values; its env needn't exist yet.
// nil for a workload with several containers, which one pin can't cover.
func envSourceFor(values map[string]any, workload *unstructured.Unstructured) *inlineSource {
	if workload == nil {
		return nil
	}
	containers := containersOf(workload)
	if len(containers) != 1 {
		return nil
	}
	name, _ := containers[0]["name"].(string)
	controllers, _ := values["controllers"].(map[string]any)
	ctrl := ""
	if len(controllers) == 1 {
		for c := range controllers {
			ctrl = c
		}
	} else {
		labels, _, _ := unstructured.NestedStringMap(workload.Object, "spec", "template", "metadata", "labels")
		for _, label := range controllerLabels {
			if _, ok := controllers[labels[label]]; ok {
				ctrl = labels[label]
				break
			}
		}
	}
	spec, _ := controllers[ctrl].(map[string]any)
	declared, _ := spec["containers"].(map[string]any)
	container, ok := declared[name].(map[string]any)
	if !ok {
		return nil
	}
	path := fmt.Sprintf("controllers.%s.containers.%s.env", ctrl, name)
	switch container["env"].(type) {
	case map[string]any:
		return &inlineSource{path: path}
	case []any, nil:
		return &inlineSource{path: path, list: true}
	}
	return nil
}

// inlineEdit is one inline env var's live value to patch in.
type inlineEdit struct {
	mappedValue
	source inlineSource
}

// applyInlineEdits patches edits into patch (spec.values): a list-form env
// is written whole — values' own list with each edited entry's value
// replaced, since a patch replaces a list rather than merging it — and a
// map-form env gets just its edited keys. A name edited to different live
// values (sibling workloads sharing one env) is a conflict, returned for
// review instead.
func applyInlineEdits(patch, values map[string]any, edits []inlineEdit) []UnmappedDiff {
	byPath := map[string]map[string][]inlineEdit{}
	sources := map[string]inlineSource{}
	for _, e := range edits {
		if byPath[e.source.path] == nil {
			byPath[e.source.path] = map[string][]inlineEdit{}
		}
		byPath[e.source.path][e.name] = append(byPath[e.source.path][e.name], e)
		sources[e.source.path] = e.source
	}

	var conflicts []UnmappedDiff
	for _, path := range sortedMapKeys(byPath) {
		live := map[string]string{}
		for _, name := range sortedMapKeys(byPath[path]) {
			entries := byPath[path][name]
			values := make([]mappedValue, len(entries))
			for i, e := range entries {
				values[i] = e.mappedValue
			}
			if distinctLive(values) == 1 {
				live[name] = entries[0].live
				continue
			}
			for _, e := range entries {
				conflicts = append(conflicts, UnmappedDiff{
					Workload: e.workload, Name: e.name, Rendered: e.rendered, Live: e.live,
					Reason: UnmappedConflict, Candidates: []string{path},
				})
			}
		}
		if len(live) == 0 {
			continue
		}
		if !sources[path].list {
			for _, name := range sortedMapKeys(live) {
				setDotPath(patch, path+"."+name, live[name])
			}
			continue
		}
		original, _, _ := unstructured.NestedFieldNoCopy(values, strings.Split(path, ".")...)
		list, _ := original.([]any)
		out := make([]any, 0, len(list)+len(live))
		written := map[string]bool{}
		for _, e := range list {
			entry, _ := e.(map[string]any)
			name, _ := entry["name"].(string)
			if value, ok := live[name]; ok {
				edited := make(map[string]any, len(entry))
				for k, v := range entry {
					edited[k] = v
				}
				edited["value"] = value
				out = append(out, edited)
				written[name] = true
				continue
			}
			out = append(out, e)
		}
		// A pinned variable (see envSourceFor) isn't in the list yet.
		for _, name := range sortedMapKeys(live) {
			if !written[name] {
				out = append(out, map[string]any{"name": name, "value": live[name]})
			}
		}
		setDotPath(patch, path, out)
	}
	sort.SliceStable(conflicts, func(i, j int) bool { return conflicts[i].Name < conflicts[j].Name })
	return conflicts
}
