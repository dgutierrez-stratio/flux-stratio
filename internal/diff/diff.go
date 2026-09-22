// Package diff computes the patch flux-stratio would splice into a
// tenant's ResourceSetInputProvider to bring an app's rendered GitOps
// desired state in line with its live legacy state, in either of the
// Python client's two modes: a manifest diff between the rendered object
// and the live cluster object (for CRD-backed apps), or a chart/env-var
// diff between a Helm chart's rendered values and the live workload's
// resolved environment (for HelmRelease-backed apps, via a --chartPath in
// the config).
//
// There is exactly one patch-document type and one YAML serializer for it
// (PatchDoc, MarshalPatchYAML) — the Python client carried three
// near-identical copies of the same `patches: [{patch: |, target:
// {kind}}]` emitter (cmd_patch.py's two branches and cmd_patch_chart.py's
// own).
package diff

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// JSONPatchOp is a single RFC 6902 JSON Patch operation.
type JSONPatchOp struct {
	Op    string `yaml:"op"`
	Path  string `yaml:"path"`
	Value any    `yaml:"value,omitempty"`
}

// PatchDoc is the single `patches:` entry flux-stratio ever emits: a
// literal-block patch — either a strategic-merge object (Patch is a
// map[string]any) or a JSON6902 ops list (Patch is []JSONPatchOp) —
// targeting one Kustomization patch by kind.
type PatchDoc struct {
	TargetKind string
	Patch      any
}

// literalBlock is a string that yaml.v3 always marshals as a "|" literal
// block scalar, so the embedded patch document reads as a real YAML block
// inside the wrapping `patches:` document rather than an escaped string.
type literalBlock string

// MarshalYAML implements yaml.Marshaler.
func (s literalBlock) MarshalYAML() (any, error) {
	return yaml.Node{Kind: yaml.ScalarNode, Style: yaml.LiteralStyle, Value: string(s)}, nil
}

type patchEntry struct {
	Patch  literalBlock `yaml:"patch"`
	Target patchTarget  `yaml:"target"`
}

type patchTarget struct {
	Kind string `yaml:"kind"`
}

type patchWrapper struct {
	Patches []patchEntry `yaml:"patches"`
}

// MarshalPatchYAML renders doc as the standalone `patches: [{patch: |,
// target: {kind}}]` document `apps diff --patch` prints via
// internal/ui.Patch.
func MarshalPatchYAML(doc PatchDoc) ([]byte, error) {
	entry, err := newPatchEntry(doc)
	if err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(patchWrapper{Patches: []patchEntry{entry}})
	if err != nil {
		return nil, fmt.Errorf("marshaling patches wrapper: %w", err)
	}
	return out, nil
}

// PatchEntryNode builds the single {patch: |, target: {kind}} node this
// patch becomes inside a tenant YAML's own `patches:` sequence — the same
// entry shape MarshalPatchYAML wraps in a top-level `patches:` list for
// standalone display. internal/tenantfile splices this node directly into
// the tenant file at the app's anchor, rather than re-parsing
// MarshalPatchYAML's wrapper back apart.
func PatchEntryNode(doc PatchDoc) (*yaml.Node, error) {
	entry, err := newPatchEntry(doc)
	if err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := node.Encode(entry); err != nil {
		return nil, fmt.Errorf("encoding patch entry: %w", err)
	}
	return &node, nil
}

func newPatchEntry(doc PatchDoc) (patchEntry, error) {
	inner, err := yaml.Marshal(doc.Patch)
	if err != nil {
		return patchEntry{}, fmt.Errorf("marshaling patch body: %w", err)
	}
	return patchEntry{Patch: literalBlock(inner), Target: patchTarget{Kind: doc.TargetKind}}, nil
}
