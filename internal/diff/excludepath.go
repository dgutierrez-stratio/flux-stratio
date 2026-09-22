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
// descendant of, one of excludePaths (dot-paths converted to their
// JSON-Pointer equivalent).
func excludeJSONPatchOps(ops []JSONPatchOp, excludePaths []string) []JSONPatchOp {
	if len(excludePaths) == 0 {
		return ops
	}
	prefixes := make([]string, len(excludePaths))
	for i, p := range excludePaths {
		prefixes[i] = "/" + strings.ReplaceAll(p, ".", "/")
	}
	out := make([]JSONPatchOp, 0, len(ops))
	for _, op := range ops {
		excluded := false
		for _, prefix := range prefixes {
			if op.Path == prefix || strings.HasPrefix(op.Path, prefix+"/") {
				excluded = true
				break
			}
		}
		if !excluded {
			out = append(out, op)
		}
	}
	return out
}
