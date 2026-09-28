package config

import (
	"fmt"
	"strings"
	"text/template"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CatalogFile is the catalog's file name under UserDir().
const CatalogFile = "catalog.yaml"

// Catalog is flux-stratio's component catalog: every type of component the
// migration pipeline supports, keyed by Type. It carries no
// environment-specific value (no instance name, namespace or cluster) —
// internal/components matches live objects against each type's Match
// selectors at run time to find the instances.
type Catalog struct {
	Types []ComponentType `yaml:"types"`
}

// ComponentType describes one kind of migratable component: how to
// recognize a live legacy object as an instance of it (Match), where its
// GitOps counterpart lives (Component, Rset, the Entry/Object/
// Kustomization name templates), how to compare the two (Chart, Exclude),
// and any precondition its migration needs (Prepare).
//
// Entry, Object and Kustomization are text/template strings (see
// internal/components for the data and functions available) because the
// instance names they produce are environment-specific; the *rule* that
// derives them from a live object is not.
type ComponentType struct {
	// Type is this type's unique identifier, shown in output and used by
	// `--as <type>/<entry>`.
	Type string `yaml:"type"`
	// Name is a human-readable label, shown in progress/log output.
	Name string `yaml:"name"`
	// Component is the tenant file's components.<key> this type's
	// instances are declared under (e.g. "postgres", "dgAgent").
	Component string `yaml:"component"`
	// Rset is the path, relative to keos-use-cases, to the ResourceSet
	// template that declares this type's Kustomization.
	Rset string `yaml:"rset"`
	// Entry is a template rendering the name of the components.<Component>
	// entry an instance maps to in the tenant file. Defaults to
	// DefaultEntry (the live object's own name).
	Entry string `yaml:"entry,omitempty"`
	// Object is a template rendering the name of the HelmRelease or custom
	// resource inside the rendered Kustomization to diff against the live
	// object. Defaults to DefaultObject (the entry name).
	Object string `yaml:"object,omitempty"`
	// Kustomization is a template rendering the name of the rendered
	// Kustomization to inspect. Defaults to DefaultKustomization.
	Kustomization string `yaml:"kustomization,omitempty"`
	// Anchor overrides where, relative to this type's own tenant-file
	// entry, its Kustomization's patches are read from — a dotted field
	// path, e.g. "config.agent" for a postgres/opensearch gosec agent.
	// internal/catalog derives this automatically from the templates, so
	// it is normally left unset; when set, internal/tenantfile.Splice
	// validates it against what the catalog independently derives and
	// fails loudly on a mismatch rather than writing to the wrong place.
	Anchor string `yaml:"anchor,omitempty"`
	// Chart, if set, selects env-var/chart diff mode (internal/diff's
	// Helm-values comparison) instead of manifest diff mode. Unset means
	// this type is a CRD/manifest object compared directly against the
	// live resource.
	Chart *Chart `yaml:"chart,omitempty"`
	// Match selects the live legacy objects that are instances of this
	// type.
	Match Match `yaml:"match"`
	// Prepare, if set, names a step in internal/prepare that must hold
	// before an instance of this type can be migrated. `apps migrate`
	// satisfies it automatically as its first pipeline stage.
	Prepare string `yaml:"prepare,omitempty"`
	// Exclude lists dot-paths to drop from the computed diff/patch —
	// fields the GitOps side is authoritative for and must never be
	// back-ported from the live cluster (identity/vault/governance values).
	Exclude []string `yaml:"exclude,omitempty"`
	// Notes is a free-text hint shown to the operator, e.g. alongside a
	// Prepare step that blocks migration.
	Notes string `yaml:"notes,omitempty"`
}

// Chart locates a chart-mode type's Helm chart source.
type Chart struct {
	// Path is the chart directory, relative to Environment.ChartsRoot().
	Path string `yaml:"path"`
	// ValuesRoot pins which .Values root to prefer when a chart mixes more
	// than one flavor's values under the same directory tree (e.g.
	// bdl-datarest's pginternal/pgmd5/pgtls flavors).
	ValuesRoot string `yaml:"valuesRoot,omitempty"`
}

// Match selects live objects: an object matches when its kind is one of
// Kinds and every selector that is set matches.
type Match struct {
	// Kinds lists the accepted "group/version/Kind" (or "version/Kind"
	// for the core group) of a matching live object. Required: it's what
	// stops, say, a PgDatabase CR that happens to share a chart app's name
	// from ever being taken for that app.
	Kinds []string `yaml:"kinds"`
	// Labels is matched against the live object's metadata.labels.
	Labels *Selector `yaml:"labels,omitempty"`
	// Annotations is matched against the live object's
	// metadata.annotations — where CCT records a legacy app's
	// cct.stratio.com/application_service and application_model.
	Annotations *Selector `yaml:"annotations,omitempty"`
}

// Selector has Kubernetes label-selector semantics (every matchLabels pair
// and every matchExpressions requirement must hold), applied to a plain
// string map. It's evaluated by internal/components rather than converted
// to a labels.Selector, because annotation values (e.g. "Stratio Command
// Center") aren't valid label values and would be rejected by one.
type Selector struct {
	MatchLabels      map[string]string     `yaml:"matchLabels,omitempty"`
	MatchExpressions []SelectorRequirement `yaml:"matchExpressions,omitempty"`
}

// SelectorRequirement is one matchExpressions entry.
type SelectorRequirement struct {
	Key      string   `yaml:"key"`
	Operator string   `yaml:"operator"`
	Values   []string `yaml:"values,omitempty"`
}

// The operators a SelectorRequirement accepts, as in Kubernetes'
// metav1.LabelSelectorOperator.
const (
	OpIn           = "In"
	OpNotIn        = "NotIn"
	OpExists       = "Exists"
	OpDoesNotExist = "DoesNotExist"
)

// The name templates a ComponentType gets when it leaves Entry, Object or
// Kustomization unset — the convention every keos-use-cases component
// follows: the tenant entry is named after the live object, the object
// after the entry, and the Kustomization "apps-<object>".
const (
	DefaultEntry         = "{{ .Live.Name }}"
	DefaultObject        = "{{ .Entry }}"
	DefaultKustomization = "apps-{{ .Object }}"
)

// Find returns the type with the given id, or nil if none matches.
func (c Catalog) Find(typeID string) *ComponentType {
	for i := range c.Types {
		if c.Types[i].Type == typeID {
			return &c.Types[i]
		}
	}
	return nil
}

// Kinds returns the union of every type's Match.Kinds, parsed, in
// first-declared order — the kinds internal/discovery must list for
// classification to see every candidate.
func (c Catalog) Kinds() []schema.GroupVersionKind {
	seen := map[schema.GroupVersionKind]bool{}
	var out []schema.GroupVersionKind
	for _, t := range c.Types {
		for _, k := range t.Match.Kinds {
			gvk, err := ParseKind(k)
			if err != nil || seen[gvk] {
				continue // validate already rejected a malformed kind
			}
			seen[gvk] = true
			out = append(out, gvk)
		}
	}
	return out
}

// EntryTemplate returns the type's Entry template, or DefaultEntry when
// unset (likewise ObjectTemplate and KustomizationTemplate).
func (t ComponentType) EntryTemplate() string { return orDefault(t.Entry, DefaultEntry) }

// ObjectTemplate returns Object, or DefaultObject — see EntryTemplate.
func (t ComponentType) ObjectTemplate() string { return orDefault(t.Object, DefaultObject) }

// KustomizationTemplate returns Kustomization, or DefaultKustomization —
// see EntryTemplate.
func (t ComponentType) KustomizationTemplate() string {
	return orDefault(t.Kustomization, DefaultKustomization)
}

// ChartPath is Chart.Path, or "" for a manifest-mode type.
func (t ComponentType) ChartPath() string {
	if t.Chart == nil {
		return ""
	}
	return t.Chart.Path
}

// ValuesRoot is Chart.ValuesRoot, or "" for a manifest-mode type.
func (t ComponentType) ValuesRoot() string {
	if t.Chart == nil {
		return ""
	}
	return t.Chart.ValuesRoot
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ParseKind parses a Match.Kinds entry: "group/version/Kind", or
// "version/Kind" for the core API group.
func ParseKind(s string) (schema.GroupVersionKind, error) {
	parts := strings.Split(s, "/")
	switch {
	case len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != "":
		return schema.GroupVersionKind{Group: parts[0], Version: parts[1], Kind: parts[2]}, nil
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return schema.GroupVersionKind{Version: parts[0], Kind: parts[1]}, nil
	default:
		return schema.GroupVersionKind{}, fmt.Errorf("kind %q must be group/version/Kind (or version/Kind for the core group)", s)
	}
}

// TemplateFuncs are the functions available to Entry/Object/Kustomization
// templates. Declared here (not in internal/components, which renders
// them) so validate can parse every template up front with the exact same
// function set.
var TemplateFuncs = template.FuncMap{
	"trimSuffix": func(suffix, s string) string { return strings.TrimSuffix(s, suffix) },
	"trimPrefix": func(prefix, s string) string { return strings.TrimPrefix(s, prefix) },
	"replace":    func(old, replacement, s string) string { return strings.ReplaceAll(s, old, replacement) },
	"lower":      strings.ToLower,
}

func (c Catalog) validate() error {
	var problems []string
	if len(c.Types) == 0 {
		problems = append(problems, "types: at least one component type is required")
	}

	seen := make(map[string]bool, len(c.Types))
	for i, t := range c.Types {
		label := t.Type
		if label == "" {
			label = fmt.Sprintf("types[%d]", i)
		}
		add := func(format string, args ...any) {
			problems = append(problems, label+": "+fmt.Sprintf(format, args...))
		}

		if t.Type == "" {
			add("type is required")
		} else if seen[t.Type] {
			problems = append(problems, fmt.Sprintf("types[%d]: duplicate type %q", i, t.Type))
		}
		seen[t.Type] = true
		if t.Name == "" {
			add("name is required")
		}
		if t.Component == "" {
			add("component is required")
		}
		if t.Rset == "" {
			add("rset is required")
		}
		if t.Chart != nil && t.Chart.Path == "" {
			add("chart.path is required when chart is set")
		}

		if len(t.Match.Kinds) == 0 {
			add("match.kinds is required")
		}
		for _, k := range t.Match.Kinds {
			if _, err := ParseKind(k); err != nil {
				add("match.kinds: %v", err)
			}
		}
		if err := t.Match.Labels.validate(); err != nil {
			add("match.labels: %v", err)
		}
		if err := t.Match.Annotations.validate(); err != nil {
			add("match.annotations: %v", err)
		}

		for _, f := range []struct{ field, tmpl string }{
			{"entry", t.EntryTemplate()}, {"object", t.ObjectTemplate()}, {"kustomization", t.KustomizationTemplate()},
		} {
			if _, err := template.New(f.field).Funcs(TemplateFuncs).Parse(f.tmpl); err != nil {
				add("%s: %v", f.field, err)
			}
		}
	}

	if len(problems) == 0 {
		return nil
	}
	msg := "invalid catalog:"
	for _, p := range problems {
		msg += "\n  - " + p
	}
	return fmt.Errorf("%s", msg)
}

func (s *Selector) validate() error {
	if s == nil {
		return nil
	}
	for _, r := range s.MatchExpressions {
		if r.Key == "" {
			return fmt.Errorf("matchExpressions: key is required")
		}
		switch r.Operator {
		case OpIn, OpNotIn:
			if len(r.Values) == 0 {
				return fmt.Errorf("matchExpressions[%s]: operator %s needs at least one value", r.Key, r.Operator)
			}
		case OpExists, OpDoesNotExist:
			if len(r.Values) > 0 {
				return fmt.Errorf("matchExpressions[%s]: operator %s takes no values", r.Key, r.Operator)
			}
		default:
			return fmt.Errorf("matchExpressions[%s]: unknown operator %q (want In, NotIn, Exists or DoesNotExist)", r.Key, r.Operator)
		}
	}
	return nil
}
