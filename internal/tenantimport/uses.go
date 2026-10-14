package tenantimport

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Stratio/flux-stratio/internal/config"
)

// usesRules reads every type's chart.uses from cfg, keyed by the tenant
// component key. A type that predates the field — a catalog.yaml seeded by
// an older release, which nobody re-seeds on upgrade — inherits the seed's
// rules for the same type id: without them every dependency would stay at
// the ResourceSets' `enabled` default (false) with nothing said, which is the
// failure this exists to prevent.
func usesRules(cfg *config.Catalog) map[string][]config.Use {
	if cfg == nil {
		return nil
	}
	seed := config.SeedCatalog()
	out := map[string][]config.Use{}
	for _, t := range cfg.Types {
		uses := t.Uses()
		if len(uses) == 0 {
			if s := seed.Find(t.Type); s != nil {
				uses = s.Uses()
			}
		}
		if len(uses) > 0 {
			out[t.Component] = append(out[t.Component], uses...)
		}
	}
	return out
}

// scanUses decides, for every discovered entry of a type declaring
// chart.uses, which of its dependencies it uses. The evidence is the
// entry's live HelmRelease values: they carry the legacy configuration the
// migration patch keeps (rocket's genAI.genaiEnabled, a crossdata host),
// while the tenant file's `enabled` is the only thing the ResourceSets read.
// An entry with no HelmRelease (not migrated yet) is undecided: nothing is
// written and the caller warns, rather than guessing either way.
func scanUses(opts Options, components Components) {
	for key, rules := range usesRules(opts.Config) {
		for _, entry := range components[key] {
			for _, rule := range rules {
				if entry.HelmReleaseSpec == nil {
					if entry.DepUnknown == nil {
						entry.DepUnknown = map[string]bool{}
					}
					entry.DepUnknown[rule.Dependency] = true
					continue
				}
				path := useEvidence(entry.HelmReleaseSpec, rule.Values)
				if path == "" {
					continue
				}
				if entry.DepEnabled == nil {
					entry.DepEnabled = map[string]string{}
				}
				entry.DepEnabled[rule.Dependency] = path
				opts.note("%s: dependency %q is in use: spec.values.%s is set in its HelmRelease", entry.Name, rule.Dependency, path)
			}
		}
	}
}

// assumeLiveUses settles the dependencies scanUses left undecided for want of
// a HelmRelease: one whose component runs on the cluster is taken as used
// (`enabled: true`), since an app that has the dependency available and was
// installed together with it almost always uses it, and the ResourceSets
// gate on `enabled` — left off, the dependent would not even wait for it.
// It is a weaker ground than a HelmRelease value, and said so. A dependency
// with no live component stays undecided and is warned about. It runs before
// the skeleton expansion, so a placeholder never counts as running.
func assumeLiveUses(opts Options, components Components) {
	for key, rules := range usesRules(opts.Config) {
		for _, entry := range components[key] {
			for _, rule := range rules {
				if !entry.DepUnknown[rule.Dependency] || len(components[rule.Dependency]) == 0 {
					continue
				}
				delete(entry.DepUnknown, rule.Dependency)
				if entry.DepAssumed == nil {
					entry.DepAssumed = map[string]bool{}
				}
				entry.DepAssumed[rule.Dependency] = true
				opts.note("%s: dependency %q is assumed in use: it runs on the cluster and there is no HelmRelease to say otherwise", entry.Name, rule.Dependency)
			}
		}
	}
}

// useEvidence returns the first of paths (dotted, under spec.values) that is
// set to something other than null, false, zero or a string spelling of off
// ("", "false", "0", "no", "off", any case), or "" when none is.
func useEvidence(spec map[string]any, paths []string) string {
	for _, p := range paths {
		v, found, err := unstructured.NestedFieldNoCopy(spec, append([]string{"values"}, strings.Split(p, ".")...)...)
		if err == nil && found && truthy(v) {
			return p
		}
	}
	return ""
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		// Legacy values are often strings: any spelling of off is not evidence.
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "", "false", "0", "no", "off":
			return false
		}
		return true
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case map[string]any:
		return len(x) > 0
	case []any:
		return len(x) > 0
	}
	return true
}

const undecidedUseFormat = "%s: cannot tell whether it uses dependency %q (no live HelmRelease to read its configuration from); `enabled` left off, set it to true by hand if it does"

// undecidedUses lists, sorted, the dependencies of e that have a rule but no
// HelmRelease to apply it to, and whose name is resolved (an unresolved one
// is already warned about).
func undecidedUses(e *Entry) []string {
	var out []string
	for dep := range e.DepUnknown {
		if e.Deps[dep] != "" {
			out = append(out, dep)
		}
	}
	sort.Strings(out)
	return out
}
