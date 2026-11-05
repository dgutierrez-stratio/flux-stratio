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
	// DepGate holds, by dependency key, every gated dependency of this entry
	// that runs on the cluster: the proposal to enable it (see markGates).
	DepGate map[string]DepGate
	// DepAbsent marks the gated dependencies that do not run on the cluster.
	DepAbsent map[string]bool
	// DepState is the answer to each proposal (see Decider.decide). Only an
	// answered dependency has an entry.
	DepState map[string]DepState
	// FromEnv is the config the entry's legacy workload's env supplied (see
	// config.EnvConfig), by config field: the cluster's own value, which wins
	// over the template default.
	FromEnv map[string]string
	// Ambiguous maps a dependency key to every entry it could have resolved to,
	// when the scan had to choose among several (Deps holds the choice).
	Ambiguous map[string][]string
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

// depConfig is the config.dependencies.<key> block of e: its name, and its
// `enabled` when the operator answered the proposal (true, or false when they
// left it off) and the name is resolved.
func (e *Entry) depConfig(key, name string) map[string]any {
	dep := map[string]any{"name": name}
	if name != "" {
		switch e.DepState[key] {
		case DepOn:
			dep["enabled"] = true
		case DepOff:
			dep["enabled"] = false
		}
	}
	return dep
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
