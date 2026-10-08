package tenantimport

import (
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
)

// fieldPlan decides what to do with field for an entry of storageType.
// required holds the component's catalog-declared required fields.
func fieldPlan(field catalog.ExtraConfigField, storageType string, required map[string]bool) fieldAction {
	switch {
	case !field.AppliesTo(storageType):
		return fieldSkip
	case required[field.Key]:
		return fieldWarn
	case field.HasDefault:
		return fieldDefault
	case field.Container != "":
		return fieldSkip // `| default dict` / `| default list`: optional
	}
	return fieldWarn
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
