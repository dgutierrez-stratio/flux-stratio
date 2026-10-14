// Package tenantimport scans a live, not-yet-migrated cluster and renders
// a ResourceSetInputProvider skeleton for a tenant: a starting point for
// migration (a Kustomization/CR/HelmRelease inventory turned into
// components.<key>[] entries with their dependencies wired), not a final
// artifact — anything the scan cannot know (secrets, model configuration,
// storage sizing beyond what's inferred) is left for the operator, and
// flagged via a warning wherever this package had to guess or fall back.
//
// The pipeline mirrors the Python client's cmd_tenant.py: CRD scan ->
// workload classification (what CCT stamped on the legacy apps, through the
// component catalog) -> HelmRelease enrichment -> mandatory-type expansion ->
// schema enrichment -> render, fixing two bugs along the way (see the
// project plan's design decision 8): the tenant label
// keos.stratio.com/resourceset-type: tenant-config (Python emitted the
// wrong, now-unselected label), and gosec-agent discovery, which is no
// longer a component key of its own — see internal/catalog's package doc.
package tenantimport

import "fmt"

// Entry is one discovered (or skeleton) component instance, threaded
// through the scan -> expand -> enrich pipeline.
type Entry struct {
	Name string
	// Type is the storage type ("hdfs", "s3", ...), once known.
	Type string
	// Deps maps a dependency key to the instance name it resolves to. A
	// key present with an empty value means "known to be needed, but
	// unresolved" — still emitted so the generated YAML's structure is
	// complete, with a warning logged at enrichment time.
	Deps map[string]string
	// HelmReleaseSpec is the live HelmRelease's spec, captured when this
	// entry was discovered (or enriched) via one.
	HelmReleaseSpec map[string]any
	// ExtraConfig holds extra scalar config fields resolved during
	// enrichment (see internal/catalog.Schema.ExtraConfig).
	ExtraConfig map[string]any
	// DepEnabled maps a dependency key to the HelmRelease values path that
	// shows this entry uses it (see scanUses): its `enabled: true` is written.
	DepEnabled map[string]string
	// DepAssumed marks the dependencies with a chart.uses rule that have no
	// HelmRelease evidence either way but whose component runs on the cluster
	// (see assumeLiveUses): `enabled: true` is written on that weaker ground.
	DepAssumed map[string]bool
	// FromEnv is the config the entry's legacy workload's env supplied (see
	// config.EnvConfig), by config field: the cluster's own value, which wins
	// over the template default.
	FromEnv map[string]string
	// Ambiguous maps a dependency key to every entry it could have resolved to,
	// when the scan had to choose among several (Deps holds the choice).
	Ambiguous map[string][]string
	// DepUnknown marks the dependencies with a chart.uses rule that could not
	// be decided because the entry has no live HelmRelease yet.
	DepUnknown map[string]bool
}

// Components is component key -> discovered entries, the working set
// threaded through every scan/expand/enrich phase.
type Components map[string][]*Entry

// tenantStorage is the storage type the tenant runs on: hdfs when an hdfs
// component is live, otherwise the ResourceSets' default.
func (c Components) tenantStorage() string {
	if len(c["hdfs"]) > 0 {
		return "hdfs"
	}
	return "s3"
}

// find returns the entry named name under key, or nil.
func (c Components) find(key, name string) *Entry {
	for _, e := range c[key] {
		if e.Name == name {
			return e
		}
	}
	return nil
}

// names returns the tenant-visible names of every entry under key.
func (c Components) names(key string) []string {
	var out []string
	for _, e := range c[key] {
		out = append(out, e.Name)
	}
	return out
}

// depConfig is the config.dependencies.<key> block of e: its name, and
// `enabled: true` when the entry's HelmRelease shows it uses the dependency
// and the name is resolved.
func (e *Entry) depConfig(key, name string) map[string]any {
	dep := map[string]any{"name": name}
	if name != "" && (e.DepEnabled[key] != "" || e.DepAssumed[key]) {
		dep["enabled"] = true
	}
	return dep
}

// enabledReason says why e's dependency key is written `enabled: true`: the
// HelmRelease value that proves it, or the assumption that the component
// running on the cluster is one it uses. "" when neither applies (e may be nil).
func (e *Entry) enabledReason(key string) string {
	switch {
	case e == nil:
		return ""
	case e.DepEnabled[key] != "":
		return fmt.Sprintf("spec.values.%s is set in its HelmRelease", e.DepEnabled[key])
	case e.DepAssumed[key]:
		return "assumed: it runs on the cluster and no HelmRelease says otherwise"
	}
	return ""
}

// fromEnv is the value e's legacy workload's env gave config field key (e may
// be nil).
func (e *Entry) fromEnv(key string) (string, bool) {
	if e == nil {
		return "", false
	}
	v, ok := e.FromEnv[key]
	return v, ok
}
