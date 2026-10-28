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
	// Decider answers the proposals to enable a gated dependency that runs on
	// the cluster and the file has no answer for. The zero value asks nobody:
	// each stays pending and is reported.
	Decider Decider
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

// warnf appends a warning.
func (r *Report) warnf(format string, a ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, a...))
}

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
	r.Warnings = append(r.Warnings, live.Warnings...)

	required := requiredConfig(opts.Config)
	byKey := map[string][]string{} // component key -> active entry names, file order
	for _, e := range entries {
		byKey[e.Key] = append(byKey[e.Key], e.Name)
	}
	scaffoldGitOpsOnly(doc, entries, scanned, byKey, cat, required, opts.Config, &r)

	for _, e := range entries {
		schema, ok := cat.Schemas[e.Key]
		if !ok {
			continue
		}
		if err := completeEntry(doc, e, schema, scanned, live, byKey, required[e.Key], opts.Decider, &r); err != nil {
			return r, err
		}
	}

	declared := make(map[string][]string, len(byKey))
	for key, names := range byKey {
		declared[key] = slices.Clone(names)
	}
	if err := completeUndeclared(doc, scanned, byKey, declared, cat, required, opts.Decider, &r); err != nil {
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

// scaffoldGitOpsOnly adds to scanned an entry for each component something
// depends on that exists only in GitOps: nothing runs it on a legacy cluster
// (no catalog type produces its component), yet a ResourceSet waits on it
// unconditionally (postgres on its backup repository), so without an entry the
// dependent would wait forever. A component the catalog can discover is never
// scaffolded — the cluster says it is not there — nor one the file declares
// or has commented out. The new entry is written by completeUndeclared like any
// other, and warned about: it creates a resource that did not exist.
//
// It changes scanned in place so that every dependent resolves to it.
func scaffoldGitOpsOnly(doc *tenantfile.Doc, entries []tenantfile.ComponentEntry, scanned Components, byKey map[string][]string, cat *catalog.Catalog, required map[string]map[string]bool, cfg *config.Catalog, r *Report) {
	if cfg == nil {
		seed := config.SeedCatalog()
		cfg = &seed
	}
	type need struct{ by, key string }
	var needs []need
	collect := func(by, schemaKey, storageType string, declared func(string) bool) {
		for _, dep := range cat.Schemas[schemaKey].Dependencies {
			if (len(dep.StorageTypes) > 0 && !containsStr(dep.StorageTypes, storageType)) || declared(dep.Key) {
				continue
			}
			needs = append(needs, need{by, dep.Key})
		}
	}
	for _, e := range entries {
		storageType := ""
		if t := tenantfile.ConfigValue(e.Node, "type"); t != nil {
			storageType = t.Value
		}
		collect(fmt.Sprintf("components.%s[%s]", e.Key, e.Name), e.Key, storageType,
			func(dep string) bool { return tenantfile.HasDependency(e.Node, dep) })
	}
	keys := make([]string, 0, len(scanned))
	for key := range scanned {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, s := range scanned[key] {
			collect(fmt.Sprintf("components.%s[%s]", key, s.Name), key, s.Type,
				func(dep string) bool { return s.Deps[dep] != "" })
		}
	}

	for _, n := range needs {
		if _, known := cat.Schemas[n.key]; !known || len(resolveDep(n.key, "", scanned, byKey)) > 0 || catalogProduces(cfg, n.key) || tenantfile.CommentedOut(doc, n.key) {
			continue
		}
		entry := &Entry{Name: camelToKebab(n.key), Deps: map[string]string{}}
		enrichEntry(cat.Schemas[n.key], entry, scanned, required[n.key], func(string, ...any) {})
		scanned[n.key] = append(scanned[n.key], entry)
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"components.%s[%s]: GitOps-only: no legacy app of it is recognized on the cluster (the catalog has no type for it: see `flux stratio doctor`), but %s depends on it; it is added so the release does not wait forever, and applying it creates it new — review it",
			n.key, entry.Name, n.by))
	}
}

// catalogProduces reports whether any catalog type's instances are entries
// of component key: whether a legacy cluster could have it.
func catalogProduces(cfg *config.Catalog, key string) bool {
	for _, t := range cfg.Types {
		if t.Component == key {
			return true
		}
	}
	return false
}

// resolveDep lists the names the dependency key can point at: what the scan
// found live and what the file declares. Together they are every entry the
// finished file has under that key (a scanned name the file lacks is
// appended by completeUndeclared), so a dependency resolves the same whether
// its dependent was already in the file or is being added. Names in both come
// first, then the file's own in file order (what the operator declared is
// their choice), then the rest of the scan's.
func resolveDep(key, storageType string, scanned Components, byKey map[string][]string) []string {
	live := scanned.names(key)
	inScan := make(map[string]bool, len(live))
	for _, n := range live {
		inScan[n] = true
	}
	seen := map[string]bool{}
	var both, fileOnly, scanOnly []string
	for _, n := range byKey[key] {
		if seen[n] {
			continue
		}
		seen[n] = true
		if inScan[n] {
			both = append(both, n)
		} else {
			fileOnly = append(fileOnly, n)
		}
	}
	for _, n := range live {
		if !seen[n] {
			seen[n] = true
			scanOnly = append(scanOnly, n)
		}
	}
	if len(both)+len(fileOnly) > 0 || len(scanOnly) < 2 {
		return slices.Concat(both, fileOnly, scanOnly)
	}
	// Only the scan has candidates: choose among them as the scan would.
	if name, ambiguous := pickDependency(scanned, key, storageType); len(ambiguous) == 0 {
		return []string{name}
	}
	return scanOnly
}

// ambiguousDepWarning is the warning for a dependency several entries could
// stand for; the first was used.
func ambiguousDepWarning(label, key string, names []string, used string) string {
	return fmt.Sprintf("%s: dependency %q has %d candidates (%s); used %q, review before use",
		label, key, len(names), strings.Join(names, ", "), used)
}

func completeEntry(doc *tenantfile.Doc, e tenantfile.ComponentEntry, schema catalog.Schema, scanned Components, live Live, byKey map[string][]string, required map[string]bool, dec Decider, r *Report) error {
	label := fmt.Sprintf("components.%s[%s]", e.Key, e.Name)
	var fields []tenantfile.Field
	var deps []tenantfile.DependencyRef
	// What this entry's own AddToEntry writes; reported only once it has, so a
	// failure leaves nothing of it in the report.
	var written []string
	addField := func(key string, value any) {
		fields = append(fields, tenantfile.Field{Key: key, Value: value})
		written = append(written, fmt.Sprintf("%s: config.%s: %s", label, key, fmtValue(value)))
	}

	// The scanned entry that stands for this one: what the cluster says about
	// it (its live HelmRelease's dependencies in use, its legacy workload's
	// storage type). Not found by name (the file may call it something else):
	// the one scanned entry of the key stands for the one the file declares.
	used := scanned.find(e.Key, e.Name)
	if used == nil && len(scanned[e.Key]) == 1 && len(byKey[e.Key]) == 1 {
		used = scanned[e.Key][0]
	}

	// Storage type: an existing one wins; otherwise what the scan found (the
	// legacy workload's own, or inferred as in Create: any dependency declaring
	// "hdfs" with an hdfs component live).
	storageType := ""
	if t := tenantfile.ConfigValue(e.Node, "type"); t != nil {
		storageType = t.Value
	} else if used != nil && used.Type != "" {
		storageType = used.Type
		addField("type", used.Type)
	} else if schemaHasStorageType(schema, "hdfs") && len(scanned["hdfs"]) > 0 {
		storageType = "hdfs"
		addField("type", "hdfs")
	}
	if used != nil {
		if err := completeUses(doc, e, schema, storageType, used, label, dec, r); err != nil {
			return err
		}
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
		if _, ok := derived[field.Key]; ok {
			continue
		}
		plan := fieldPlan(field, storageType, required)
		fromEnv, hasEnv := used.fromEnv(field.Key)
		if existing := tenantfile.ConfigValue(e.Node, field.Key); existing != nil {
			// The file's value wins; only a scaffolded block is looked into,
			// so its placeholders keep being reported until filled in.
			if hasEnv && existing.Value != fromEnv {
				r.Warnings = append(r.Warnings, fmt.Sprintf(
					"%s: config.%s is %s but its legacy workload is configured with %q; left as is — the release would change it",
					label, field.Key, existing.Value, fromEnv))
			}
			if plan == fieldScaffold {
				for _, leaf := range tenantfile.EmptyConfigLeaves(e.Node, field.Key, leafKeys(field)) {
					r.Warnings = append(r.Warnings, fmt.Sprintf("%s: config.%s.%s is empty; set it by hand", label, field.Key, leaf))
				}
			}
			continue
		}
		if hasEnv {
			addField(field.Key, fromEnv) // what the app runs with, not the template's default
			continue
		}
		switch plan {
		case fieldWarn:
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: config %q has no default; set it by hand", label, field.Key))
		case fieldDefault:
			addField(field.Key, defaultValue(field))
		case fieldScaffold:
			addField(field.Key, scaffoldValue(field))
			r.Warnings = append(r.Warnings, scaffoldWarning(label, field))
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
		names := resolveDep(dep.Key, storageType, scanned, byKey)
		if len(names) == 0 && used != nil && used.Deps[dep.Key] != "" {
			names = []string{used.Deps[dep.Key]} // read off its legacy workload
		}
		switch len(names) {
		case 0:
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: dependency %q could not be resolved; not added, fill it in by hand", label, dep.Key))
			continue
		case 1:
		default:
			r.Warnings = append(r.Warnings, ambiguousDepWarning(label, dep.Key, names, names[0]))
		}
		ref := tenantfile.DependencyRef{Key: dep.Key, Name: names[0]}
		added := fmt.Sprintf("%s: config.dependencies.%s.name: %s", label, dep.Key, names[0])
		if used != nil {
			state, err := decideDependency(dec, label, used, dep.Key, r)
			if err != nil {
				return err
			}
			switch state {
			case DepOn:
				ref.Enabled = tenantfile.Bool(true)
				added += ", enabled: true"
			case DepOff:
				ref.Enabled = tenantfile.Bool(false)
				added += ", enabled: false (left off)"
			}
		}
		deps = append(deps, ref)
		written = append(written, added)
	}

	if err := doc.AddToEntry(e.Node, fields, deps); err != nil {
		// Nothing of this entry was written; say why.
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s: %v", label, err))
		return nil
	}
	r.Added = append(r.Added, written...)
	return nil
}

// decideDependency settles `enabled` for dependency key of the scanned entry
// used, when the tenant file has no answer for it: it asks about a gated
// dependency that runs on the cluster, and warns about a gated one that does
// not. The zero state means there is nothing to write (not gated, absent, or
// still pending).
func decideDependency(dec Decider, label string, used *Entry, key string, r *Report) (DepState, error) {
	if used.DepAbsent[key] {
		r.warnf(absentFormat, label, key)
		return 0, nil
	}
	if _, proposed := used.DepGate[key]; !proposed {
		return 0, nil
	}
	return dec.decide(label, used, key, r.warnf)
}

// completeUses settles `enabled` for the gated dependencies e already declares
// without one: asks about each that runs on the cluster and writes the answer
// (`true`, or an explicit `false` when declined, so it is not asked again);
// warns about each that does not. A dependency whose `enabled` the file
// already holds is never asked about or changed, `false` included. A new
// dependency is handled where it is added.
func completeUses(doc *tenantfile.Doc, e tenantfile.ComponentEntry, schema catalog.Schema, storageType string, used *Entry, label string, dec Decider, r *Report) error {
	for _, dep := range schema.Dependencies {
		if !dep.Gated || (len(dep.StorageTypes) > 0 && !containsStr(dep.StorageTypes, storageType)) {
			continue
		}
		if !tenantfile.HasDependency(e.Node, dep.Key) || tenantfile.DependencyField(e.Node, dep.Key, "enabled") != nil {
			continue
		}
		if _, proposed := used.DepGate[dep.Key]; proposed && tenantfile.DependencyNames(e.Node)[dep.Key] == "" {
			r.warnf("%s: dependency %q has no name; not asked about `enabled`, fill the name in by hand first", label, dep.Key)
			continue
		}
		state, err := decideDependency(dec, label, used, dep.Key, r)
		if err != nil {
			return err
		}
		var value bool
		switch state {
		case DepOn:
			value = true
		case DepOff:
		default:
			continue
		}
		if err := doc.AddToDependency(e.Node, dep.Key, []tenantfile.Field{{Key: "enabled", Value: value}}); err != nil {
			r.warnf("%s: %v", label, err)
			continue
		}
		r.Added = append(r.Added, fmt.Sprintf("%s: config.dependencies.%s.enabled: %t", label, dep.Key, value))
	}
	return nil
}

// completeUndeclared handles the components the scan found live that the
// file has no active entry for (byKey: its active entries, and those added so
// far; declared: only the file's own), appended as new entries — except under a
// commented-out block, which is the operator's own switch-on-one-at-a-time
// and never gets a duplicate.
func completeUndeclared(doc *tenantfile.Doc, scanned Components, byKey, declared map[string][]string, cat *catalog.Catalog, required map[string]map[string]bool, dec Decider, r *Report) error {
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
				// Only what the file declared: the entries added next to this one
				// in this run are the cluster's several instances, not a rename.
				if others := declared[key]; len(others) > 0 {
					r.Warnings = append(r.Warnings, fmt.Sprintf(
						"%s: added, but components.%s already declares %s; if that is this component under another name, remove the new entry",
						label, key, strings.Join(others, ", ")))
				}
				// What the scan could not resolve may be declared in the file (the
				// operator added it, or it is appended in this very run).
				deps := resolveUnresolved(s, label, scanned, byKey, r)
				if err := dec.decideEntry(label, s, r.warnf); err != nil {
					return err
				}
				if err := doc.AppendEntry(key, s.Name, entryConfig(s, deps)); err != nil {
					return err
				}
				byKey[key] = append(byKey[key], s.Name)
				r.Added = append(r.Added, label+": new entry")
				for _, field := range cat.Schemas[key].ExtraConfig {
					switch fieldPlan(field, s.Type, required[key]) {
					case fieldWarn:
						if _, set := s.ExtraConfig[field.Key]; !set {
							r.Warnings = append(r.Warnings, fmt.Sprintf("%s: config %q has no default; set it by hand", label, field.Key))
						}
					case fieldScaffold:
						r.Warnings = append(r.Warnings, scaffoldWarning(label, field))
					}
				}
			}
		}
	}
	return nil
}

// resolveUnresolved returns the dependency names of a scanned entry about to
// be appended: the scan's own, and for each it could not resolve the name
// resolveDep finds in the file or among the other scanned entries. A
// dependency that still has none is warned about and left out.
func resolveUnresolved(s *Entry, label string, scanned Components, byKey map[string][]string, r *Report) map[string]string {
	deps := make(map[string]string, len(s.Deps))
	keys := make([]string, 0, len(s.Deps))
	for k := range s.Deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		deps[k] = s.Deps[k]
		if deps[k] != "" {
			if names := s.Ambiguous[k]; len(names) > 0 {
				r.Warnings = append(r.Warnings, ambiguousDepWarning(label, k, names, deps[k]))
			}
			continue
		}
		switch names := resolveDep(k, s.Type, scanned, byKey); len(names) {
		case 0:
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"%s: dependency %q could not be resolved; not added, fill it in by hand (nothing live or in the file under components.%s)", label, k, k))
		case 1:
			deps[k] = names[0]
		default:
			deps[k] = names[0]
			r.Warnings = append(r.Warnings, ambiguousDepWarning(label, k, names, names[0]))
		}
	}
	return deps
}

// entryConfig is the config block of a scanned entry, as renderEntries
// writes it, with the dependency names in deps and without the ones still
// unresolved (an empty name would only be broken YAML for the render).
func entryConfig(e *Entry, deps map[string]string) map[string]any {
	config := map[string]any{}
	if e.Type != "" {
		config["type"] = e.Type
	}
	for k, v := range e.ExtraConfig {
		config[k] = v
	}
	depConfigs := map[string]any{}
	for k, v := range deps {
		if v != "" {
			depConfigs[k] = e.depConfig(k, v)
		}
	}
	if len(depConfigs) > 0 {
		config["dependencies"] = depConfigs
	}
	return config
}

func fmtValue(v any) string {
	switch v := v.(type) {
	case string:
		return fmt.Sprintf("%q", v)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+fmtValue(v[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}
