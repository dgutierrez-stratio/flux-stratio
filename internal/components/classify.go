// Package components turns live legacy objects into resolved component
// instances (config.App): it classifies each live object against the
// component catalog's Match selectors (Classify), derives the instance's
// tenant-file entry, object and Kustomization names from the type's
// templates, checks them against the tenant file, and asks the operator —
// only when it genuinely can't infer an answer — which type or which entry
// an ambiguous live object maps to (Resolve/ResolveAll).
//
// Nothing it derives or is told is persisted: every run re-derives the
// same instances from the live cluster and the tenant file, and an
// operator's answer is either given again interactively or up front via
// `--as <type>/<entry>`.
package components

import (
	"bytes"
	"fmt"
	"path"
	"sort"
	"strings"
	"text/template"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Stratio/flux-stratio/internal/config"
)

// Instance is one component instance Classify found: a catalog type, the
// tenant-file entry name its Entry template derives, and every live object
// that classified as it (the primary one first). Its entry may still be
// corrected by Resolve against the tenant file.
type Instance struct {
	Type  *config.ComponentType
	Entry string
	Live  []*unstructured.Unstructured
}

// Primary is the instance's first (anchor) live object.
func (i Instance) Primary() *unstructured.Unstructured { return i.Live[0] }

// Label renders the instance for a prompt or an error message:
// "<type> <namespace>/<live name> → entry <entry>".
func (i Instance) Label() string {
	p := i.Primary()
	return fmt.Sprintf("%s %s/%s → entry %q", i.Type.Type, p.GetNamespace(), p.GetName(), i.Entry)
}

// Classify matches every object against every catalog type and groups the
// matches into instances by (type, namespace, entry) — so a chart deploying
// several anchor workloads that all derive the same entry becomes one
// instance, while the same entry live in two namespaces stays two (for
// Resolve to ask about). An object with ownerReferences is never an
// instance anchor (e.g. the Deployment a PgBouncer CR owns, or rocket's
// own sub-Deployments) and is skipped, as is one CCT annotated as another
// tenant's (see TenantAnnotation). An object may classify as more than
// one type; each is its own instance, and Resolve asks which one is meant.
//
// A chart type's siblings (Chart.Siblings — genai's genai-ui next to its
// genai-api anchor) never start an instance: each joins the one instance
// of its type in its namespace, after the anchors, so Primary stays the
// anchor. A sibling with no such instance, or several, joins none, and
// warn (when set) says why.
//
// Instances come back sorted by catalog type order, then entry, then
// namespace — deterministic regardless of the API's own list order.
func Classify(cat *config.Catalog, objs []*unstructured.Unstructured, tenant string, warn func(format string, a ...any)) ([]Instance, error) {
	type key struct {
		typeIdx          int
		namespace, entry string
	}
	byKey := map[key]*Instance{}
	var keys []key

	for _, obj := range objs {
		if len(obj.GetOwnerReferences()) > 0 || !ownedByTenant(obj, tenant) {
			continue
		}
		for ti := range cat.Types {
			t := &cat.Types[ti]
			if !Matches(t, obj) {
				continue
			}
			entry, err := render(t.EntryTemplate(), newTemplateData(obj, "", "", tenant))
			if err != nil {
				return nil, fmt.Errorf("type %q: rendering entry for %s %s/%s: %w", t.Type, obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
			}
			k := key{ti, obj.GetNamespace(), entry}
			inst, ok := byKey[k]
			if !ok {
				inst = &Instance{Type: t, Entry: entry}
				byKey[k] = inst
				keys = append(keys, k)
			}
			inst.Live = append(inst.Live, obj)
		}
	}

	siblings := attachSiblings(cat, objs, tenant, byKey, keys, warn)

	sort.Slice(keys, func(a, b int) bool {
		ka, kb := keys[a], keys[b]
		if ka.typeIdx != kb.typeIdx {
			return ka.typeIdx < kb.typeIdx
		}
		if ka.entry != kb.entry {
			return ka.entry < kb.entry
		}
		return ka.namespace < kb.namespace
	})
	out := make([]Instance, 0, len(keys))
	for _, k := range keys {
		inst := byKey[k]
		sort.SliceStable(inst.Live, func(a, b int) bool { return inst.Live[a].GetName() < inst.Live[b].GetName() })
		sibs := siblings[inst]
		sort.SliceStable(sibs, func(a, b int) bool { return sibs[a].GetName() < sibs[b].GetName() })
		inst.Live = append(inst.Live, sibs...)
		out = append(out, *inst)
	}
	return out, nil
}

// attachSiblings finds every object one of cat's chart types selects as a
// sibling (Chart.Siblings) and returns, per instance, the siblings that
// join it: an object joins the one instance of that type in its own
// namespace. With none there's no anchor for it to belong to (the anchor
// isn't live); with several, nothing says which one it belongs to — either
// way it joins none, and warn says so.
func attachSiblings[K comparable](cat *config.Catalog, objs []*unstructured.Unstructured, tenant string, byKey map[K]*Instance, keys []K, warn func(string, ...any)) map[*Instance][]*unstructured.Unstructured {
	out := map[*Instance][]*unstructured.Unstructured{}
	for _, obj := range objs {
		if len(obj.GetOwnerReferences()) > 0 || !ownedByTenant(obj, tenant) {
			continue
		}
		for ti := range cat.Types {
			t := &cat.Types[ti]
			if !SiblingMatches(t, obj) {
				continue
			}
			var candidates []*Instance
			for _, k := range keys {
				if inst := byKey[k]; inst.Type == t && inst.Primary().GetNamespace() == obj.GetNamespace() && !hasObject(inst.Live, obj) {
					candidates = append(candidates, inst)
				}
			}
			label := fmt.Sprintf("%s %s/%s", obj.GetKind(), obj.GetNamespace(), obj.GetName())
			switch len(candidates) {
			case 1:
				out[candidates[0]] = append(out[candidates[0]], obj)
			case 0:
				warnf(warn, "%s is a %s sibling, but no %s instance is live in namespace %s to attach it to; "+
					"it won't be captured or compared as part of one", label, t.Type, t.Type, obj.GetNamespace())
			default:
				entries := make([]string, len(candidates))
				for i, c := range candidates {
					entries[i] = c.Entry
				}
				sort.Strings(entries)
				warnf(warn, "%s is a %s sibling, but %d %s instances are live in namespace %s (entries %s) "+
					"and nothing says which it belongs to; it won't be captured or compared as part of either",
					label, t.Type, len(candidates), t.Type, obj.GetNamespace(), strings.Join(entries, ", "))
			}
		}
	}
	return out
}

func hasObject(objs []*unstructured.Unstructured, obj *unstructured.Unstructured) bool {
	for _, o := range objs {
		if o == obj {
			return true
		}
	}
	return false
}

func warnf(warn func(string, ...any), format string, a ...any) {
	if warn != nil {
		warn(format, a...)
	}
}

// SiblingMatches reports whether obj is one of t's chart siblings
// (Chart.Siblings).
func SiblingMatches(t *config.ComponentType, obj *unstructured.Unstructured) bool {
	for _, m := range t.SiblingMatches() {
		if matchKinds(m.Kinds, obj) && selectorMatches(m.Labels, obj.GetLabels()) && selectorMatches(m.Annotations, obj.GetAnnotations()) {
			return true
		}
	}
	return false
}

// TenantAnnotation is where CCT records which tenant a legacy object belongs
// to. The same cluster runs the platform's own components under tenant
// "keos" alongside a customer tenant's — eosdev has an "opensearch1"
// OsCluster (and gosec agent) in both keos-core (tenant keos) and
// stratio-datastores (tenant stratio) — and a run only ever migrates one
// tenant.
const TenantAnnotation = "cct.stratio.com/application_tenant"

// ownedByTenant reports whether obj may be an instance for tenant: true
// unless CCT annotated it as some other tenant's. An object without the
// annotation isn't excluded, since not everything a type could select is
// CCT-deployed.
func ownedByTenant(obj *unstructured.Unstructured, tenant string) bool {
	owner, ok := obj.GetAnnotations()[TenantAnnotation]
	return !ok || owner == "" || tenant == "" || owner == tenant
}

// Matches reports whether obj is selected by t: its group and kind are one
// of t.Match.Kinds (the version isn't compared, so a type keeps matching
// across an API version bump) and every set label/annotation selector
// holds.
func Matches(t *config.ComponentType, obj *unstructured.Unstructured) bool {
	return kindMatches(t, obj) &&
		selectorMatches(t.Match.Labels, obj.GetLabels()) &&
		selectorMatches(t.Match.Annotations, obj.GetAnnotations())
}

func kindMatches(t *config.ComponentType, obj *unstructured.Unstructured) bool {
	return matchKinds(t.Match.Kinds, obj)
}

func matchKinds(kinds []string, obj *unstructured.Unstructured) bool {
	gvk := obj.GroupVersionKind()
	for _, k := range kinds {
		want, err := config.ParseKind(k)
		if err == nil && want.Group == gvk.Group && want.Kind == gvk.Kind {
			return true
		}
	}
	return false
}

// Labels Flux's helm-controller sets on every object a HelmRelease renders,
// and the tenant label keos puts on it — CCT's own annotations, including
// TenantAnnotation, are gone once a HelmRelease has adopted an object.
const (
	helmReleaseNameLabel      = "helm.toolkit.fluxcd.io/name"
	helmReleaseNamespaceLabel = "helm.toolkit.fluxcd.io/namespace"
	keosTenantLabel           = "keos.stratio.com/tenant"
)

// ManagedHelmRelease returns the "<namespace>/<name>" of the Flux
// HelmRelease that rendered obj, or "" when none did.
func ManagedHelmRelease(obj *unstructured.Unstructured) string {
	l := obj.GetLabels()
	name, ns := l[helmReleaseNameLabel], l[helmReleaseNamespaceLabel]
	if name == "" || ns == "" {
		return ""
	}
	return ns + "/" + name
}

// ManagedMatches reports whether obj is an already-migrated workload of
// the chart-mode type t: its group and kind are one of t.Match.Kinds, and
// the HelmRelease that rendered it (charts maps "<namespace>/<name>" to its
// spec.chart.spec.chart) deploys t's chart — the base name of Chart.Path.
func ManagedMatches(t *config.ComponentType, obj *unstructured.Unstructured, charts map[string]string) bool {
	if t.Chart == nil || !kindMatches(t, obj) {
		return false
	}
	hr := ManagedHelmRelease(obj)
	chart, ok := charts[hr]
	return hr != "" && ok && chart == path.Base(t.Chart.Path)
}

// helmReleaseCharts maps every HelmRelease in objs ("<namespace>/<name>")
// to the chart name it deploys. A HelmRelease referencing its chart via
// spec.chartRef has no chart name and is left out.
func helmReleaseCharts(objs []*unstructured.Unstructured) map[string]string {
	charts := map[string]string{}
	for _, obj := range objs {
		gvk := obj.GroupVersionKind()
		if gvk.Group != "helm.toolkit.fluxcd.io" || gvk.Kind != "HelmRelease" {
			continue
		}
		if chart, _, _ := unstructured.NestedString(obj.Object, "spec", "chart", "spec", "chart"); chart != "" {
			charts[obj.GetNamespace()+"/"+obj.GetName()] = chart
		}
	}
	return charts
}

func selectorMatches(sel *config.Selector, values map[string]string) bool {
	if sel == nil {
		return true
	}
	for k, want := range sel.MatchLabels {
		if got, ok := values[k]; !ok || got != want {
			return false
		}
	}
	for _, r := range sel.MatchExpressions {
		got, ok := values[r.Key]
		switch r.Operator {
		case config.OpIn:
			if !ok || !contains(r.Values, got) {
				return false
			}
		case config.OpNotIn:
			if ok && contains(r.Values, got) {
				return false
			}
		case config.OpExists:
			if !ok {
				return false
			}
		case config.OpDoesNotExist:
			if ok {
				return false
			}
		default:
			return false // config validation rejects any other operator
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// liveData is what a template sees as .Live.
type liveData struct {
	Name, Namespace     string
	Labels, Annotations map[string]string
}

// Label returns the live object's label k, or "" — callable from a
// template as {{ .Live.Label "some/key" }}, since dotted/slashed keys
// can't be reached with plain field syntax.
func (l liveData) Label(k string) string { return l.Labels[k] }

// Annotation is Label's counterpart for annotations.
func (l liveData) Annotation(k string) string { return l.Annotations[k] }

// templateData is the data an Entry/Object/Kustomization template renders
// against. Entry is empty while rendering Entry itself, Object while
// rendering Object.
type templateData struct {
	Live   liveData
	Entry  string
	Object string
	Tenant string
}

func newTemplateData(obj *unstructured.Unstructured, entry, object, tenant string) templateData {
	return templateData{
		Live: liveData{
			Name: obj.GetName(), Namespace: obj.GetNamespace(),
			Labels: obj.GetLabels(), Annotations: obj.GetAnnotations(),
		},
		Entry: entry, Object: object, Tenant: tenant,
	}
}

func render(tmpl string, data templateData) (string, error) {
	t, err := template.New("name").Funcs(config.TemplateFuncs).Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	if buf.Len() == 0 {
		return "", fmt.Errorf("template %q rendered an empty name", tmpl)
	}
	return buf.String(), nil
}
