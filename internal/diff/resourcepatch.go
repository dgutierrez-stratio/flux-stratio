package diff

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ResourceEdit is one container's live resources carried into the patch
// through the bjw-s controllers.<controller>.containers.<container>.resources
// values, so the migrated workload keeps the requests and limits the
// legacy one had instead of the GitOps size overlay's.
type ResourceEdit struct {
	// Kind, Workload and Container name the rendered workload and its
	// container.
	Kind, Workload, Container string
	// Path is the values dot-path the patch sets.
	Path string
	// Diffs are the resource differences the edit carries.
	Diffs []SpecDiff
}

// resourceEdits builds the patch values that carry each compared
// container's live resources: both requests and limits when either
// differs, so a carried limit never lands under a request the overlay
// still sets. A key the render sets but live doesn't (a limit the legacy
// workload never had) is written as null, which Helm reads as "delete the
// default". Containers whose controller can't be told, or whose path the
// catalog excludes, are left to the spec warnings.
func resourceEdits(values map[string]any, in ChartDiffInput) (map[string]any, []ResourceEdit) {
	patch := map[string]any{}
	var edits []ResourceEdit
	for _, lw := range in.Live {
		if lw.Rendered == nil || lw.Live == nil {
			continue
		}
		byContainer := map[string][]SpecDiff{}
		for _, d := range CompareWorkloadSpecs(lw.Rendered, lw.Live) {
			if strings.HasPrefix(d.Field, "resources.") {
				byContainer[d.Container] = append(byContainer[d.Container], d)
			}
		}
		for _, container := range sortedMapKeys(byContainer) {
			ctrl := controllerKeyFor(values, lw.Rendered, container)
			if ctrl == "" {
				continue
			}
			path := fmt.Sprintf("controllers.%s.containers.%s.resources", ctrl, container)
			if isExcluded(path, in.Exclude) {
				continue
			}
			rendered, live := containerNamed(lw.Rendered, container), pairedContainer(lw.Rendered, lw.Live, container)
			if live == nil || rendered == nil {
				continue
			}
			for _, list := range resourceLists {
				r, l := resourceList(rendered, list), resourceList(live, list)
				for _, name := range unionKeys(r, l) {
					if lv, ok := l[name]; ok {
						setDotPath(patch, path+"."+list+"."+name, carriedQuantity(lv))
					} else {
						setDotPath(patch, path+"."+list+"."+name, nil)
					}
				}
			}
			edits = append(edits, ResourceEdit{Kind: lw.Rendered.GetKind(), Workload: lw.Rendered.GetName(), Container: container, Path: path, Diffs: byContainer[container]})
		}
	}
	return patch, edits
}

// containerNamed is w's container called name.
func containerNamed(w *unstructured.Unstructured, name string) map[string]any {
	for _, c := range containersOf(w) {
		if n, _ := c["name"].(string); n == name {
			return c
		}
	}
	return nil
}

// pairedContainer is the container of live CompareWorkloadSpecs pairs with
// rendered's container called name: the only one, when each side has one.
func pairedContainer(rendered, live *unstructured.Unstructured, name string) map[string]any {
	if lc := containersOf(live); len(containersOf(rendered)) == 1 && len(lc) == 1 {
		return lc[0]
	}
	return containerNamed(live, name)
}

// carriedQuantity writes a live quantity the way a person would: Kubernetes
// reports 4096M as 4.096E9, which is the same amount but reads like noise
// in a tenant file.
func carriedQuantity(live string) string {
	q, err := resource.ParseQuantity(live)
	if err != nil || q.Format != resource.DecimalExponent {
		return live
	}
	// A fresh quantity: String() of a parsed one keeps its original spelling.
	return resource.NewMilliQuantity(q.MilliValue(), resource.DecimalSI).String()
}

// controllerKeyFor is the controllers.<key> of values that declares
// container and renders workload: the only controller declaring it, or,
// among several, the one named by the workload's pod labels (see
// controllerLabels). "" when that can't be told.
func controllerKeyFor(values map[string]any, workload *unstructured.Unstructured, container string) string {
	controllers, _ := values["controllers"].(map[string]any)
	var candidates []string
	for _, ctrl := range sortedMapKeys(controllers) {
		spec, _ := controllers[ctrl].(map[string]any)
		containers, _ := spec["containers"].(map[string]any)
		if _, ok := containers[container]; ok {
			candidates = append(candidates, ctrl)
		}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	labels, _, _ := unstructured.NestedStringMap(workload.Object, "spec", "template", "metadata", "labels")
	for _, label := range controllerLabels {
		for _, c := range candidates {
			if labels[label] == c {
				return c
			}
		}
	}
	return ""
}

// VerifyResources checks every carried resource edit against patched, the
// chart rendered again with the patch applied: an edit whose container
// still differs from live (a chart that doesn't read the values path, a
// template that rewrites them) has its path taken back out of the patch,
// like VerifyMapped does for env — a value the patch can't reproduce is
// never written. It returns the edits that did not hold; their diffs stay
// in the spec comparison. Patch becomes nil if nothing is left in it.
func VerifyResources(result *ChartDiffResult, patched []*unstructured.Unstructured, live []LiveWorkloadEnv) []ResourceEdit {
	if result.Patch == nil || len(result.ResourceEdits) == 0 {
		return nil
	}
	byName := map[string]*unstructured.Unstructured{}
	for _, w := range patched {
		byName[w.GetKind()+"/"+w.GetName()] = w
	}
	liveByName := map[string]*unstructured.Unstructured{}
	for _, lw := range live {
		if lw.Rendered != nil && lw.Live != nil {
			liveByName[lw.Rendered.GetKind()+"/"+lw.Rendered.GetName()] = lw.Live
		}
	}
	obj, _ := result.Patch.Patch.(map[string]any)
	var kept, failed []ResourceEdit
	for _, e := range result.ResourceEdits {
		key := e.Kind + "/" + e.Workload
		holds := false
		if w, l := byName[key], liveByName[key]; w != nil && l != nil {
			holds = true
			for _, d := range CompareWorkloadSpecs(w, l) {
				if d.Container == e.Container && strings.HasPrefix(d.Field, "resources.") {
					holds = false
				}
			}
		}
		if holds {
			kept = append(kept, e)
			continue
		}
		removeDotPath(obj, "spec.values."+e.Path)
		failed = append(failed, e)
	}
	result.ResourceEdits = kept
	if len(failed) > 0 {
		pruneEmptyMaps(obj)
		if len(PatchValues(result.Patch)) == 0 {
			result.Patch = nil
		}
	}
	return failed
}
