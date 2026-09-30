package diff

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// PatchValues returns the spec.values a chart-mode patch sets, or nil.
func PatchValues(p *PatchDoc) map[string]any {
	if p == nil {
		return nil
	}
	// Plain map access, not unstructured.NestedMap: that deep-copies,
	// and a patch's coerced values (int, bool) aren't JSON-copyable.
	obj, _ := p.Patch.(map[string]any)
	spec, _ := obj["spec"].(map[string]any)
	values, _ := spec["values"].(map[string]any)
	return values
}

// MergeValues deep-merges src over dst into a new map, the way Flux
// applies a HelmRelease patch's spec.values: nested maps merge, anything
// else in src replaces dst's. Neither input is modified.
func MergeValues(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		srcMap, srcIsMap := v.(map[string]any)
		dstMap, dstIsMap := out[k].(map[string]any)
		if srcIsMap && dstIsMap {
			out[k] = MergeValues(dstMap, srcMap)
			continue
		}
		out[k] = v
	}
	return out
}

// SettledByPatch drops the unmapped diffs the patch settles anyway: with
// the patch applied, the chart (rendered again as patched) already
// renders the live value. That's a variable built from several templates
// rather than one .Values path — rocket's ROCKET_API_DOCKER_IMAGE is
// "<registry>/rocket-api:<image tag>", and the image tag is patched
// through ROCKET_VERSION. A diff from a flat baseline (no Workload) can't
// be checked against one workload, so it's kept.
func SettledByPatch(unmapped []UnmappedDiff, patched []*unstructured.Unstructured) []UnmappedDiff {
	r := indexRendered(patched)
	envs := map[string]map[string]renderedVar{}
	for _, w := range r.workloads {
		envs[w.GetName()] = r.workloadEnv(w)
	}
	var remaining []UnmappedDiff
	for _, u := range unmapped {
		if env, ok := envs[u.Workload]; ok && u.Workload != "" {
			if v, ok := env[u.Name]; ok && v.Value == u.Live {
				continue
			}
		}
		remaining = append(remaining, u)
	}
	return remaining
}
