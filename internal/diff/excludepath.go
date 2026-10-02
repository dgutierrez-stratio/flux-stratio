package diff

import "strings"

// removeDotPath deletes the field at dotPath (e.g.
// "spec.bootstrap.pgBackup") from obj, navigating nested maps. It is a
// silent no-op if any segment of the path doesn't exist — an exclude path
// that doesn't apply to a given app's patch is not an error, since the
// config's exclude list is written once and reused across every diff of
// that app. Only dotted map navigation is supported, matching the config
// file's exclude paths (no list-index or dotted-key addressing).
func removeDotPath(obj map[string]any, dotPath string) {
	parts := strings.Split(dotPath, ".")
	cur := obj
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, parts[len(parts)-1])
}

// excludeJSONPatchOps drops every JSONPatchOp whose Path is, or is a
// descendant of, one of excludePaths, and strips an excluded field out of
// the value of an op on one of its ancestors: objectOps adds a whole
// missing subtree in one op (add /spec/bootstrap, carrying pgBackup), and
// matching on the op's own path alone let the excluded field through in
// it. An ancestor op left with nothing in its value is dropped too.
// Paths are compared segment by segment, JSON-Pointer-unescaped, so a key
// holding "~" or "/" matches its dotted exclude path.
func excludeJSONPatchOps(ops []JSONPatchOp, excludePaths []string) []JSONPatchOp {
	if len(excludePaths) == 0 {
		return ops
	}
	excludes := make([][]string, len(excludePaths))
	for i, p := range excludePaths {
		excludes[i] = strings.Split(p, ".")
	}
	out := make([]JSONPatchOp, 0, len(ops))
	for _, op := range ops {
		opSegs := pointerSegments(op.Path)
		keep := true
		for _, ex := range excludes {
			switch {
			case hasSegmentPrefix(opSegs, ex):
				keep = false
			case hasSegmentPrefix(ex, opSegs):
				value, ok := op.Value.(map[string]any)
				if !ok {
					continue
				}
				if len(value) == 0 {
					continue
				}
				value = copyMaps(value)
				removeAndPrune(value, ex[len(opSegs):])
				if len(value) == 0 {
					keep = false
				}
				op.Value = value
			}
			if !keep {
				break
			}
		}
		if keep {
			out = append(out, op)
		}
	}
	return out
}

// removeAndPrune deletes the field at path from m, then every map on the
// way down to it that the delete left empty — and nothing else, so an
// empty map elsewhere in m (a live value in its own right) stays.
func removeAndPrune(m map[string]any, path []string) {
	if len(path) == 0 {
		return
	}
	if len(path) == 1 {
		delete(m, path[0])
		return
	}
	child, ok := m[path[0]].(map[string]any)
	if !ok {
		return
	}
	removeAndPrune(child, path[1:])
	if len(child) == 0 {
		delete(m, path[0])
	}
}

// pointerSegments splits a JSON Pointer into its unescaped segments.
func pointerSegments(pointer string) []string {
	if pointer == "" || pointer == "/" {
		return nil
	}
	segs := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for i, s := range segs {
		segs[i] = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
	}
	return segs
}

// hasSegmentPrefix reports whether path starts with prefix.
func hasSegmentPrefix(path, prefix []string) bool {
	if len(prefix) > len(path) {
		return false
	}
	for i := range prefix {
		if path[i] != prefix[i] {
			return false
		}
	}
	return true
}

// copyMaps copies m and every map nested in it, so removing a field from
// the copy never changes m; anything else is shared.
func copyMaps(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if child, ok := v.(map[string]any); ok {
			v = copyMaps(child)
		}
		out[k] = v
	}
	return out
}
