// Package tenantimport scans a live, not-yet-migrated cluster and renders
// a ResourceSetInputProvider skeleton for a tenant: a starting point for
// migration (a Kustomization/CR/HelmRelease inventory turned into
// components.<key>[] entries with their dependencies wired), not a final
// artifact — anything the scan cannot know (secrets, model configuration,
// storage sizing beyond what's inferred) is left for the operator, and
// flagged via a warning wherever this package had to guess or fall back.
//
// The pipeline mirrors the Python client's cmd_tenant.py: CRD scan ->
// deployment scan -> HelmRelease enrichment -> mandatory-type expansion ->
// schema enrichment -> render, fixing two bugs along the way (see the
// project plan's design decision 8): the tenant label
// keos.stratio.com/resourceset-type: tenant-config (Python emitted the
// wrong, now-unselected label), and gosec-agent discovery, which is no
// longer a component key of its own — see internal/catalog's package doc.
package tenantimport

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
}

// Components is component key -> discovered entries, the working set
// threaded through every scan/expand/enrich phase.
type Components map[string][]*Entry

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
