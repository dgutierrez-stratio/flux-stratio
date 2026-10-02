package tenantimport

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Render builds the ResourceSetInputProvider YAML for tenantName from the
// enriched components, with the label current keos-use-cases templates
// actually select on (keos.stratio.com/resourceset-type: tenant-config —
// the Python client emitted flux.stratio.com/tenant: "true" instead,
// making every tenant it generated invisible to every ResourceSet; see
// design decision 8 in the project plan).
//
// Field order within each entry is alphabetical, not the Python client's
// "type, extras, dependencies, uiEnabled-last" convention: gopkg.in/yaml.v3
// sorts map[string]any keys for determinism regardless of insertion
// order, so this plugin doesn't fight that to reproduce it. That costs
// nothing functional — field order is cosmetic, never meaningful to a
// YAML consumer.
func Render(tenantName, size string, components Components) ([]byte, error) {
	orderedComponents := map[string]any{}
	for key, entries := range components {
		orderedComponents[key] = renderEntries(entries)
	}

	doc := map[string]any{
		"apiVersion": "fluxcd.controlplane.io/v1",
		"kind":       "ResourceSetInputProvider",
		"metadata": map[string]any{
			"name":      tenantName,
			"namespace": "flux-system",
			"labels": map[string]any{
				"keos.stratio.com/resourceset-type": "tenant-config",
			},
		},
		"spec": map[string]any{
			"type": "Static",
			"defaultValues": map[string]any{
				"tenantName":           tenantName,
				"size":                 size,
				"internalS3BucketName": "",
				"sopsSecretProvided":   false,
				"userEmail":            "",
				"userId":               "",
				"userName":             "",
				"components":           orderedComponents,
			},
		},
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encoding tenant file: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("closing tenant file encoder: %w", err)
	}
	return buf.Bytes(), nil
}

func renderEntries(entries []*Entry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		item := map[string]any{"name": e.Name}
		config := map[string]any{}
		if e.Type != "" {
			config["type"] = e.Type
		}
		for k, v := range e.ExtraConfig {
			config[k] = v
		}
		if len(e.Deps) > 0 {
			deps := map[string]any{}
			for k, v := range e.Deps {
				deps[k] = map[string]any{"name": v}
			}
			config["dependencies"] = deps
		}
		if len(config) > 0 {
			item["config"] = config
		}
		out = append(out, item)
	}
	return out
}
