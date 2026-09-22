package tenantfile

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Stratio/flux-stratio/internal/catalog"
)

// FindComponentEntry scans every key under spec.defaultValues.components
// (each a sequence of entries) for the first entry whose own "name" field
// equals objectName — matching how the tenant YAML's components structure
// works: an app's entry can live under any component key, not just one
// this plugin knows in advance (design decision 2 in the project plan).
func FindComponentEntry(d *Doc, objectName string) (*yaml.Node, error) {
	components, err := d.components()
	if err != nil {
		return nil, err
	}
	entry := findComponentEntry(components, objectName)
	if entry == nil {
		return nil, fmt.Errorf("no components.<key> entry named %q found in the tenant file", objectName)
	}
	return entry, nil
}

func (d *Doc) components() (*yaml.Node, error) {
	root := documentRoot(d.root)
	spec := mapGet(root, "spec")
	if spec == nil {
		return nil, fmt.Errorf("tenant file has no top-level spec")
	}
	defaultValues := mapGet(spec, "defaultValues")
	if defaultValues == nil {
		return nil, fmt.Errorf("tenant file has no spec.defaultValues")
	}
	components := mapGet(defaultValues, "components")
	if components == nil {
		return nil, fmt.Errorf("tenant file has no spec.defaultValues.components")
	}
	return components, nil
}

func findComponentEntry(components *yaml.Node, objectName string) *yaml.Node {
	if components.Kind != yaml.MappingNode {
		return nil
	}
	for i := 1; i < len(components.Content); i += 2 {
		seq := components.Content[i]
		if seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, entry := range seq.Content {
			if nameNode := mapGet(entry, "name"); nameNode != nil && nameNode.Value == objectName {
				return entry
			}
		}
	}
	return nil
}

// DependencyNames returns the instance names entry.config.dependencies
// references, keyed by dependency key — e.g. {"postgres": "psql",
// "pgbouncer": "pool-psql"} — read directly from the tenant file itself.
// Used by internal/appmigrate to build a dependency graph for `apps
// migrate --all`'s topological ordering, without needing to know
// entry's own component key.
func DependencyNames(entry *yaml.Node) map[string]string {
	deps := mapGet(mapGet(entry, "config"), "dependencies")
	if deps == nil || deps.Kind != yaml.MappingNode {
		return nil
	}
	out := map[string]string{}
	for i := 0; i+1 < len(deps.Content); i += 2 {
		depKey := deps.Content[i].Value
		if name := mapGet(deps.Content[i+1], "name"); name != nil && name.Value != "" {
			out[depKey] = name.Value
		}
	}
	return out
}

// ResolveAnchorNode navigates from a component entry to the node whose
// "patches" key this app's Kustomization actually reads, per the
// catalog's Anchor classification: the entry itself for AnchorDefault, or
// a nested mapping (created if absent, e.g. a bare component entry with no
// config.agent block yet) for AnchorNested — "config.agent" for a
// postgres/opensearch gosec agent (design decision 2).
func ResolveAnchorNode(entry *yaml.Node, anchor catalog.ResolvedAnchor) (*yaml.Node, error) {
	switch anchor.Kind {
	case catalog.AnchorDefault:
		return entry, nil
	case catalog.AnchorNested:
		node := entry
		for _, part := range strings.Split(anchor.Field, ".") {
			next := mapGet(node, part)
			if next == nil {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				mapSet(node, part, next)
			}
			node = next
		}
		return node, nil
	default:
		return nil, fmt.Errorf("anchor kind %s is not patchable", anchor.Kind)
	}
}
