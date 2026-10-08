package tenantimport

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

// CompleteOptions configures a Complete run.
type CompleteOptions struct {
	TenantName string
	// Size is written as spec.defaultValues.size when the file has none;
	// defaults to S.
	Size string
	// Config is the component catalog, read for requiredConfig. Nil uses
	// the seed's.
	Config *config.Catalog
}

// Report is what a Complete run did, grouped the way it is printed.
type Report struct {
	// Added is every key, dependency or entry written.
	Added []string
	// LeftAlone is what the scan has an opinion about but the file keeps:
	// commented-out blocks, undeclared live components.
	LeftAlone []string
	// Warnings is what the operator should look at: a value the file
	// disagrees with the cluster about, a field with no usable default.
	Warnings []string
}

// Empty reports whether the run changed nothing.
func (r Report) Empty() bool { return len(r.Added) == 0 }

// Complete adds to doc what a tenant file for the scanned cluster needs and
// lacks, and nothing else: never removes, reorders, uncomments or
// overwrites. Existing values always win; where the scan disagrees with
// one, the disagreement is a warning. The result depends only on the file,
// the cluster (scanned, live) and the catalog, so a second run over its own
// output adds nothing.
//
// scanned must come from Scan without skeleton expansion: a skeleton entry
// would claim a component that may exist in the file under another name or
// be commented out.
func Complete(doc *tenantfile.Doc, scanned Components, live Live, cat *catalog.Catalog, opts CompleteOptions) (Report, error) {
	var r Report
	entries, err := doc.ActiveEntries()
	if err != nil {
		return r, err
	}

	if err := completeTopLevel(doc, opts, &r); err != nil {
		return r, err
	}

	required := requiredConfig(opts.Config)
	byKey := map[string][]string{} // component key -> active entry names, file order
	for _, e := range entries {
		byKey[e.Key] = append(byKey[e.Key], e.Name)
	}

	for _, e := range entries {
		schema, ok := cat.Schemas[e.Key]
		if !ok {
			continue
		}
		if err := completeEntry(doc, e, schema, scanned, live, byKey, required[e.Key], &r); err != nil {
			return r, err
		}
	}

	if err := completeUndeclared(doc, scanned, byKey, cat, required, &r); err != nil {
		return r, err
	}

	var commented []string
	for key := range cat.Schemas {
		if len(byKey[key]) == 0 && tenantfile.CommentedOut(doc, key) {
			commented = append(commented, key)
		}
	}
	if len(commented) > 0 {
		sort.Strings(commented)
		r.LeftAlone = append(r.LeftAlone, fmt.Sprintf(
			"left commented out: %s (run tenant import again after uncommenting; it only completes active entries)",
			strings.Join(commented, ", ")))
	}
	return r, nil
}

func completeTopLevel(doc *tenantfile.Doc, opts CompleteOptions, r *Report) error {
	size := opts.Size
	if size == "" {
		size = "S"
	}
	fields := []struct {
		key   string
		value any
	}{
		{"tenantName", opts.TenantName},
		{"size", size},
		{"sopsSecretProvided", false},
		{"internalS3BucketName", ""},
		{"userEmail", ""},
		{"userId", ""},
		{"userName", ""},
	}
	var missing []tenantfile.Field
	for _, f := range fields {
		if doc.HasTopLevel(f.key) {
			continue
		}
		missing = append(missing, tenantfile.Field{Key: f.key, Value: f.value})
		r.Added = append(r.Added, fmt.Sprintf("spec.defaultValues.%s: %s", f.key, fmtValue(f.value)))
	}
	return doc.AddTopLevel(missing)
}

func completeEntry(doc *tenantfile.Doc, e tenantfile.ComponentEntry, schema catalog.Schema, scanned Components, live Live, byKey map[string][]string, required map[string]bool, r *Report) error {
	label := fmt.Sprintf("components.%s[%s]", e.Key, e.Name)
	var fields []tenantfile.Field
	var deps []tenantfile.DependencyRef
	addField := func(key string, value any) {
		fields = append(fields, tenantfile.Field{Key: key, Value: value})
		r.Added = append(r.Added, fmt.Sprintf("%s: config.%s: %s", label, key, fmtValue(value)))
	}

	// Storage type: an existing one wins; otherwise inferred as in Create
	// (any dependency declaring "hdfs" with an hdfs component live).
	storageType := ""
	if t := tenantfile.ConfigValue(e.Node, "type"); t != nil {
		storageType = t.Value
	} else if schemaHasStorageType(schema, "hdfs") && len(scanned["hdfs"]) > 0 {
		storageType = "hdfs"
		addField("type", "hdfs")
	}

	// What the entry's live HelmRelease says about the dependencies it uses.
	// Not found by name (the file may call it something else): the one
	// scanned entry of the key stands for the one the file declares.
	used := scanned.find(e.Key, e.Name)
	if used == nil && len(scanned[e.Key]) == 1 && len(byKey[e.Key]) == 1 {
		used = scanned[e.Key][0]
	}
	if used != nil {
		completeUses(doc, e, schema, storageType, used, label, r)
	}

	// Config fields: the cluster's own value first, then the template
	// default; a field with neither is reported, never dropped silently.
	derived := live.Values[e.Key]
	derivedKeys := make([]string, 0, len(derived))
	for k := range derived {
		derivedKeys = append(derivedKeys, k)
	}
	sort.Strings(derivedKeys)
	for _, key := range derivedKeys {
		if existing := tenantfile.ConfigValue(e.Node, key); existing != nil {
			if existing.Value == "false" && derived[key] == true {
				r.Warnings = append(r.Warnings, fmt.Sprintf(
					"%s: config.%s is false but %s run on the cluster; left as is — the next release would remove them",
					label, key, strings.Join(live.Siblings[e.Key], ", ")))
			}
			continue
		}
		addField(key, derived[key])
		if on, _ := derived[key].(bool); on {
			// Switching the siblings on makes the chart render workloads under
			// the legacy Deployments' names with another, immutable selector:
			// pushed as is, the release fails and cannot roll back.
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"%s: config.%s: true makes the chart render %s; a legacy CCT Deployment of the same name blocks the release (immutable selector) — run `apps migrate %s` first, its prepare step removes them, before this change reaches the cluster",
				label, key, strings.Join(live.Siblings[e.Key], ", "), e.Name))
		}
	}
	for _, field := range schema.ExtraConfig {
		if _, ok := derived[field.Key]; ok || tenantfile.ConfigValue(e.Node, field.Key) != nil {
			continue
		}
		switch fieldPlan(field, storageType, required) {
		case fieldWarn:
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: config %q has no default; set it by hand", label, field.Key))
		case fieldDefault:
			addField(field.Key, defaultValue(field))
		}
	}

	// Dependencies the schema declares and the entry lacks.
	for _, dep := range schema.Dependencies {
		if len(dep.StorageTypes) > 0 && !containsStr(dep.StorageTypes, storageType) {
			continue
		}
		if tenantfile.HasDependency(e.Node, dep.Key) {
			continue // declared, even with another or an empty name: the file wins
		}
		names := scanned.names(dep.Key)
		if len(names) == 0 && len(byKey[dep.Key]) == 1 {
			names = byKey[dep.Key] // not live yet, but the file declares exactly one
		}
		switch len(names) {
		case 0:
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: dependency %q could not be resolved; not added, fill it in by hand", label, dep.Key))
			continue
		case 1:
		default:
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: dependency %q has %d discovered instances; added %q, review before use", label, dep.Key, len(names), names[0]))
		}
		ref := tenantfile.DependencyRef{Key: dep.Key, Name: names[0]}
		added := fmt.Sprintf("%s: config.dependencies.%s.name: %s", label, dep.Key, names[0])
		if used != nil && used.DepEnabled[dep.Key] != "" {
			ref.Enabled = true
			added += fmt.Sprintf(", enabled: true (spec.values.%s is set in its HelmRelease)", used.DepEnabled[dep.Key])
		} else if used != nil && used.DepUnknown[dep.Key] {
			r.Warnings = append(r.Warnings, fmt.Sprintf(undecidedUseFormat, label, dep.Key))
		}
		deps = append(deps, ref)
		r.Added = append(r.Added, added)
	}

	if err := doc.AddToEntry(e.Node, fields, deps); err != nil {
		// Nothing of this entry was written; take its additions back out of
		// the report and say why.
		r.Added = r.Added[:len(r.Added)-len(fields)-len(deps)]
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s: %v", label, err))
	}
	return nil
}

// completeUses settles `enabled` for the dependencies e already declares,
// from the HelmRelease evidence in used: a declared dependency without it
// gets `enabled: true` when its values show the integration in use; one the
// operator set to false stays, with a warning; and one the cluster cannot
// vouch for (no HelmRelease yet) is warned about, never guessed. A new
// dependency is handled where it is added.
func completeUses(doc *tenantfile.Doc, e tenantfile.ComponentEntry, schema catalog.Schema, storageType string, used *Entry, label string, r *Report) {
	for _, dep := range schema.Dependencies {
		if len(dep.StorageTypes) > 0 && !containsStr(dep.StorageTypes, storageType) {
			continue
		}
		if !tenantfile.HasDependency(e.Node, dep.Key) {
			continue
		}
		current := tenantfile.DependencyField(e.Node, dep.Key, "enabled")
		switch evidence := used.DepEnabled[dep.Key]; {
		case evidence != "" && current != nil:
			if current.Value == "false" {
				r.Warnings = append(r.Warnings, fmt.Sprintf(
					"%s: config.dependencies.%s.enabled is false but spec.values.%s is set in its HelmRelease; left as is — the integration stays off",
					label, dep.Key, evidence))
			}
		case evidence != "":
			if tenantfile.DependencyNames(e.Node)[dep.Key] == "" {
				r.Warnings = append(r.Warnings, fmt.Sprintf("%s: dependency %q has no name; not enabled, fill it in by hand first", label, dep.Key))
				continue
			}
			if err := doc.AddToDependency(e.Node, dep.Key, []tenantfile.Field{{Key: "enabled", Value: true}}); err != nil {
				r.Warnings = append(r.Warnings, fmt.Sprintf("%s: %v", label, err))
				continue
			}
			r.Added = append(r.Added, fmt.Sprintf("%s: config.dependencies.%s.enabled: true (spec.values.%s is set in its HelmRelease)", label, dep.Key, evidence))
		case used.DepUnknown[dep.Key] && current == nil:
			r.Warnings = append(r.Warnings, fmt.Sprintf(undecidedUseFormat, label, dep.Key))
		}
	}
}

// completeUndeclared handles the components the scan found live that the
// file has no active entry for, appended as new entries — except under a
// commented-out block, which is the operator's own switch-on-one-at-a-time
// and never gets a duplicate.
func completeUndeclared(doc *tenantfile.Doc, scanned Components, byKey map[string][]string, cat *catalog.Catalog, required map[string]map[string]bool, r *Report) error {
	keys := make([]string, 0, len(scanned))
	for key := range scanned {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, s := range scanned[key] {
			if slices.Contains(byKey[key], s.Name) {
				continue
			}
			label := fmt.Sprintf("components.%s[%s]", key, s.Name)
			switch {
			case len(byKey[key]) == 0 && tenantfile.CommentedOut(doc, key):
				// Covered by the summary line about commented-out blocks.
			default:
				if others := byKey[key]; len(others) > 0 {
					r.Warnings = append(r.Warnings, fmt.Sprintf(
						"%s: added, but components.%s already declares %s; if that is this component under another name, remove the new entry",
						label, key, strings.Join(others, ", ")))
				}
				if err := doc.AppendEntry(key, s.Name, entryConfig(s)); err != nil {
					return err
				}
				byKey[key] = append(byKey[key], s.Name)
				r.Added = append(r.Added, label+": new entry")
				for _, field := range cat.Schemas[key].ExtraConfig {
					if _, set := s.ExtraConfig[field.Key]; !set && fieldPlan(field, s.Type, required[key]) == fieldWarn {
						r.Warnings = append(r.Warnings, fmt.Sprintf("%s: config %q has no default; set it by hand", label, field.Key))
					}
				}
				for dep, name := range s.Deps {
					if name == "" {
						r.Warnings = append(r.Warnings, fmt.Sprintf("%s: dependency %q could not be resolved; not added, fill it in by hand", label, dep))
					}
				}
				for _, dep := range undecidedUses(s) {
					r.Warnings = append(r.Warnings, fmt.Sprintf(undecidedUseFormat, label, dep))
				}
			}
		}
	}
	return nil
}

// entryConfig is the config block of a scanned entry, as renderEntries
// writes it, minus the dependencies it could not resolve (an empty name
// would only be broken YAML for the render).
func entryConfig(e *Entry) map[string]any {
	config := map[string]any{}
	if e.Type != "" {
		config["type"] = e.Type
	}
	for k, v := range e.ExtraConfig {
		config[k] = v
	}
	deps := map[string]any{}
	for k, v := range e.Deps {
		if v != "" {
			deps[k] = e.depConfig(k, v)
		}
	}
	if len(deps) > 0 {
		config["dependencies"] = deps
	}
	return config
}

func fmtValue(v any) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprint(v)
}
