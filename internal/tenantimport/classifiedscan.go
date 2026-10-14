package tenantimport

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/components"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
)

// scanClassified is the primary app-component discovery pass: the live
// objects of the tenant's namespaces, classified by the same component
// catalog `apps` uses (internal/components), one entry per instance. A
// legacy (CCT) app has no HelmRelease and its workload is rarely named like
// its chart, so what identifies it is what CCT stamped on it
// (application_service/application_model), and what it will be called in the
// tenant file is the catalog's `entry` template: genai-api -> genai,
// connectors-management -> connectors, a datamarket agent ->
// governance-datamarket-agent. A type that already has an entry of that name
// (found as a custom resource, or twice by one instance's workloads) adds
// nothing.
//
// Only the kinds the catalog selects are listed. One that is not installed
// (an operator's CRD this cluster lacks) is skipped; any other failure to
// list it is an error, since treating its instances as absent would have
// their dependents wired to placeholders.
func scanClassified(ctx context.Context, c client.Client, cfg *config.Catalog, cat *catalog.Catalog, tenantName string, found Components, warn func(string, ...any)) error {
	var objs []*unstructured.Unstructured
	for _, gvk := range cfg.Kinds() {
		list, err := kubeclient.ListUnstructured(ctx, c, gvk, "")
		if kubeclient.IsNoMatch(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("listing %s: %w", gvk.Kind, err)
		}
		for i := range list.Items {
			if tenantObject(&list.Items[i], tenantName) {
				objs = append(objs, &list.Items[i])
			}
		}
	}

	instances, err := components.Classify(cfg, objs, tenantName)
	if err != nil {
		return err
	}
	for _, inst := range instances {
		key := inst.Type.Component
		if _, known := cat.Schemas[key]; !known {
			warn("%s: the catalog's type %q names component %q, which no ResourceSet of keos-use-cases declares; not imported", inst.Label(), inst.Type.Type, key)
			continue
		}
		if nested(inst, cat, tenantName) {
			continue
		}
		entry := found.find(key, inst.Entry)
		isNew := entry == nil
		if isNew {
			entry = &Entry{Name: inst.Entry, Deps: map[string]string{}}
		}
		if reason := applyEnvConfigs(inst, entry, cat.Schemas[key]); reason != "" {
			warn("%s: %s; not imported — declare it by hand if it should be a %s entry", inst.Label(), reason, key)
			continue
		}
		if isNew {
			found[key] = append(found[key], entry)
		}
	}
	return nil
}

// envConfigs is the type's configFromEnv rules. A type that predates the field —
// a catalog.yaml seeded by an older release, which nobody re-seeds on upgrade —
// inherits the seed's for the same type id: without them an agent would be
// imported without the storage type that picks its overlay.
func envConfigs(t *config.ComponentType) []config.EnvConfig {
	if len(t.ConfigFromEnv) > 0 {
		return t.ConfigFromEnv
	}
	if s := config.SeedCatalog().Find(t.Type); s != nil {
		return s.ConfigFromEnv
	}
	return nil
}

// applyEnvConfigs fills entry from the env of inst's primary workload as the
// type's configFromEnv says, never over a value already known. It returns why
// the instance has no GitOps equivalent when an env var holds a value the rule
// does not map (a legacy POSTGRESQL agent where only hdfs and s3 overlays
// exist), "" otherwise.
func applyEnvConfigs(inst components.Instance, entry *Entry, schema catalog.Schema) string {
	for _, rule := range envConfigs(inst.Type) {
		value, set := envValue(inst.Primary(), rule.Env)
		if !set {
			continue
		}
		derived, known := rule.Apply(value)
		if !known {
			return fmt.Sprintf("%s is %q, which the GitOps component has no %s for (known: %s)", rule.Env, value, rule.Key, knownValues(rule))
		}
		if derived == "" {
			continue
		}
		switch field := rule.Field(); {
		case rule.DependencyKey() != "":
			if dep := rule.DependencyKey(); entry.Deps[dep] == "" {
				entry.Deps[dep] = derived
			}
		case field != "":
			if !schemaReadsField(schema, field) {
				continue // the ResourceSet never reads it: writing it would do nothing
			}
			if entry.FromEnv == nil {
				entry.FromEnv = map[string]string{}
			}
			if _, set := entry.FromEnv[field]; !set {
				entry.FromEnv[field] = derived
				if entry.ExtraConfig == nil {
					entry.ExtraConfig = map[string]any{}
				}
				entry.ExtraConfig[field] = derived
			}
		case entry.Type == "":
			entry.Type = derived
		}
	}
	return ""
}

// schemaReadsField reports whether the component's ResourceSet reads config
// field name (a field the catalog extracted from its template).
func schemaReadsField(schema catalog.Schema, name string) bool {
	for _, f := range schema.ExtraConfig {
		if f.Key == name {
			return true
		}
	}
	return false
}

func knownValues(rule config.EnvConfig) string {
	var out []string
	for live := range rule.Values {
		out = append(out, live)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// envValue is the literal value of the env var name on any container of obj
// (a workload's pod template). One set from a Secret or ConfigMap reference is
// not read: the scan must never touch secret values.
func envValue(obj *unstructured.Unstructured, name string) (string, bool) {
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		container, _ := c.(map[string]any)
		envs, _ := container["env"].([]any)
		for _, e := range envs {
			env, _ := e.(map[string]any)
			if env["name"] != name {
				continue
			}
			if v, ok := env["value"].(string); ok {
				return v, true
			}
		}
	}
	return "", false
}

// nested reports whether inst is not a component of its own but a part of
// another entry's config (a postgres' gosec agent, patched under
// config.agent): its entry is its parent's, and importing it would invent a
// parent the cluster may not have.
func nested(inst components.Instance, cat *catalog.Catalog, tenantName string) bool {
	app, err := inst.App(tenantName)
	if err != nil {
		return false
	}
	anchor, err := cat.ResolveAnchor(app.Kustomization)
	return err == nil && anchor.Kind == catalog.AnchorNested
}
