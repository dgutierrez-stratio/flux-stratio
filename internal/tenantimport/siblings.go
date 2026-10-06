package tenantimport

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/config"
)

// Derived is the config values read off the live cluster rather than a
// template default, keyed by component key and then by config field:
// {"virtualizer": {"uiEnabled": true}}. A value here wins over the schema
// default, and Complete uses it for every active entry of the key.
type Derived map[string]map[string]any

// Live is what the cluster says about the tenant beyond its components.
type Live struct {
	// Values are the config fields derived from it.
	Values Derived
	// Siblings lists, per component key, the sibling workloads running.
	Siblings map[string][]string
}

// scanEnabledBy derives, for every chart type declaring Chart.EnabledBy,
// whether any of its siblings runs in a tenant namespace — as a legacy CCT
// Deployment or already Helm-managed (both carry the sibling's workload
// name; only the CCT annotations differ, and they are gone after a
// migration). That is the value of the config key that switches the
// siblings on: left at its template default (false) next to live
// siblings, the next release would remove them.
//
// A nil catalog derives nothing: the config key then falls back to its
// template default, and the caller warns.
func scanEnabledBy(ctx context.Context, c client.Client, cfg *config.Catalog, tenantName string) (Live, error) {
	if cfg == nil {
		return Live{}, nil
	}
	switches := enabledBySwitches(cfg)
	if len(switches) == 0 {
		return Live{}, nil
	}

	var deployments appsv1.DeploymentList
	if err := c.List(ctx, &deployments); err != nil {
		return Live{}, err
	}
	live := map[string]bool{} // workload name -> running in a tenant namespace
	for i := range deployments.Items {
		d := &deployments.Items[i]
		if tenantObject(d, tenantName) && len(d.OwnerReferences) == 0 {
			live[d.Name] = true
		}
	}

	out := Live{Values: Derived{}, Siblings: map[string][]string{}}
	for _, sw := range switches {
		for _, name := range sw.siblings {
			if live[name] {
				out.Siblings[sw.component] = append(out.Siblings[sw.component], name)
			}
		}
		if out.Values[sw.component] == nil {
			out.Values[sw.component] = map[string]any{}
		}
		out.Values[sw.component][sw.field] = len(out.Siblings[sw.component]) > 0
	}
	return out, nil
}

// enabledBySwitch is one component key's "config key that switches its
// sibling workloads on" and those siblings' workload names.
type enabledBySwitch struct {
	component, field string
	siblings         []string
}

// enabledBySwitches reads every type's chart.enabledBy from cfg. A type
// that predates the field — a catalog.yaml seeded by an older release, which
// nobody re-seeds on upgrade — inherits the seed's value for the same type
// id: without it the key would fall back to its template default (false)
// with nothing said, which is the failure this exists to prevent.
func enabledBySwitches(cfg *config.Catalog) []enabledBySwitch {
	seed := config.SeedCatalog()
	var out []enabledBySwitch
	for _, t := range cfg.Types {
		field, names := t.EnabledBy(), t.SiblingNames()
		if field == "" {
			s := seed.Find(t.Type)
			if s == nil || s.EnabledBy() == "" {
				continue
			}
			field = s.EnabledBy()
			if len(names) == 0 {
				names = s.SiblingNames()
			}
		}
		out = append(out, enabledBySwitch{component: t.Component, field: field, siblings: names})
	}
	return out
}
