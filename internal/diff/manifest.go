package diff

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DeepDiff computes local vs live's diff with live winning: a key only in
// live is added; a key only in local is dropped; nested maps recurse;
// scalars and lists are compared for equality and, if different, live's
// value is kept. The direction is deliberate — this plugin back-ports the
// live cluster's current values into a patch, not the other way around.
func DeepDiff(local, live map[string]any) map[string]any {
	out := map[string]any{}
	for k, liveVal := range live {
		localVal, exists := local[k]
		if !exists {
			out[k] = liveVal
			continue
		}
		liveMap, liveIsMap := liveVal.(map[string]any)
		localMap, localIsMap := localVal.(map[string]any)
		if liveIsMap && localIsMap {
			if nested := DeepDiff(localMap, liveMap); len(nested) > 0 {
				out[k] = nested
			}
			continue
		}
		if !reflect.DeepEqual(localVal, liveVal) {
			out[k] = liveVal
		}
	}
	return out
}

// NeedsJSON6902 reports whether local and live's patch should use RFC 6902
// JSON Patch instead of a strategic merge patch: true only when some
// top-level key is a list on both sides. Deliberately not recursive — a
// list nested inside, say, spec.values is still handled by strategic-merge
// value replacement, which is simpler and sufficient there; only a
// top-level list (as operator CRDs like PgCluster commonly have) forces
// index-aware JSON6902 ops.
func NeedsJSON6902(local, live map[string]any) bool {
	for k, localVal := range local {
		if _, ok := localVal.([]any); !ok {
			continue
		}
		if liveVal, ok := live[k]; ok {
			if _, ok := liveVal.([]any); ok {
				return true
			}
		}
	}
	return false
}

// ToJSON6902Ops derives the ops that turn local into live under the given
// JSON-Pointer root (e.g. "/spec"): a dict key only in live is "add"; a
// key in both recurses; a key only in local is skipped (this plugin never
// emits "remove" — a field the chart declares that live lacks just stays
// at its chart default, needing no override). A list is walked index-wise:
// an index past local's length is "add"; a dict element at a shared index
// recurses; anything else that differs is "replace".
func ToJSON6902Ops(local, live any, path string) []JSONPatchOp {
	switch liveVal := live.(type) {
	case map[string]any:
		return objectOps(local, liveVal, path)
	case []any:
		return listOps(local, liveVal, path)
	default:
		if !reflect.DeepEqual(local, live) {
			return []JSONPatchOp{{Op: "replace", Path: path, Value: live}}
		}
		return nil
	}
}

func objectOps(local any, live map[string]any, path string) []JSONPatchOp {
	localMap, _ := local.(map[string]any)
	keys := make([]string, 0, len(live))
	for k := range live {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic output, independent of Go's random map iteration order

	var ops []JSONPatchOp
	for _, k := range keys {
		liveChild := live[k]
		childPath := path + "/" + escapeJSONPointer(k)
		localChild, exists := localMap[k]
		if !exists {
			ops = append(ops, JSONPatchOp{Op: "add", Path: childPath, Value: liveChild})
			continue
		}
		ops = append(ops, ToJSON6902Ops(localChild, liveChild, childPath)...)
	}
	return ops
}

func listOps(local any, live []any, path string) []JSONPatchOp {
	localList, _ := local.([]any)
	var ops []JSONPatchOp
	for i, liveItem := range live {
		childPath := fmt.Sprintf("%s/%d", path, i)
		if i >= len(localList) {
			ops = append(ops, JSONPatchOp{Op: "add", Path: childPath, Value: liveItem})
			continue
		}
		localItem := localList[i]
		if liveDict, ok := liveItem.(map[string]any); ok {
			ops = append(ops, objectOps(localItem, liveDict, childPath)...)
			continue
		}
		if !reflect.DeepEqual(localItem, liveItem) {
			ops = append(ops, JSONPatchOp{Op: "replace", Path: childPath, Value: liveItem})
		}
	}
	return ops
}

// escapeJSONPointer escapes a JSON-Pointer reference token per RFC 6901:
// "~" must be escaped before "/", since escaping "/" first would corrupt
// an already-escaped "~1".
func escapeJSONPointer(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}

// ManifestDiff compares local (the rendered desired-state object) against
// live (the current cluster object) and returns the patch flux-stratio
// would splice into the tenant YAML, or nil if there is no difference
// after excludePaths are applied. excludePaths are dot-paths, rooted at
// the patch document (e.g. "spec.bootstrap.pgBackup") — fields the GitOps
// side is authoritative for and must never be back-ported from the live
// cluster.
func ManifestDiff(local, live *unstructured.Unstructured, excludePaths []string) (*PatchDoc, error) {
	localSpec, _, err := unstructured.NestedMap(local.Object, "spec")
	if err != nil {
		return nil, fmt.Errorf("reading local spec: %w", err)
	}
	liveSpec, _, err := unstructured.NestedMap(live.Object, "spec")
	if err != nil {
		return nil, fmt.Errorf("reading live spec: %w", err)
	}

	if NeedsJSON6902(localSpec, liveSpec) {
		ops := excludeJSONPatchOps(ToJSON6902Ops(localSpec, liveSpec, "/spec"), excludePaths)
		if len(ops) == 0 {
			return nil, nil
		}
		return &PatchDoc{TargetKind: local.GetKind(), Patch: ops}, nil
	}

	patchObj := map[string]any{
		"apiVersion": local.GetAPIVersion(),
		"kind":       local.GetKind(),
		"metadata":   map[string]any{"name": local.GetName()},
		"spec":       any(DeepDiff(localSpec, liveSpec)),
	}
	for _, p := range excludePaths {
		removeDotPath(patchObj, p)
	}
	pruneEmptyMaps(patchObj)

	spec, _ := patchObj["spec"].(map[string]any)
	if len(spec) == 0 {
		return nil, nil
	}
	return &PatchDoc{TargetKind: local.GetKind(), Patch: patchObj}, nil
}
