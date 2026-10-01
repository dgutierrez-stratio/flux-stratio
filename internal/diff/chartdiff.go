package diff

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// UnmappedReason says why a differing variable couldn't be turned into a
// values patch automatically.
type UnmappedReason string

const (
	// UnmappedNoPath means the chart file the variable comes from doesn't
	// map it to a single .Values path (a literal or computed value), or no
	// chart file does.
	UnmappedNoPath UnmappedReason = "no .Values path"
	// UnmappedAmbiguous means the variable could come from several .Values
	// paths (Candidates) and nothing says which — typically a flat
	// baseline that doesn't record which sibling workload a value came
	// from, or a ConfigMap no chart file could be matched to.
	UnmappedAmbiguous UnmappedReason = "ambiguous"
	// UnmappedConflict means variables read from the same .Values path
	// (Candidates[0]) have different live values — sibling workloads, or
	// another variable that already matches (Shared) — and one path can
	// carry only one of them.
	UnmappedConflict UnmappedReason = "conflicting live values"
	// UnmappedInline means a container sets the variable in its own env,
	// overriding the chart's ConfigMaps, and no values env entry renders
	// it as-is (a hardcoded or templated env value).
	UnmappedInline UnmappedReason = "set inline in the container env"
)

// LiveOnlyVar is a live variable the chart doesn't render.
type LiveOnlyVar struct {
	// Workload is the rendered workload the live side was compared
	// against, or "" for a flat live side (see UnmappedDiff.Workload).
	Workload   string
	Name, Live string
}

// UnmappedDiff is a variable whose rendered and live values differ but
// which ChartDiff could not turn into a patch value — reported for human
// review rather than silently dropped or guessed.
type UnmappedDiff struct {
	// Workload is the rendered workload the variable belongs to, or ""
	// when the live side didn't say (a flat baseline compared against
	// every rendered workload at once).
	Workload             string
	Name, Rendered, Live string
	Reason               UnmappedReason
	// Candidates are the .Values paths involved: every possible one when
	// Reason is UnmappedAmbiguous, the shared one when it's
	// UnmappedConflict.
	Candidates []string
	// Shared, for an UnmappedConflict, are the variables that already
	// match live through the same path ("NAME=live", workload-prefixed
	// when there are several), which patching it would change.
	Shared []string
}

// LiveWorkloadEnv is one live workload's already-resolved env vars (e.g.
// from internal/envvars.Extract), paired with the rendered workload it's
// the live counterpart of.
type LiveWorkloadEnv struct {
	// Rendered is the rendered workload Env is compared against — or nil
	// to compare it against every rendered workload at once, for a live
	// side that doesn't say which workload a value came from (a backup's
	// flat env-vars.env). Any variable more than one of them could have
	// set is then reported as ambiguous rather than guessed.
	Rendered *unstructured.Unstructured
	Env      map[string]string
}

// ChartDiffInput is what ChartDiff compares.
type ChartDiffInput struct {
	// Rendered is the chart's rendered output (HelmTemplate).
	Rendered []*unstructured.Unstructured
	// Live are the live workloads to compare, each paired with its
	// rendered counterpart. A rendered workload with no live counterpart
	// contributes nothing.
	Live []LiveWorkloadEnv
	// Files is ScanChartFiles' result for the chart.
	Files []ChartFile
	// ValuesRoot, when set, is the .Values root a multi-flavor chart's
	// ambiguities resolve to (see AttributeConfigMaps, candidatePaths).
	ValuesRoot string
	// HRName names the HelmRelease the patch targets.
	HRName string
	// Exclude are dot-paths rooted at the patch document (e.g.
	// "spec.values.datarestPgInternal.general.identity.approlename")
	// never written to the patch.
	Exclude []string
	// Values are the values the chart was rendered with (its defaults
	// under the HelmRelease's), where a container's inline env var is
	// looked up to patch it (see inlineSource).
	Values map[string]any
}

// WorkloadComparison is one compared workload's two sides, for a
// before/after view.
type WorkloadComparison struct {
	// Workload is the rendered workload's name, or "" for a live side
	// compared against every rendered workload at once.
	Workload       string
	Rendered, Live map[string]string
}

// ChartDiffResult is the outcome of ChartDiff.
type ChartDiffResult struct {
	// Patch is the HelmRelease values patch, or nil if nothing to patch.
	Patch *PatchDoc
	// UnmappedDiffs lists variables that differ but couldn't be patched
	// automatically, sorted by workload and name.
	UnmappedDiffs []UnmappedDiff
	// LiveOnly are the variables that exist live but not in the chart's
	// rendered output, by name within each compared workload — these values will be
	// lost after migration, since nothing in the chart can carry them
	// forward.
	LiveOnly []LiveOnlyVar
	// RenderedOnlyCount is how many variables the chart renders that have
	// no live counterpart (e.g. new defaults introduced since the app was
	// last deployed).
	RenderedOnlyCount int
	// Workloads are the compared sides, one per Live entry in order —
	// exposed so a caller can present a full before/after view (e.g.
	// internal/appdiff builds a unified diff from it), not just the
	// mapped subset that became a patch.
	Workloads []WorkloadComparison
}

// resolvedVar is a rendered variable's value and the .Values paths it
// could be patched through.
type resolvedVar struct {
	value string
	paths []string
	// ambiguous is set when merged workloads disagree on whether the
	// variable has a path at all, so even a single path isn't certain.
	ambiguous bool
	// inline is the values env a container's own env var is rendered
	// from, when found; isInline is set for any container env var.
	inline   *inlineSource
	isInline bool
}

// mappedValue is one workload's live value for a .Values path.
type mappedValue struct {
	workload, name, rendered, live string
}

// ChartDiff compares each live workload's env vars against its rendered
// counterpart's and returns the HelmRelease values patch flux-stratio
// would splice into the tenant YAML.
//
// A differing variable is patched through the .Values path of the chart
// file its rendered value came from — the file behind the rendered
// ConfigMap the workload reads it from (AttributeConfigMaps) — never
// through a sibling workload's same-named variable. Where that can't be
// told (see UnmappedReason), the difference is reported in UnmappedDiffs
// instead of guessed.
func ChartDiff(in ChartDiffInput) *ChartDiffResult {
	c := chartContext{
		rendered:   indexRendered(in.Rendered),
		files:      in.Files,
		attributed: AttributeConfigMaps(in.Rendered, in.Files, in.ValuesRoot),
		valuesRoot: in.ValuesRoot,
		values:     in.Values,
	}
	result := &ChartDiffResult{}
	mapped := map[string][]mappedValue{}
	// matching are the variables already equal live through a single
	// path: patching that path for another variable would change them.
	matching := map[string][]mappedValue{}
	var ambiguous []UnmappedDiff
	var inlineEdits []inlineEdit
	workloads := map[string]*unstructured.Unstructured{}
	for _, lw := range in.Live {
		if lw.Rendered != nil {
			workloads[lw.Rendered.GetName()] = lw.Rendered
		}
	}
	// A flat live side names no workload, but a chart rendering only one
	// leaves no doubt which one a variable is pinned in.
	if len(c.rendered.workloads) == 1 {
		workloads[""] = c.rendered.workloads[0]
	}

	for _, lw := range in.Live {
		workload := ""
		var vars map[string]resolvedVar
		if lw.Rendered != nil {
			workload = lw.Rendered.GetName()
			vars = c.workloadVars(lw.Rendered)
		} else {
			vars = c.mergedVars()
		}

		comparison := WorkloadComparison{Workload: workload, Rendered: map[string]string{}, Live: lw.Env}
		for name, v := range vars {
			comparison.Rendered[name] = v.value
		}
		result.Workloads = append(result.Workloads, comparison)

		for _, name := range unionKeys(comparison.Rendered, lw.Env) {
			rv, hasRendered := vars[name]
			liveVal, hasLive := lw.Env[name]
			switch {
			case !hasLive:
				result.RenderedOnlyCount++
			case !hasRendered:
				result.LiveOnly = append(result.LiveOnly, LiveOnlyVar{Workload: workload, Name: name, Live: liveVal})
			case isPlaceholder(rv.value) || isPlaceholder(liveVal):
				// An unresolvable placeholder on either side can't be diffed.
			case rv.value == liveVal:
				if len(rv.paths) == 1 && !rv.ambiguous {
					matching[rv.paths[0]] = append(matching[rv.paths[0]], mappedValue{workload, name, rv.value, liveVal})
				}
			case rv.inline != nil && !rv.ambiguous:
				inlineEdits = append(inlineEdits, inlineEdit{mappedValue{workload, name, rv.value, liveVal}, *rv.inline})
			case rv.isInline && !rv.ambiguous:
				result.UnmappedDiffs = append(result.UnmappedDiffs, UnmappedDiff{
					Workload: workload, Name: name, Rendered: rv.value, Live: liveVal, Reason: UnmappedInline,
				})
			case len(rv.paths) == 1 && !rv.ambiguous:
				mapped[rv.paths[0]] = append(mapped[rv.paths[0]], mappedValue{workload, name, rv.value, liveVal})
			case len(rv.paths) == 0:
				result.UnmappedDiffs = append(result.UnmappedDiffs, UnmappedDiff{
					Workload: workload, Name: name, Rendered: rv.value, Live: liveVal, Reason: UnmappedNoPath,
				})
			default:
				ambiguous = append(ambiguous, UnmappedDiff{
					Workload: workload, Name: name, Rendered: rv.value, Live: liveVal,
					Reason: UnmappedAmbiguous, Candidates: rv.paths,
				})
			}
		}
	}

	// An ambiguity only matters while some candidate could still be
	// written: one whose every candidate is excluded is moot.
	for _, u := range ambiguous {
		var remaining []string
		for _, p := range u.Candidates {
			if !isExcluded(p, in.Exclude) {
				remaining = append(remaining, p)
			}
		}
		if len(remaining) > 0 {
			u.Candidates = remaining
			result.UnmappedDiffs = append(result.UnmappedDiffs, u)
		}
	}

	values := map[string]any{}
	for _, path := range sortedMapKeys(mapped) {
		if isExcluded(path, in.Exclude) {
			continue
		}
		entries := mapped[path]
		var shared []string
		var pins []inlineEdit
		pinnable := true
		for _, m := range matching[path] {
			if m.live == entries[0].live {
				continue
			}
			shared = append(shared, describeShared(m, len(in.Live) > 1))
			src := envSourceFor(in.Values, workloads[m.workload])
			if src == nil || isExcluded(src.path, in.Exclude) {
				pinnable = false
				continue
			}
			pins = append(pins, inlineEdit{m, *src})
		}
		if distinctLive(entries) == 1 && (len(shared) == 0 || pinnable) {
			setDotPath(values, path, CoerceValue(entries[0].live))
			inlineEdits = append(inlineEdits, pins...)
			continue
		}
		for _, e := range entries {
			result.UnmappedDiffs = append(result.UnmappedDiffs, UnmappedDiff{
				Workload: e.workload, Name: e.name, Rendered: e.rendered, Live: e.live,
				Reason: UnmappedConflict, Candidates: []string{path}, Shared: shared,
			})
		}
	}
	var keptEdits []inlineEdit
	for _, e := range inlineEdits {
		if !isExcluded(e.source.path, in.Exclude) && !isExcluded(e.source.path+"."+e.name, in.Exclude) {
			keptEdits = append(keptEdits, e)
		}
	}
	result.UnmappedDiffs = append(result.UnmappedDiffs, applyInlineEdits(values, in.Values, keptEdits)...)
	sort.SliceStable(result.UnmappedDiffs, func(i, j int) bool {
		a, b := result.UnmappedDiffs[i], result.UnmappedDiffs[j]
		if a.Workload != b.Workload {
			return a.Workload < b.Workload
		}
		return a.Name < b.Name
	})

	patchObj := map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]any{"name": in.HRName},
		"spec":       map[string]any{"values": values},
	}
	for _, p := range in.Exclude {
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

type chartContext struct {
	rendered   renderedEnv
	files      []ChartFile
	attributed map[string]*ChartFile
	valuesRoot string
	values     map[string]any
}

// pathsFor is the .Values paths v could be patched through: its own
// chart file's mapping when v came from a ConfigMap attributed to one
// (none, if that file doesn't map it — another file's same-named mapping
// belongs to a different ConfigMap), otherwise every chart file's.
func (c chartContext) pathsFor(v renderedVar) []string {
	if v.inline() {
		return nil
	}
	if file := c.attributed[v.ConfigMap]; file != nil {
		if p, ok := file.Values[v.Key]; ok {
			return []string{p}
		}
		return nil
	}
	return candidatePaths(c.files, v.Key, c.valuesRoot)
}

func (c chartContext) workloadVars(workload *unstructured.Unstructured) map[string]resolvedVar {
	out := map[string]resolvedVar{}
	for name, v := range c.rendered.workloadEnv(workload) {
		rv := resolvedVar{value: v.Value, paths: c.pathsFor(v), isInline: v.inline()}
		if rv.isInline {
			rv.inline = inlineSourceFor(c.values, workload, v)
		}
		out[name] = rv
	}
	return out
}

// mergedVars is every rendered workload's variables at once: a later
// workload's value overwrites an earlier one's (a concrete value is never
// replaced by a placeholder), and a variable's paths are every path any
// workload's value could be patched through — ambiguous when workloads
// disagree on whether it has one at all.
func (c chartContext) mergedVars() map[string]resolvedVar {
	out := map[string]resolvedVar{}
	for _, w := range c.rendered.workloads {
		for name, v := range c.workloadVars(w) {
			prev, ok := out[name]
			if !ok {
				out[name] = v
				continue
			}
			value := v.value
			if !isPlaceholder(prev.value) && isPlaceholder(value) {
				value = prev.value
			}
			ambiguous := prev.ambiguous || (len(prev.paths) == 0) != (len(v.paths) == 0) ||
				prev.isInline != v.isInline || !sameInline(prev.inline, v.inline)
			out[name] = resolvedVar{
				value: value, paths: unionSorted(prev.paths, v.paths), ambiguous: ambiguous,
				inline: v.inline, isInline: v.isInline,
			}
		}
	}
	return out
}

// isExcluded reports whether a .Values path is, or is under, one of
// exclude's patch-document paths.
func isExcluded(valuesPath string, exclude []string) bool {
	full := "spec.values." + valuesPath
	for _, p := range exclude {
		if full == p || strings.HasPrefix(full, p+".") {
			return true
		}
	}
	return false
}

func sameInline(a, b *inlineSource) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// describeShared names a variable Shared lists: its name and live value,
// prefixed with its workload when several are compared.
func describeShared(m mappedValue, prefix bool) string {
	name := m.name
	if prefix && m.workload != "" {
		name = m.workload + "/" + name
	}
	return name + "=" + m.live
}

func distinctLive(entries []mappedValue) int {
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.live] = true
	}
	return len(seen)
}

func unionKeys(a, b map[string]string) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	return sortedMapKeys(seen)
}

func unionSorted(a, b []string) []string {
	seen := map[string]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		seen[s] = true
	}
	return sortedMapKeys(seen)
}

func sortedMapKeys[V any](m map[string]V) []string {
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
