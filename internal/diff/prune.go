package diff

// pruneEmptyMaps recursively removes any map value that becomes an empty
// map after its own subtree is pruned. It never removes false, 0, "", or
// an empty slice — only an empty map, matching a real-world requirement
// from the gosec-agent patch shape, where an excluded leaf can leave its
// parent map empty and that parent (not the falsy values elsewhere in the
// same document) must disappear so it doesn't override a chart default
// with nothing.
func pruneEmptyMaps(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for k, child := range m {
		pruned := pruneEmptyMaps(child)
		if childMap, ok := pruned.(map[string]any); ok && len(childMap) == 0 {
			delete(m, k)
			continue
		}
		m[k] = pruned
	}
	return m
}
