package diff

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// UnmappedDiff is a variable whose rendered and live values differ but
// which BuildChartValuesMap could not trace back to a .Values path —
// reported for human review rather than silently dropped, since it can't
// be automatically patched.
type UnmappedDiff struct {
	Name, Rendered, Live string
}

// ChartDiffResult is the outcome of ChartDiff.
type ChartDiffResult struct {
	// Patch is the HelmRelease values patch, or nil if nothing to patch.
	Patch *PatchDoc
	// UnmappedDiffs lists variables that differ with no known .Values path.
	UnmappedDiffs []UnmappedDiff
	// LiveOnlyCount is how many variables exist live but not in the
	// chart's rendered output — informational: these values will be lost
	// after migration, since nothing in the chart can carry them forward.
	LiveOnlyCount int
	// RenderedOnlyCount is how many variables the chart renders that have
	// no live counterpart (e.g. new defaults introduced since the app was
	// last deployed).
	RenderedOnlyCount int
	// RenderedEnv is the deduped rendered env vars this comparison used —
	// exposed so a caller can present a full before/after view (e.g.
	// internal/appdiff builds a unified diff from it), not just the
	// mapped subset that became a patch.
	RenderedEnv map[string]string
}

// ChartDiff compares a chart's rendered env vars (renderedDocs, the output
// of HelmTemplate) against a live workload's already-resolved env vars
// (liveEnv, e.g. from internal/envvars.Extract) and returns the
// HelmRelease values patch flux-stratio would splice into the tenant
// YAML. excludePaths are dot-paths rooted at the patch document (e.g.
// "spec.values.datarestPgInternal.general.identity.approlename").
func ChartDiff(renderedDocs []*unstructured.Unstructured, liveEnv map[string]string, valuesMap map[string]string, hrName string, excludePaths []string) *ChartDiffResult {
	rendered := dedupeRenderedEnv(CollectRenderedEnvVars(renderedDocs))

	names := map[string]bool{}
	for k := range rendered {
		names[k] = true
	}
	for k := range liveEnv {
		names[k] = true
	}

	values := map[string]any{}
	result := &ChartDiffResult{RenderedEnv: rendered}

	for _, name := range sortedKeys(names) {
		renderedVal, hasRendered := rendered[name]
		liveVal, hasLive := liveEnv[name]

		switch {
		case !hasLive:
			result.RenderedOnlyCount++
		case !hasRendered:
			result.LiveOnlyCount++
		case strings.HasPrefix(renderedVal, "<") || strings.HasPrefix(liveVal, "<"):
			// An unresolvable placeholder on either side can't be diffed.
		case renderedVal == liveVal:
			// No difference.
		default:
			if valuesPath, ok := valuesMap[name]; ok {
				setDotPath(values, valuesPath, CoerceValue(liveVal))
			} else {
				result.UnmappedDiffs = append(result.UnmappedDiffs, UnmappedDiff{Name: name, Rendered: renderedVal, Live: liveVal})
			}
		}
	}

	patchObj := map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]any{"name": hrName},
		"spec":       map[string]any{"values": values},
	}
	for _, p := range excludePaths {
		removeDotPath(patchObj, p)
	}
	pruneEmptyMaps(patchObj)

	if spec, _ := patchObj["spec"].(map[string]any); len(spec) > 0 {
		if specValues, _ := spec["values"].(map[string]any); len(specValues) > 0 {
			result.Patch = &PatchDoc{TargetKind: "HelmRelease", Patch: patchObj}
		}
	}

	return result
}

// dedupeRenderedEnv collapses CollectRenderedEnvVars' entries — which may
// name the same variable more than once, e.g. a ConfigMap key that a
// container also sets directly — into one value per name. A later entry
// overwrites an earlier one, unless the later value is an unresolvable
// "<...>" placeholder: a concrete value already stored is never silently
// replaced by a placeholder appearing later in doc order (a later
// concrete value, however, does overwrite an earlier concrete one — the
// last container/ConfigMap to set a name wins, matching how the rendered
// manifests would actually behave at apply time).
func dedupeRenderedEnv(entries []EnvVarEntry) map[string]string {
	out := map[string]string{}
	for _, e := range entries {
		if _, exists := out[e.Name]; exists && strings.HasPrefix(e.Value, "<") {
			continue
		}
		out[e.Name] = e.Value
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// setDotPath sets value at dotPath inside root, creating intermediate maps
// as needed.
func setDotPath(root map[string]any, dotPath string, value any) {
	parts := strings.Split(dotPath, ".")
	cur := root
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
}
