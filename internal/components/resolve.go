package components

import (
	"errors"
	"fmt"
	"strings"

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
	instances, err := Classify(opts.Catalog, opts.Objects, opts.Tenant)
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

	if asEntry != "" {
		chosen.Entry = asEntry
	}
	if err := resolveEntry(opts, &chosen, asEntry != ""); err != nil {
		return config.App{}, err
	}
	return toApp(chosen, opts.Tenant)
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
	instances, err := Classify(opts.Catalog, opts.Objects, opts.Tenant)
	if err != nil {
		return nil, nil, err
	}

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
		if err := resolveEntry(opts, &inst, false); err != nil {
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

	apps = make([]config.App, 0, len(order))
	for _, k := range order {
		group := byKey[k]
		chosen := group[0]
		if len(group) > 1 {
			labels := make([]string, len(group))
			for i, g := range group {
				labels[i] = g.Label()
			}
			i, err := prompter(opts).Choose(
				fmt.Sprintf("more than one live object maps to %s entry %q; which one is the real one?", k.typ, k.entry), labels)
			if err != nil {
				err = answerError(fmt.Sprintf("%s entry %q has %d live candidates (%s)", k.typ, k.entry, len(group), strings.Join(labels, "; ")), err)
				if !errors.Is(err, ErrNoAnswer) {
					return nil, nil, err
				}
				unresolved = append(unresolved, err)
				continue
			}
			chosen = group[i]
		}
		app, err := toApp(chosen, opts.Tenant)
		if err != nil {
			return nil, nil, err
		}
		apps = append(apps, app)
	}
	return apps, unresolved, nil
}

// resolveEntry checks inst.Entry against the tenant file's
// components.<Component> entries, asking the operator to pick one when the
// derived name isn't declared. pinned means the entry came from --as: it
// must exist as given, never be second-guessed with a prompt.
func resolveEntry(opts Options, inst *Instance, pinned bool) error {
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

	i, err := prompter(opts).Choose(
		fmt.Sprintf("%s: entry %q isn't declared under components.%s in the tenant file; which entry does it migrate into?",
			inst.Label(), inst.Entry, inst.Type.Component), names)
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
	object, err := render(t.ObjectTemplate(), newTemplateData(primary, inst.Entry, "", tenant))
	if err != nil {
		return config.App{}, fmt.Errorf("type %q: rendering object: %w", t.Type, err)
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
	var named []string
	for _, obj := range opts.Objects {
		if obj.GetName() == name {
			named = append(named, fmt.Sprintf("%s %s/%s", obj.GetKind(), obj.GetNamespace(), obj.GetName()))
		}
	}
	scope := "any catalog type"
	if asType != "" {
		scope = fmt.Sprintf("type %q", asType)
	}
	if len(named) == 0 {
		return fmt.Errorf("no live component instance named %q matches %s", name, scope)
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
