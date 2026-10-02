package components

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

// Options configures a resolution run.
type Options struct {
	Catalog *config.Catalog
	// Objects are the live objects to classify — typically
	// internal/discovery.Index.Objects().
	Objects []*unstructured.Unstructured
	// Tenant is the tenant name, available to templates as .Tenant.
	Tenant string
	// Doc is the tenant file, when the caller needs every instance's
	// entry to exist in it (apps diff/migrate, which render desired state
	// from it). Nil skips every tenant-file check — apps backup and
	// --drift read only the live cluster and don't need one.
	Doc *tenantfile.Doc
	// Prompter asks the operator when an answer can't be inferred.
	// Defaults to NonInteractive.
	Prompter Prompter
	// As is the `--as <type>[/<entry>]` answer given up front: it narrows
	// a Resolve to one type and, with an entry, pins the tenant-file entry
	// the live object maps to.
	As  string
	Log *log.Logger
}

// Resolve returns the single instance the operator means by name — a live
// object's name or an instance's derived object name, falling back to its
// derived tenant-file entry name — resolved
// against the tenant file (when opts.Doc is set). When name matches more
// than one instance (several types, or the same entry live in several
// namespaces), the operator is asked which one.
func Resolve(opts Options, name string) (config.App, error) {
	instances, notes, err := classify(opts.Catalog, opts.Objects, opts.Tenant)
	if err != nil {
		return config.App{}, err
	}
	asType, asEntry, err := parseAs(opts)
	if err != nil {
		return config.App{}, err
	}

	// Two tiers: an instance whose own live or object name is name beats
	// one merely sharing name as its tenant entry — "psql" is the
	// PgCluster, not the gosec agent that migrates into the psql entry.
	var exact, byEntry []Instance
	for _, inst := range instances {
		if asType != "" && inst.Type.Type != asType {
			continue
		}
		switch {
		case inst.namedAs(name, opts.Tenant):
			exact = append(exact, inst)
		case inst.Entry == name:
			byEntry = append(byEntry, inst)
		}
	}
	candidates := exact
	if len(candidates) == 0 {
		candidates = byEntry
	}
	if len(candidates) == 0 {
		candidates, err = managedInstances(opts, name, asType)
		if err != nil {
			return config.App{}, err
		}
	}
	if len(candidates) == 0 {
		return config.App{}, noMatchError(opts, name, asType)
	}

	chosen := candidates[0]
	if len(candidates) > 1 {
		labels := make([]string, len(candidates))
		for i, c := range candidates {
			labels[i] = c.Label()
		}
		i, err := prompter(opts).Choose(fmt.Sprintf("%q matches more than one live component instance; which one?", name), labels)
		if err != nil {
			return config.App{}, answerError(fmt.Sprintf("%q is ambiguous (%s)", name, strings.Join(labels, "; ")), err)
		}
		chosen = candidates[i]
	}

	reportSiblingNotes(opts, notes, map[string]bool{chosen.Type.Type: true})
	if asEntry != "" {
		chosen.Entry = asEntry
	}
	if err := resolveEntry(opts, &chosen, asEntry != "", false); err != nil {
		return config.App{}, err
	}
	return toApp(chosen, opts.Tenant)
}

// reportSiblingNotes warns about the siblings that joined no instance, for
// the types being worked on (types); the rest only at debug level, since
// they're unrelated to what was asked for.
func reportSiblingNotes(opts Options, notes []SiblingNote, types map[string]bool) {
	if len(notes) == 0 {
		return
	}
	charts := helmReleaseCharts(opts.Objects)
	for _, n := range notes {
		if types[n.Type.Type] {
			opts.Log.Warningf("%s", n.Message(charts))
		} else {
			opts.Log.Debugf("%s", n.Message(charts))
		}
	}
}

// managedInstances is Resolve's fallback for an app already migrated:
// its HelmRelease adopted the legacy workloads and Helm rewrote their
// metadata, so the CCT annotations the selectors (and the tenant check)
// rely on are gone. name is then either the HelmRelease itself or one of
// the workloads it renders (labelled as the run's tenant) — any of them,
// anchor or sibling: genai-ui finds the same genai instance genai-api
// does. The instance is that HelmRelease's, for every chart-mode type
// whose chart it deploys (one per type when several share the chart — the
// gosec agents — for Resolve to ask about):
//   - its object is the HelmRelease's own name;
//   - its entry is the tenant-file entry whose object renders to that
//     name, when exactly one does (else the one the type's Entry template
//     derives, and resolveEntry asks as usual);
//   - its live objects are every workload of the type the HelmRelease
//     renders, the one named name (or else named like the HelmRelease)
//     first.
//
// Only a named lookup falls back like this: Classify, and so ResolveAll's
// --all, still selects by the catalog's selectors alone.
func managedInstances(opts Options, name, asType string) ([]Instance, error) {
	charts := helmReleaseCharts(opts.Objects)
	var releases []string
	seen := map[string]bool{}
	for _, obj := range opts.Objects {
		if obj.GetName() != name {
			continue
		}
		hr := ""
		switch {
		case isHelmRelease(obj):
			hr = obj.GetNamespace() + "/" + obj.GetName()
		case len(obj.GetOwnerReferences()) == 0 && managedByTenant(obj, opts.Tenant):
			hr = ManagedHelmRelease(obj)
		}
		if _, ok := charts[hr]; ok && !seen[hr] {
			seen[hr] = true
			releases = append(releases, hr)
		}
	}
	sort.Strings(releases)

	var out []Instance
	for _, hr := range releases {
		_, hrName, _ := strings.Cut(hr, "/")
		for ti := range opts.Catalog.Types {
			t := &opts.Catalog.Types[ti]
			if asType != "" && t.Type != asType {
				continue
			}
			var live []*unstructured.Unstructured
			for _, obj := range opts.Objects {
				if ManagedHelmRelease(obj) == hr && len(obj.GetOwnerReferences()) == 0 && managedByTenant(obj, opts.Tenant) && ManagedMatches(t, obj, charts) {
					live = append(live, obj)
				}
			}
			if len(live) == 0 {
				continue
			}
			sort.SliceStable(live, func(a, b int) bool {
				if ra, rb := primaryRank(live[a], name, hrName), primaryRank(live[b], name, hrName); ra != rb {
					return ra < rb
				}
				return live[a].GetName() < live[b].GetName()
			})
			entry, err := managedEntry(opts, t, live[0], hrName)
			if err != nil {
				return nil, err
			}
			opts.Log.Debugf("%s: no selector matches; already migrated by HelmRelease %s deploying type %q's chart (%d workload(s))",
				name, hr, t.Type, len(live))
			out = append(out, Instance{Type: t, Entry: entry, Live: live, object: hrName})
		}
	}
	return out, nil
}

// primaryRank orders an already-migrated instance's workloads: the one the
// operator named, then the one named like its HelmRelease, then the rest.
func primaryRank(obj *unstructured.Unstructured, name, hrName string) int {
	switch obj.GetName() {
	case name:
		return 0
	case hrName:
		return 1
	default:
		return 2
	}
}

// managedEntry is an already-migrated instance's tenant-file entry: the
// one declared entry whose object name renders to its HelmRelease's name —
// inferred without asking when exactly one does — or else what the type's
// Entry template derives from primary, which resolveEntry checks (and asks
// about) as for any instance.
func managedEntry(opts Options, t *config.ComponentType, primary *unstructured.Unstructured, hrName string) (string, error) {
	if opts.Doc != nil {
		names, err := tenantfile.EntryNames(opts.Doc, t.Component)
		if err != nil {
			return "", err
		}
		var matches []string
		for _, e := range names {
			if object, err := render(t.ObjectTemplate(), newTemplateData(primary, e, "", opts.Tenant)); err == nil && object == hrName {
				matches = append(matches, e)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
	}
	entry, err := render(t.EntryTemplate(), newTemplateData(primary, "", "", opts.Tenant))
	if err != nil {
		return "", fmt.Errorf("type %q: rendering entry for %s %s/%s: %w", t.Type, primary.GetKind(), primary.GetNamespace(), primary.GetName(), err)
	}
	return entry, nil
}

func isHelmRelease(obj *unstructured.Unstructured) bool {
	return obj.GroupVersionKind().Group == "helm.toolkit.fluxcd.io" && obj.GetKind() == "HelmRelease"
}

// managedByTenant is ownedByTenant for a Helm-rendered object, which keeps
// keos's tenant label but not CCT's tenant annotation.
func managedByTenant(obj *unstructured.Unstructured, tenant string) bool {
	return BelongsToTenant(obj, tenant)
}

// BelongsToTenant reports whether obj isn't marked as some other
// tenant's, by CCT's tenant annotation or keos's tenant label — an
// object carrying neither isn't excluded. Exported for
// internal/tenantimport, so `tenant import` and `apps` agree on which
// objects are a tenant's.
func BelongsToTenant(obj metav1.Object, tenant string) bool {
	if tenant == "" {
		return true
	}
	if owner := obj.GetAnnotations()[TenantAnnotation]; owner != "" && owner != tenant {
		return false
	}
	owner := obj.GetLabels()[keosTenantLabel]
	return owner == "" || owner == tenant
}

// ResolveAll resolves every instance Classify finds. With opts.Doc set, an
// instance whose component key the tenant file doesn't declare at all is
// skipped with a warning (that tenant doesn't run it on the GitOps side
// yet — nothing to migrate into); one whose entry isn't declared under an
// otherwise-declared key is asked about. Two instances resolving to the
// same type and entry (e.g. the same agent left live in two namespaces)
// are asked about too, so one component never gets two captures or two
// patches.
//
// An instance whose question goes unanswered (NonInteractive, or the
// operator declining) doesn't abort the rest: it's returned in unresolved,
// so the caller can report every one at once and decide — like any other
// per-app failure — whether the resolvable apps still go ahead. Any other
// error is fatal.
func ResolveAll(opts Options) (apps []config.App, unresolved []error, err error) {
	instances, notes, err := classify(opts.Catalog, opts.Objects, opts.Tenant)
	if err != nil {
		return nil, nil, err
	}
	types := map[string]bool{}
	for _, inst := range instances {
		types[inst.Type.Type] = true
	}
	reportSiblingNotes(opts, notes, types)

	type key struct{ typ, entry string }
	var order []key
	byKey := map[key][]Instance{}
	for _, inst := range instances {
		if opts.Doc != nil {
			names, err := tenantfile.EntryNames(opts.Doc, inst.Type.Component)
			if err != nil {
				return nil, nil, err
			}
			if len(names) == 0 {
				opts.Log.Warningf("skipping %s: %s", inst.Label(), undeclaredReason(opts.Doc, inst.Type.Component))
				continue
			}
		}
		if err := resolveEntry(opts, &inst, false, true); err != nil {
			if errors.Is(err, ErrSkipped) {
				opts.Log.Warningf("skipping %s: you chose to leave it out of this run", inst.Label())
				continue
			}
			if !errors.Is(err, ErrNoAnswer) {
				return nil, nil, err
			}
			unresolved = append(unresolved, err)
			continue
		}
		k := key{inst.Type.Type, inst.Entry}
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], inst)
	}

	var chosen []Instance
	for _, k := range order {
		group := byKey[k]
		pick := group[0]
		if len(group) > 1 {
			labels := make([]string, len(group))
			for i, g := range group {
				labels[i] = g.Label()
			}
			i, skipped, err := chooseOrSkip(opts,
				fmt.Sprintf("more than one live object maps to %s entry %q; which one is the real one?", k.typ, k.entry), labels)
			if skipped {
				opts.Log.Warningf("skipping %s entry %q: you chose to leave it out of this run", k.typ, k.entry)
				continue
			}
			if err != nil {
				err = answerError(fmt.Sprintf("%s entry %q has %d live candidates (%s)", k.typ, k.entry, len(group), strings.Join(labels, "; ")), err)
				if !errors.Is(err, ErrNoAnswer) {
					return nil, nil, err
				}
				unresolved = append(unresolved, err)
				continue
			}
			pick = group[i]
		}
		chosen = append(chosen, pick)
	}

	chosen, conflicts, err := oneTypePerObject(opts, chosen)
	if err != nil {
		return nil, nil, err
	}
	unresolved = append(unresolved, conflicts...)

	apps = make([]config.App, 0, len(chosen))
	for _, inst := range chosen {
		app, err := toApp(inst, opts.Tenant)
		if err != nil {
			return nil, nil, err
		}
		apps = append(apps, app)
	}
	return apps, unresolved, nil
}

// oneTypePerObject keeps one instance per live anchor object: an object
// two catalog types both select (overlapping selectors) would otherwise
// become two apps — backed up twice, and migrated into two tenant-file
// entries, each running its own prepare step. Like Resolve, it asks which
// type is meant; an unanswered question leaves that object out, reported
// in unresolved.
func oneTypePerObject(opts Options, instances []Instance) (kept []Instance, unresolved []error, err error) {
	type objectKey struct{ gvk, namespace, name string }
	keyOf := func(i Instance) objectKey {
		p := i.Primary()
		return objectKey{p.GroupVersionKind().GroupKind().String(), p.GetNamespace(), p.GetName()}
	}
	byObject := map[objectKey][]int{}
	for i, inst := range instances {
		byObject[keyOf(inst)] = append(byObject[keyOf(inst)], i)
	}
	decided := map[objectKey]int{}
	for _, inst := range instances {
		k := keyOf(inst)
		idx := byObject[k]
		if len(idx) == 1 {
			kept = append(kept, inst)
			continue
		}
		if _, done := decided[k]; done {
			continue
		}
		labels := make([]string, len(idx))
		for j, n := range idx {
			labels[j] = instances[n].Label()
		}
		p := inst.Primary()
		choice, skipped, err := chooseOrSkip(opts,
			fmt.Sprintf("%s %s/%s is selected by more than one catalog type; which one is it?", p.GetKind(), p.GetNamespace(), p.GetName()), labels)
		decided[k] = -1
		if skipped {
			opts.Log.Warningf("skipping %s %s/%s: you chose to leave it out of this run", p.GetKind(), p.GetNamespace(), p.GetName())
			continue
		}
		if err != nil {
			err = answerError(fmt.Sprintf("%s %s/%s is selected by %d catalog types (%s)", p.GetKind(), p.GetNamespace(), p.GetName(), len(idx), strings.Join(labels, "; ")), err)
			if !errors.Is(err, ErrNoAnswer) {
				return nil, nil, err
			}
			unresolved = append(unresolved, err)
			continue
		}
		decided[k] = idx[choice]
		kept = append(kept, instances[idx[choice]])
	}
	return kept, unresolved, nil
}

// resolveEntry checks inst.Entry against the tenant file's
// components.<Component> entries, asking the operator to pick one when the
// derived name isn't declared. pinned means the entry came from --as: it
// must exist as given, never be second-guessed with a prompt. skippable
// adds a "skip" choice to the question (ResolveAll's: one instance the
// operator can't map shouldn't stop the others) and returns ErrSkipped when
// it's picked.
func resolveEntry(opts Options, inst *Instance, pinned, skippable bool) error {
	if opts.Doc == nil {
		return nil
	}
	names, err := tenantfile.EntryNames(opts.Doc, inst.Type.Component)
	if err != nil {
		return err
	}
	if contains(names, inst.Entry) {
		return nil
	}
	if pinned {
		return fmt.Errorf("--as entry %q isn't declared under components.%s in the tenant file (declared: %s)",
			inst.Entry, inst.Type.Component, listOrNone(names))
	}
	if len(names) == 0 {
		return fmt.Errorf("%s: %s", inst.Label(), undeclaredReason(opts.Doc, inst.Type.Component))
	}

	question := fmt.Sprintf("%s: entry %q isn't declared under components.%s in the tenant file; which entry does it migrate into?",
		inst.Label(), inst.Entry, inst.Type.Component)
	var i int
	if skippable {
		var skipped bool
		if i, skipped, err = chooseOrSkip(opts, question, names); skipped {
			return ErrSkipped
		}
	} else {
		i, err = prompter(opts).Choose(question, names)
	}
	if err != nil {
		return answerError(fmt.Sprintf("%s: entry %q isn't declared under components.%s (declared: %s)",
			inst.Label(), inst.Entry, inst.Type.Component, strings.Join(names, ", ")), err)
	}
	opts.Log.Debugf("%s: operator mapped it to entry %q", inst.Label(), names[i])
	inst.Entry = names[i]
	return nil
}

// undeclaredReason explains why a component key has no tenant-file
// entries, and what to do about it — distinguishing a key that's merely
// commented out (the common case while enabling components one at a time)
// from one that's absent altogether. Only desired-state commands need the
// entry, so it also points at the ones that don't.
func undeclaredReason(doc *tenantfile.Doc, componentKey string) string {
	const liveOnly = "; `apps backup` and `apps diff --drift` work without it"
	if tenantfile.CommentedOut(doc, componentKey) {
		return fmt.Sprintf("components.%s is commented out in the tenant file; uncomment it (and any component it depends on) to render its desired state"+liveOnly, componentKey)
	}
	return fmt.Sprintf("the tenant file declares no components.%s entries; add one (see `flux stratio tenant import`) to render its desired state"+liveOnly, componentKey)
}

// toApp combines inst's type and resolved entry into a config.App,
// rendering its Object and Kustomization names.
func toApp(inst Instance, tenant string) (config.App, error) {
	t := inst.Type
	primary := inst.Primary()
	object := inst.object
	if object == "" {
		var err error
		object, err = render(t.ObjectTemplate(), newTemplateData(primary, inst.Entry, "", tenant))
		if err != nil {
			return config.App{}, fmt.Errorf("type %q: rendering object: %w", t.Type, err)
		}
	}
	kustomization, err := render(t.KustomizationTemplate(), newTemplateData(primary, inst.Entry, object, tenant))
	if err != nil {
		return config.App{}, fmt.Errorf("type %q: rendering kustomization: %w", t.Type, err)
	}

	live := make([]config.ObjectRef, len(inst.Live))
	for i, obj := range inst.Live {
		live[i] = config.ObjectRef{GVK: obj.GroupVersionKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}
	}
	return config.App{
		ID:            object,
		Name:          t.Name + " " + object,
		Type:          t.Type,
		Rset:          t.Rset,
		Entry:         inst.Entry,
		Kustomization: kustomization,
		Object:        object,
		Anchor:        t.Anchor,
		ChartPath:     t.ChartPath(),
		ValuesRoot:    t.ValuesRoot(),
		Prepare:       t.Prepare,
		Exclude:       t.Exclude,
		Notes:         t.Notes,
		Live:          live,
	}, nil
}

// namedAs reports whether name is one of inst's live objects' names or
// its derived object name — so `apps diff psql-agent` (the legacy name)
// and `apps diff psql-gosec-agent` (the GitOps one) both find the same
// gosec agent.
func (i Instance) namedAs(name, tenant string) bool {
	for _, obj := range i.Live {
		if obj.GetName() == name {
			return true
		}
	}
	if i.object != "" {
		return i.object == name
	}
	object, err := render(i.Type.ObjectTemplate(), newTemplateData(i.Primary(), i.Entry, "", tenant))
	return err == nil && object == name
}

func parseAs(opts Options) (typ, entry string, err error) {
	if opts.As == "" {
		return "", "", nil
	}
	typ, entry, _ = strings.Cut(opts.As, "/")
	if opts.Catalog.Find(typ) == nil {
		return "", "", fmt.Errorf("--as %q: unknown type %q", opts.As, typ)
	}
	return typ, entry, nil
}

func noMatchError(opts Options, name, asType string) error {
	var named, managed []string
	for _, obj := range opts.Objects {
		if obj.GetName() == name {
			named = append(named, fmt.Sprintf("%s %s/%s", obj.GetKind(), obj.GetNamespace(), obj.GetName()))
			if hr := ManagedHelmRelease(obj); hr != "" {
				managed = append(managed, fmt.Sprintf("%s %s/%s is rendered by HelmRelease %s", obj.GetKind(), obj.GetNamespace(), obj.GetName(), hr))
			}
		}
	}
	scope := "any catalog type"
	if asType != "" {
		scope = fmt.Sprintf("type %q", asType)
	}
	if len(named) == 0 {
		return fmt.Errorf("no live component instance named %q matches %s", name, scope)
	}
	if len(managed) > 0 {
		// Already migrated, yet managedInstances found nothing: no
		// chart-mode type's chart.path names the HelmRelease's chart, or
		// it's another tenant's.
		return fmt.Errorf("found %s live, but none is selected by %s — %s (already migrated), but no chart-mode type's chart.path names its chart, or it's labelled %s other than %q",
			strings.Join(named, ", "), scope, strings.Join(managed, "; "), keosTenantLabel, opts.Tenant)
	}
	return fmt.Errorf("found %s live, but none is selected by %s — check its labels/annotations (and ownerReferences) against the catalog's match selectors",
		strings.Join(named, ", "), scope)
}

// answerError explains a question that couldn't be answered. An
// ErrNoAnswer stays detectable through it (errors.Is), so ResolveAll can
// tell "unanswered" apart from a real failure.
func answerError(problem string, err error) error {
	if errors.Is(err, ErrNoAnswer) {
		return &unansweredError{problem: problem}
	}
	return fmt.Errorf("%s: %w", problem, err)
}

type unansweredError struct{ problem string }

func (e *unansweredError) Error() string {
	return e.problem + "; answer interactively (without --yes), or pass --as <type>/<entry>"
}

func (e *unansweredError) Unwrap() error { return ErrNoAnswer }

// skipOption is the extra last choice chooseOrSkip offers.
const skipOption = "none of these: skip it (leave it out of this run)"

// chooseOrSkip asks like Choose, with skipOption added after options.
// skipped is true when the operator picks it; i then means nothing.
func chooseOrSkip(opts Options, question string, options []string) (i int, skipped bool, err error) {
	withSkip := append(append(make([]string, 0, len(options)+1), options...), skipOption)
	i, err = prompter(opts).Choose(question, withSkip)
	if err != nil {
		return 0, false, err
	}
	if i == len(options) {
		return 0, true, nil
	}
	return i, false, nil
}

func prompter(opts Options) Prompter {
	if opts.Prompter == nil {
		return NonInteractive{}
	}
	return opts.Prompter
}

func listOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
