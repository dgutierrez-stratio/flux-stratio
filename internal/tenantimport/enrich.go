package tenantimport

import (
	"strconv"

	"github.com/Stratio/flux-stratio/internal/catalog"
)

// enrich applies each entry's schema: resolves its declared dependencies
// against other discovered entries, fills extra config fields from
// schema defaults, and preserves anything already known (e.g. a dep name
// a CRD's own spec already supplied, or a storage type inferred from a
// live HelmRelease). A dependency with no matching discovered component —
// including a "governance*" dep, which this catalog never registers as a
// component type — is still emitted with an empty name and a warning, so
// the generated YAML's structure is always complete; the operator fills
// it in by hand. A config field with no default is never dropped silently:
// it is warned about — except one the template defaults to an empty dict or
// list (optional unless the catalog lists it as required) and one only read
// for another storage type. A required dict whose keys the template reads
// is written with those keys, empty, and warned about too.
func enrich(cat *catalog.Catalog, components Components, required map[string]map[string]bool, warn func(string, ...any)) {
	for key, entries := range components {
		schema, ok := cat.Schemas[key]
		if !ok {
			continue
		}
		for _, entry := range entries {
			enrichEntry(schema, entry, components, required[key], warn)
		}
	}
}

func enrichEntry(schema catalog.Schema, entry *Entry, components Components, required map[string]bool, warn func(string, ...any)) {
	// HDFS storage-type inference: generalized over any dependency
	// declaring "hdfs" in its StorageTypes, rather than the Python
	// client's hardcoded 4-component name list (design decision 8).
	if entry.Type == "" && schemaHasStorageType(schema, "hdfs") && len(components["hdfs"]) > 0 {
		entry.Type = "hdfs"
	}

	if entry.Deps == nil {
		entry.Deps = map[string]string{}
	}
	for _, dep := range schema.Dependencies {
		if _, already := entry.Deps[dep.Key]; already {
			continue
		}
		if len(dep.StorageTypes) > 0 && !containsStr(dep.StorageTypes, entry.Type) {
			continue // this dep only applies to a different storage type
		}
		switch names := components.names(dep.Key); len(names) {
		case 0:
			entry.Deps[dep.Key] = ""
			warn("%s: dependency %q could not be resolved; fill in its name by hand", entry.Name, dep.Key)
		case 1:
			entry.Deps[dep.Key] = names[0]
		default:
			entry.Deps[dep.Key] = names[0]
			warn("%s: dependency %q has %d discovered instances; defaulted to %q, review before use", entry.Name, dep.Key, len(names), names[0])
		}
	}

	for _, dep := range undecidedUses(entry) {
		warn(undecidedUseFormat, entry.Name, dep)
	}

	if entry.ExtraConfig == nil {
		entry.ExtraConfig = map[string]any{}
	}
	for _, field := range schema.ExtraConfig {
		if _, already := entry.ExtraConfig[field.Key]; already {
			continue
		}
		switch fieldPlan(field, entry.Type, required) {
		case fieldWarn:
			warn("%s: config %q has no default; set it by hand", entry.Name, field.Key)
		case fieldDefault:
			entry.ExtraConfig[field.Key] = defaultValue(field)
		case fieldScaffold:
			entry.ExtraConfig[field.Key] = scaffoldValue(field)
			warn("%s", scaffoldWarning(entry.Name, field))
		}
	}
}

// defaultValue is the YAML value a field's template default stands for: a
// quoted default (`default "false"`) is a string, as the template wrote it;
// a bare literal (`<< else >>false<< end >>`) is a bool or a number.
func defaultValue(f catalog.ExtraConfigField) any {
	if !f.Bare {
		return f.Default
	}
	if b, err := strconv.ParseBool(f.Default); err == nil && (f.Default == "true" || f.Default == "false") {
		return b
	}
	if n, err := strconv.ParseInt(f.Default, 10, 64); err == nil {
		return n
	}
	if n, err := strconv.ParseFloat(f.Default, 64); err == nil {
		return n
	}
	return f.Default
}

func schemaHasStorageType(schema catalog.Schema, storageType string) bool {
	for _, dep := range schema.Dependencies {
		if containsStr(dep.StorageTypes, storageType) {
			return true
		}
	}
	return false
}
