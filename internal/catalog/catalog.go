// Package catalog parses keos-use-cases's ResourceSet templates
// (apps/components/resourceset-apps-*.yaml) into the facts flux-stratio
// needs to migrate an application safely: what each component depends on,
// which Helm chart or CRD it deploys, and — the fix this package exists
// for — exactly where in the tenant YAML a given Kustomization's patches
// come from.
//
// The Python client answered that last question by assuming every
// Kustomization's patches live at components.<key>[name=X].patches. That
// assumption is wrong for a postgres or opensearch instance's gosec-agent
// sub-Kustomization (patches actually live one level deeper, at
// components.<key>[name=X].config.agent.patches) and silently correct-looking
// for a "-postrequisites" companion Kustomization, whose template
// hardcodes `patches: []` and accepts no patch at all. See design
// decision 2 in the project plan.
package catalog

import "k8s.io/apimachinery/pkg/runtime/schema"

// Dependency is one dependency key a component's template resolves via
// `get $dependencies "<key>"`, in first-declared order.
type Dependency struct {
	Key string
	// StorageTypes lists the storage types (e.g. "hdfs") under which this
	// dependency is referenced inside an `if eq $storageType "<type>"`
	// block. Empty means the dependency is unconditional.
	StorageTypes []string
}

// ExtraConfigField is one scalar field a component's template reads via
// `get $componentConfig "<field>"`.
type ExtraConfigField struct {
	Key        string
	Default    string
	HasDefault bool
}

// AnchorKind classifies where a Kustomization's patches come from in the
// tenant YAML.
type AnchorKind int

const (
	// AnchorDefault is the matching components.<key>[name=X] entry's own
	// "patches" key, found by scanning every component key for a
	// matching name — the shape every App in the config file gets when
	// its Anchor field is left unset.
	AnchorDefault AnchorKind = iota
	// AnchorNested is a field nested inside the matching entry's own
	// config, e.g. "config.agent" for a postgres/opensearch gosec agent.
	AnchorNested
	// AnchorNotPatchable means the template hardcodes `patches: []` for
	// this Kustomization (e.g. a "-postrequisites" companion) — no app
	// should be configured to migrate into it.
	AnchorNotPatchable
	// AnchorUnknown means a `patches:` line was found in a Kustomization
	// that belongs to this component (its name references the component's
	// own instance), but in a shape this parser doesn't recognize. It is
	// never silently treated as AnchorDefault — ResolveAnchor errors
	// instead, matching this package's rule to fail loudly rather than
	// write to the wrong place on an unrecognized template shape.
	AnchorUnknown
)

// String renders the AnchorKind's name, for error messages and test
// output.
func (k AnchorKind) String() string {
	switch k {
	case AnchorDefault:
		return "default"
	case AnchorNested:
		return "nested"
	case AnchorNotPatchable:
		return "not-patchable"
	default:
		return "unknown"
	}
}

// KustomizationAnchor is one Kustomization a component's template
// declares, keyed by the suffix appended to the component instance's own
// name to form its rendered Kustomization name: "" for the component's
// own Kustomization, "-gosec-agent" or "-postrequisites" for a
// sub-resource.
type KustomizationAnchor struct {
	Suffix string
	Kind   AnchorKind
	Field  string // set when Kind == AnchorNested, e.g. "config.agent"
}

// Schema is one component key's parsed facts.
type Schema struct {
	Key            string
	Dependencies   []Dependency
	ExtraConfig    []ExtraConfigField
	Kustomizations []KustomizationAnchor
	// ChartName is the Helm chart this component's own (AnchorDefault)
	// Kustomization deploys, when its `path:` names one under
	// components/<chart>/app/; empty when none was found (e.g. a
	// component whose Kustomization deploys an operator custom resource
	// directly, with no chart-shaped path).
	ChartName string
}

// isEmpty reports whether s carries no extracted facts at all — used to
// let a later, populated re-registration of the same component key win
// over an earlier empty one (a defensive rule ported from the Python
// client, in case a component key's block is ever split across files).
func (s Schema) isEmpty() bool {
	return len(s.Dependencies) == 0 && len(s.ExtraConfig) == 0 && len(s.Kustomizations) == 0
}

// ChartMapping resolves a Helm chart name to the component key(s) whose
// template declares a Kustomization deploying it.
type ChartMapping struct {
	// Key is the sole owning component key, set only when exactly one
	// component's template deploys this chart.
	Key string
	// Ambiguous lists every owning component key when more than one
	// component's template deploys the same chart name (e.g. "pgbackup",
	// shared by pgbackupLogical and pgbackupPhysical) — callers must
	// disambiguate some other way (for pgbackup: the CRD's own
	// spec.type) instead of guessing, unlike the Python client, which
	// collapsed this case to a bare nil and then misrouted it through its
	// gosec-agent heuristics.
	Ambiguous []string
}

// CRDInfo is one CRD a component's template declares via its
// healthCheckExprs.
type CRDInfo struct {
	// GVK is the CRD's GroupVersionKind, as declared in the template
	// (e.g. {Group: "postgres.stratio.com", Version: "v1", Kind: "PgCluster"}) —
	// enough to list live instances of it directly, with no separate
	// lookup needed.
	GVK schema.GroupVersionKind
	// ComponentKey is the component key whose template declares this CRD.
	ComponentKey string
}

// Catalog is every fact this package extracts from
// keos-use-cases/apps/components/resourceset-apps-*.yaml.
type Catalog struct {
	// Schemas maps component key -> Schema.
	Schemas map[string]Schema
	// Charts maps Helm chart name -> ChartMapping.
	Charts map[string]ChartMapping
	// CRDs maps CRD plural (e.g. "pgclusters.postgres.stratio.com") -> CRDInfo.
	CRDs map[string]CRDInfo

	anchorsBySuffix map[string]ResolvedAnchor
}
