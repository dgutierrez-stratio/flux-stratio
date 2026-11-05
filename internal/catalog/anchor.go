package catalog

import (
	"fmt"
	"strings"
)

// ResolvedAnchor is the answer to "where do this Kustomization's patches
// come from in the tenant YAML".
type ResolvedAnchor struct {
	Kind AnchorKind
	// Field is the nested dotted path (set only when Kind ==
	// AnchorNested), e.g. "config.agent".
	Field string
	// Suffix is the Kustomization-name suffix that was matched — "" for
	// the default suffix every component declares, "-gosec-agent" etc.
	// otherwise. Stripping it (and the "apps-" prefix) from a
	// Kustomization name recovers its owning component entry's own name
	// in the tenant YAML, which is not always the same as the app's
	// Object (a nested anchor's Object is the sub-resource's own name,
	// e.g. a gosec agent's HelmRelease — its owning entry is named
	// something else entirely).
	Suffix string
}

// buildAnchorIndex collects every distinct Kustomization name-suffix
// declared anywhere in schemas (e.g. "", "-gosec-agent",
// "-postrequisites") into a single suffix -> ResolvedAnchor index,
// checked for consistency: if two different component schemas declare the
// same suffix with different Kind/Field, that is a template ambiguity
// this package cannot safely resolve, and Load fails rather than guessing.
func buildAnchorIndex(schemas map[string]Schema) (map[string]ResolvedAnchor, error) {
	index := map[string]ResolvedAnchor{}
	for _, s := range schemas {
		for _, k := range s.Kustomizations {
			ra := ResolvedAnchor{Kind: k.Kind, Field: k.Field, Suffix: k.Suffix}
			if existing, ok := index[k.Suffix]; ok && existing != ra {
				return nil, fmt.Errorf(
					"inconsistent anchor for kustomization suffix %q: %s vs %s (component %q)",
					k.Suffix, existing.Kind, ra.Kind, s.Key,
				)
			}
			index[k.Suffix] = ra
		}
	}
	return index, nil
}

// ResolveAnchor determines where a rendered Kustomization's patches come
// from, by longest-suffix match against every suffix declared anywhere in
// the catalog: "apps-psql-gosec-agent" matches the "-gosec-agent" suffix
// (AnchorNested, field "config.agent") in preference to the "" suffix
// every component also declares (AnchorDefault) — the empty suffix always
// matches trivially, so it is only ever the fallback, never a competitor.
//
// It deliberately never needs to know which component key owns the
// instance: for both AnchorDefault and AnchorNested, the matching
// components.<key>[name=X] entry is found by scanning every component key
// (design decision 2), and every schema that declares a given suffix
// agrees on its Kind/Field (checked once, at Load).
func (c *Catalog) ResolveAnchor(kustomizationName string) (ResolvedAnchor, error) {
	rest, ok := strings.CutPrefix(kustomizationName, "apps-")
	if !ok {
		return ResolvedAnchor{}, fmt.Errorf("kustomization name %q does not start with %q", kustomizationName, "apps-")
	}

	best, bestLen := "", -1
	for suffix := range c.anchorsBySuffix {
		if suffix == "" {
			continue // the empty suffix is the fallback, handled below
		}
		if strings.HasSuffix(rest, suffix) && len(suffix) > bestLen {
			best, bestLen = suffix, len(suffix)
		}
	}

	var anchor ResolvedAnchor
	if bestLen >= 0 {
		anchor = c.anchorsBySuffix[best]
	} else if a, ok := c.anchorsBySuffix[""]; ok {
		anchor = a
	} else {
		return ResolvedAnchor{}, fmt.Errorf("kustomization %q does not match any known component pattern", kustomizationName)
	}

	if anchor.Kind == AnchorUnknown {
		return anchor, fmt.Errorf("kustomization %q has a patches: shape this catalog does not recognize; supply an explicit anchor in the config", kustomizationName)
	}
	return anchor, nil
}
