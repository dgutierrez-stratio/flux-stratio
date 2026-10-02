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
// through ROCKET_VERSION. A diff from a flat baseline (no Workload) is
// settled only when every patched workload setting the variable renders
// the live value.
func SettledByPatch(unmapped []UnmappedDiff, patched []*unstructured.Unstructured) []UnmappedDiff {
	r := indexRendered(patched)
	envs := map[string]map[string]renderedVar{}
	for _, w := range r.workloads {
		envs[w.GetName()] = r.workloadEnv(w)
	}
	var remaining []UnmappedDiff
	for _, u := range unmapped {
		if !settled(u, envs) {
			remaining = append(remaining, u)
		}
	}
	return remaining
}

func settled(u UnmappedDiff, envs map[string]map[string]renderedVar) bool {
	if u.Workload != "" {
		v, ok := envs[u.Workload][u.Name]
		return ok && v.Value == u.Live
	}
	found := false
	for _, env := range envs {
		if v, ok := env[u.Name]; ok {
			if v.Value != u.Live {
				return false
			}
			found = true
		}
	}
	return found
}

// VerifyMapped checks every variable result's patch sets through a mapped
// .Values path against patched — the chart rendered again with that patch
// applied. One that still doesn't render its live value (a template that
// does more with the value than chartValueLineRe could see) has its path
// taken back out of the patch and is reported as UnmappedNotReproduced
// instead: a value the patch can't reproduce is never written. Patch
// becomes nil if nothing is left in it.
func VerifyMapped(result *ChartDiffResult, patched []*unstructured.Unstructured) {
	if result.Patch == nil || len(result.Mapped) == 0 {
		return
	}
	r := indexRendered(patched)
	envs := map[string]map[string]renderedVar{}
	for _, w := range r.workloads {
		envs[w.GetName()] = r.workloadEnv(w)
	}
	failed := map[string]bool{}
	for _, m := range result.Mapped {
		u := UnmappedDiff{Workload: m.Workload, Name: m.Name, Rendered: m.Rendered, Live: m.Live}
		if !settled(u, envs) {
			failed[m.Path] = true
		}
	}
	if len(failed) == 0 {
		return
	}
	obj, _ := result.Patch.Patch.(map[string]any)
	var kept []MappedVar
	for _, m := range result.Mapped {
		if !failed[m.Path] {
			kept = append(kept, m)
			continue
		}
		removeDotPath(obj, "spec.values."+m.Path)
		result.UnmappedDiffs = append(result.UnmappedDiffs, UnmappedDiff{
			Workload: m.Workload, Name: m.Name, Rendered: m.Rendered, Live: m.Live,
			Reason: UnmappedNotReproduced, Candidates: []string{m.Path},
		})
	}
	result.Mapped = kept
	sortUnmapped(result.UnmappedDiffs)
	pruneEmptyMaps(obj)
	if len(PatchValues(result.Patch)) == 0 {
		result.Patch = nil
	}
}
