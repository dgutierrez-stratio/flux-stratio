package tenantfile

import (
	"gopkg.in/yaml.v3"
)

// LegacyAgentPatches counts the patches in entry's own top-level "patches"
// that target the HelmRelease agentObject — where the legacy client wrote
// a gosec agent's patch. Nothing reads them there (the parent's
// Kustomization renders no agent HelmRelease; the agent's patch belongs in
// config.agent.patches), so they are inert but misleading. Read-only: it
// never edits entry.
func LegacyAgentPatches(entry *yaml.Node, agentObject string) int {
	patches := mapGet(entry, "patches")
	if patches == nil || patches.Kind != yaml.SequenceNode {
		return 0
	}
	n := 0
	for _, p := range patches.Content {
		if patchTargetKind(p) != "HelmRelease" {
			continue
		}
		body := mapGet(p, "patch")
		if body == nil || body.Kind != yaml.ScalarNode {
			continue
		}
		var doc struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if yaml.Unmarshal([]byte(body.Value), &doc) == nil && doc.Metadata.Name == agentObject {
			n++
		}
	}
	return n
}
