package tenantimport

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/components"
)

// A gated dependency is one whose `enabled` the keos-use-cases ResourceSets
// read (catalog.Dependency.Gated): the tenant file's value is the only thing
// that wires it in, and it defaults to off. The catalog learns which they are
// from the templates, so this file knows no component and no dependency by
// name: a new component, or a new `enabled` gate on an existing one, is
// covered the day it lands in keos-use-cases.
//
// The evidence is presence. A dependency that runs on the cluster next to an
// app that can use it almost always is used, so the import proposes
// `enabled: true` and asks (Decider); what the legacy workload itself says is
// not read. A dependency that does not run on the cluster is left off and said
// so. Nothing is ever decided silently, and an answer already in the tenant
// file is never asked again, `false` included.

// DepState is what became of a proposal to enable one gated dependency.
type DepState int

const (
	// DepPending means the dependency runs on the cluster but nobody answered
	// (nothing to ask, or no way to ask). Nothing is written.
	DepPending DepState = iota + 1
	// DepOn means `enabled: true` is written.
	DepOn
	// DepOff means the operator declined: `enabled: false` is written so a
	// later run does not ask again.
	DepOff
)

// DepGate is a proposal: a gated dependency of an entry that runs on the
// cluster, resolved to the entry Name, with what turning it on Adds.
type DepGate struct {
	Name string
	Adds []string
}

// Decider answers each proposal. The zero value asks nobody: every proposal
// stays pending and is reported.
type Decider struct {
	// Prompter asks the operator. A nil one, or one that cannot ask
	// (components.NonInteractive, or input that ran out), leaves the proposal
	// pending: silence never means yes.
	Prompter components.Prompter
	// AssumeYes accepts every proposal without asking, reporting each.
	AssumeYes bool
}

// markGates records on every live entry what its gated dependencies come to:
// DepGate for each one that runs on the cluster, DepAbsent for each one that
// does not. running is the set of entries the cluster itself produced: a
// skeleton placeholder, or an entry the file has and the cluster lacks, never
// counts as running. It runs after enrich, which resolves the dependency names.
func markGates(cat *catalog.Catalog, comps Components, running map[*Entry]bool) {
	for key, entries := range comps {
		schema, ok := cat.Schemas[key]
		if !ok {
			continue
		}
		for _, e := range entries {
			if !running[e] {
				continue
			}
			for _, dep := range schema.Dependencies {
				if !dep.Gated || (len(dep.StorageTypes) > 0 && !containsStr(dep.StorageTypes, e.Type)) {
					continue
				}
				name := e.Deps[dep.Key]
				if name == "" {
					continue // unresolved: enrich already said so
				}
				if target := comps.find(dep.Key, name); target != nil && running[target] {
					if e.DepGate == nil {
						e.DepGate = map[string]DepGate{}
					}
					e.DepGate[dep.Key] = DepGate{Name: name, Adds: dep.Adds}
					continue
				}
				if e.DepAbsent == nil {
					e.DepAbsent = map[string]bool{}
				}
				e.DepAbsent[dep.Key] = true
			}
		}
	}
}

const (
	absentFormat  = "%s: dependency %q is not running on the cluster, so `enabled` is left off and the app is not wired to it; set it to true by hand if the app should use it"
	pendingFormat = "%s: dependency %q (%s) runs on the cluster; enabling it is not decided here (no way to ask, or --dry-run): run again in a terminal to be asked, or with --yes to enable it"
	assumedFormat = "%s: dependency %q enabled without asking (--yes): %s runs on the cluster and turning it on %s; review it"
	genericAdds   = "also changes what the app waits for (dependsOn) and what its prerequisites set up"
)

// decide settles the proposal for dependency key of e: asks (or assumes yes),
// remembers the answer on e so it is asked once per run, and returns it. warn
// reports what the operator should look at. The caller has checked that e
// carries a proposal for key and that the tenant file has no `enabled` for it.
func (d Decider) decide(label string, e *Entry, key string, warn func(string, ...any)) (DepState, error) {
	if st, done := e.DepState[key]; done {
		return st, nil
	}
	g := e.DepGate[key]
	state := DepPending
	switch {
	case d.AssumeYes:
		state = DepOn
		warn(assumedFormat, label, key, g.Name, addsText(g.Adds))
	case d.Prompter != nil:
		question := fmt.Sprintf("%s: dependency %q (%s) runs on the cluster. Turning it on %s.\nEnable it?", label, key, g.Name, addsText(g.Adds))
		idx, err := d.Prompter.Choose(question, []string{"enable it (enabled: true)", "leave it off (enabled: false)"})
		switch {
		case err == nil && idx == 0:
			state = DepOn
		case err == nil:
			state = DepOff
		case errors.Is(err, components.ErrNoAnswer):
		default:
			return 0, fmt.Errorf("asking about dependency %q of %s: %w", key, label, err)
		}
	}
	if state == DepPending {
		warn(pendingFormat, label, key, g.Name)
	}
	if e.DepState == nil {
		e.DepState = map[string]DepState{}
	}
	e.DepState[key] = state
	return state, nil
}

// decideEntry settles every gated dependency of e that the tenant file does
// not already answer (a new entry has no file to answer): it asks about each
// that runs on the cluster and warns about each that does not. Dependency
// keys are visited in sorted order so the questions come in a stable order.
func (d Decider) decideEntry(label string, e *Entry, warn func(string, ...any)) error {
	for _, key := range sortedKeys(e.DepGate) {
		if _, err := d.decide(label, e, key, warn); err != nil {
			return err
		}
	}
	for _, key := range sortedKeys(e.DepAbsent) {
		warn(absentFormat, label, key)
	}
	return nil
}

// decideAll is decideEntry for every entry of comps, in key and entry order:
// the create-mode run, where the file has no answers yet.
func (d Decider) decideAll(comps Components, warn func(string, ...any)) error {
	keys := make([]string, 0, len(comps))
	for key := range comps {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, e := range comps[key] {
			if err := d.decideEntry(fmt.Sprintf("components.%s[%s]", key, e.Name), e, warn); err != nil {
				return err
			}
		}
	}
	return nil
}

// addsText says what turning a dependency on changes, as the question puts it.
func addsText(adds []string) string {
	if len(adds) == 0 {
		return genericAdds
	}
	return strings.Join(adds, "; ")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
