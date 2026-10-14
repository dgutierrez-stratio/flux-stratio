package tenantimport

import (
	"fmt"
	"strings"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
)

// fieldAction is what to do with one config field the schema declares, for
// one entry that lacks it.
type fieldAction int

const (
	// fieldSkip: nothing to write and nothing to say — the field is not
	// read for this entry's storage type, or it is an optional sub-config.
	fieldSkip fieldAction = iota
	// fieldDefault: write the template's default.
	fieldDefault
	// fieldWarn: the field has no usable default, or is required: the
	// operator sets it by hand.
	fieldWarn
	// fieldScaffold: a required dict whose keys the template reads (genai's
	// llmModels): write it with those keys, empty, and warn to fill them in.
	fieldScaffold
)

// fieldPlan decides what to do with field for an entry of storageType.
// required holds the component's catalog-declared required fields.
func fieldPlan(field catalog.ExtraConfigField, storageType string, required map[string]bool) fieldAction {
	switch {
	case !field.AppliesTo(storageType):
		return fieldSkip
	case required[field.Key] && field.Container == "dict" && len(field.Leaves) > 0:
		return fieldScaffold
	case required[field.Key]:
		return fieldWarn
	case field.HasDefault:
		return fieldDefault
	case field.Container != "":
		return fieldSkip // `| default dict` / `| default list`: optional
	}
	return fieldWarn
}

// scaffoldValue is the mapping a fieldScaffold field is written as: each key
// the template reads, with the template's default for it, else "".
func scaffoldValue(field catalog.ExtraConfigField) map[string]any {
	out := make(map[string]any, len(field.Leaves))
	for _, leaf := range field.Leaves {
		if leaf.HasDefault {
			out[leaf.Key] = defaultValue(leaf)
		} else {
			out[leaf.Key] = ""
		}
	}
	return out
}

// leafKeys lists a field's leaf keys, in template order.
func leafKeys(field catalog.ExtraConfigField) []string {
	keys := make([]string, 0, len(field.Leaves))
	for _, leaf := range field.Leaves {
		keys = append(keys, leaf.Key)
	}
	return keys
}

// scaffoldWarning is the warning for a fieldScaffold field just written.
func scaffoldWarning(label string, field catalog.ExtraConfigField) string {
	return fmt.Sprintf("%s: config %q is required: added with its keys %s empty; set them by hand",
		label, field.Key, strings.Join(leafKeys(field), ", "))
}

// requiredConfig reads every type's requiredConfig from cfg, keyed by the
// tenant component key. A type that predates the field (a catalog.yaml
// seeded by an older release) inherits the seed's list for the same type
// id, and a nil cfg uses the seed's wholesale: without them a field the
// template defaults to an empty container but the chart cannot run without
// (genai's llmModels) would stop being reported.
func requiredConfig(cfg *config.Catalog) map[string]map[string]bool {
	seed := config.SeedCatalog()
	if cfg == nil {
		cfg = &seed
	}
	out := map[string]map[string]bool{}
	for _, t := range cfg.Types {
		fields := t.RequiredConfig
		if len(fields) == 0 {
			if s := seed.Find(t.Type); s != nil {
				fields = s.RequiredConfig
			}
		}
		for _, f := range fields {
			if out[t.Component] == nil {
				out[t.Component] = map[string]bool{}
			}
			out[t.Component][f] = true
		}
	}
	return out
}
