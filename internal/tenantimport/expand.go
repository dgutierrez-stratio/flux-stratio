package tenantimport

import "github.com/Stratio/flux-stratio/internal/catalog"

// expandMandatoryComponents iterates to a fixpoint: for every dependency
// of every already-present component that (a) has its own schema in the
// catalog and (b) isn't already present, a skeleton entry is added — so a
// component the live cluster scan missed (or that genuinely doesn't exist
// yet) still appears in the generated YAML with a name to fill in,
// keeping every mandatory field's structure complete. A dependency with
// no schema of its own (e.g. "governancePostgres", which this catalog
// never registers as a component type) is never expanded into a bogus
// top-level entry — that check alone is enough; no separate exclusion
// list is needed. Storage-type-conditional deps are skipped unless that
// storage type is actually in use.
func expandMandatoryComponents(cat *catalog.Catalog, components Components, warn func(string, ...any)) {
	for {
		added := false
		for key, entries := range components {
			schema, ok := cat.Schemas[key]
			if !ok {
				continue
			}
			for _, entry := range entries {
				for _, dep := range schema.Dependencies {
					if len(dep.StorageTypes) > 0 && !containsStr(dep.StorageTypes, entry.Type) {
						continue
					}
					if _, ok := cat.Schemas[dep.Key]; !ok {
						continue
					}
					if len(components[dep.Key]) > 0 {
						continue
					}
					name := camelToKebab(dep.Key)
					components[dep.Key] = append(components[dep.Key], &Entry{Name: name, Deps: map[string]string{}})
					warn("%q not found in cluster; adding skeleton entry (required by %q)", dep.Key, key)
					added = true
				}
			}
		}
		if !added {
			break
		}
	}
}
