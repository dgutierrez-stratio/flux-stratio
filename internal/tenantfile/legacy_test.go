package tenantfile

import (
	"os"
	"path/filepath"
	"testing"
)

// legacyAgentTenant is the shape the legacy client left: the opensearch1
// agent's HelmRelease patch in the parent entry's top-level patches, where
// nothing reads it, next to a patch of its own HelmRelease and one for a
// different kind.
const legacyAgentTenant = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  defaultValues:
    components:
      opensearch:
      - name: opensearch1
        patches:
        - patch: |
            apiVersion: helm.toolkit.fluxcd.io/v2
            kind: HelmRelease
            metadata:
              name: opensearch1-gosec-agent
            spec:
              values:
                general:
                  identity:
                    approlename: legacy
          target:
            kind: HelmRelease
        - patch: |
            apiVersion: helm.toolkit.fluxcd.io/v2
            kind: HelmRelease
            metadata:
              name: opensearch1-other
            spec: {}
          target:
            kind: HelmRelease
        - patch: |
            apiVersion: opensearch.stratio.com/v1
            kind: OsCluster
            metadata:
              name: opensearch1-gosec-agent
          target:
            kind: OsCluster
        config:
          agent:
            patches:
            - patch: |
                apiVersion: helm.toolkit.fluxcd.io/v2
                kind: HelmRelease
                metadata:
                  name: opensearch1-gosec-agent
              target:
                kind: HelmRelease
      psql:
      - name: psql
`

func TestLegacyAgentPatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant.yaml")
	if err := os.WriteFile(path, []byte(legacyAgentTenant), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, entry, agent string
		want               int
	}{
		// Only the top-level HelmRelease patch named for the agent counts: not
		// another HelmRelease, another kind, or the config.agent.patches one.
		{"legacy placement", "opensearch1", "opensearch1-gosec-agent", 1},
		{"no patch for that object", "opensearch1", "psql-gosec-agent", 0},
		{"entry without patches", "psql", "psql-gosec-agent", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := FindComponentEntry(d, tt.entry)
			if err != nil {
				t.Fatal(err)
			}
			if got := LegacyAgentPatches(entry, tt.agent); got != tt.want {
				t.Errorf("LegacyAgentPatches(%s, %s) = %d, want %d", tt.entry, tt.agent, got, tt.want)
			}
		})
	}
}

// Shapes a hand-edited file can have must never panic or count: no patches,
// patches that isn't a list, a patch without a body or whose body isn't YAML
// for a mapping, and a body that isn't a string.
func TestLegacyAgentPatches_OddShapesCountNothing(t *testing.T) {
	const tenant = `spec:
  defaultValues:
    components:
      opensearch:
      - name: noPatches
      - name: notAList
        patches: none
      - name: odd
        patches:
        - target:
            kind: HelmRelease
        - patch: "just: [a"
          target:
            kind: HelmRelease
        - patch: not a mapping
          target:
            kind: HelmRelease
        - patch:
            metadata:
              name: odd-gosec-agent
          target:
            kind: HelmRelease
        - patch: |
            metadata:
              name: odd-gosec-agent
`
	path := filepath.Join(t.TempDir(), "tenant.yaml")
	if err := os.WriteFile(path, []byte(tenant), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"noPatches", "notAList", "odd"} {
		entry, err := FindComponentEntry(d, name)
		if err != nil {
			t.Fatal(err)
		}
		if got := LegacyAgentPatches(entry, name+"-gosec-agent"); got != 0 {
			t.Errorf("%s: LegacyAgentPatches = %d, want 0", name, got)
		}
	}
	if got := LegacyAgentPatches(nil, "x"); got != 0 {
		t.Errorf("nil entry: LegacyAgentPatches = %d, want 0", got)
	}
}

func TestLegacyAgentPatches_CountsEveryCopy(t *testing.T) {
	const tenant = `spec:
  defaultValues:
    components:
      postgres:
      - name: psql
        patches:
        - patch: |
            metadata:
              name: psql-gosec-agent
          target:
            kind: HelmRelease
        - patch: |
            metadata:
              name: psql-gosec-agent
          target:
            kind: HelmRelease
`
	path := filepath.Join(t.TempDir(), "tenant.yaml")
	if err := os.WriteFile(path, []byte(tenant), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := FindComponentEntry(d, "psql")
	if err != nil {
		t.Fatal(err)
	}
	if got := LegacyAgentPatches(entry, "psql-gosec-agent"); got != 2 {
		t.Errorf("LegacyAgentPatches = %d, want 2", got)
	}
}
