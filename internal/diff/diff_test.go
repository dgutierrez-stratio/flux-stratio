package diff

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMarshalPatchYAML_StrategicMerge(t *testing.T) {
	doc := PatchDoc{
		TargetKind: "HelmRelease",
		Patch: map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata":   map[string]any{"name": "psql-gosec-agent"},
			"spec":       map[string]any{"values": map[string]any{"foo": "bar"}},
		},
	}
	out, err := MarshalPatchYAML(doc)
	if err != nil {
		t.Fatalf("MarshalPatchYAML returned error: %v", err)
	}
	s := string(out)
	for _, want := range []string{"patches:", "patch: |", "target:", "kind: HelmRelease", "apiVersion: helm.toolkit.fluxcd.io/v2", "foo: bar"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
}

func TestMarshalPatchYAML_UsesTwoSpaceIndent(t *testing.T) {
	// gopkg.in/yaml.v3's default Marshal would indent "values:" 4 spaces
	// deeper than "spec:" (and so on up the tree); this plugin's
	// convention (internal/tenantfile.Doc.Bytes) is 2 spaces per level
	// everywhere it writes YAML — including a generated patch's literal
	// `patch: |` block body, which isn't re-flowed once it's spliced into
	// the tenant file (it's a pre-rendered string embedded verbatim).
	doc := PatchDoc{
		TargetKind: "HelmRelease",
		Patch: map[string]any{
			"spec": map[string]any{"values": map[string]any{"foo": "bar"}},
		},
	}
	out, err := MarshalPatchYAML(doc)
	if err != nil {
		t.Fatalf("MarshalPatchYAML returned error: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"\n  - patch: |",
		"\n      spec:",
		"\n        values:",
		"\n          foo: bar",
		"\n    target:",
		"\n      kind: HelmRelease",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q (want 2-space indent throughout); got:\n%s", want, s)
		}
	}
}

func TestMarshalPatchYAML_JSON6902(t *testing.T) {
	doc := PatchDoc{
		TargetKind: "PgCluster",
		Patch: []JSONPatchOp{
			{Op: "add", Path: "/spec/nodes/1", Value: map[string]any{"name": "b"}},
		},
	}
	out, err := MarshalPatchYAML(doc)
	if err != nil {
		t.Fatalf("MarshalPatchYAML returned error: %v", err)
	}
	s := string(out)
	for _, want := range []string{"op: add", "path: /spec/nodes/1", "kind: PgCluster"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
}

func TestMarshalPatchYAML_LiteralBlockNotEscaped(t *testing.T) {
	// The embedded patch must be a real "|" block, not a quoted/escaped
	// scalar (which would be unreadable and, more importantly, is not
	// what Flux's own patches: field expects).
	doc := PatchDoc{TargetKind: "Foo", Patch: map[string]any{"a": "b"}}
	out, err := MarshalPatchYAML(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `patch: "`) || strings.Contains(string(out), "patch: '") {
		t.Errorf("patch value looks quoted/escaped, want a literal \"|\" block:\n%s", out)
	}
}

func TestPatchEntryNode_LiteralBlockAndShape(t *testing.T) {
	doc := PatchDoc{
		TargetKind: "HelmRelease",
		Patch: map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata":   map[string]any{"name": "psql-gosec-agent"},
			"spec":       map[string]any{"values": map[string]any{"foo": "bar"}},
		},
	}
	node, err := PatchEntryNode(doc)
	if err != nil {
		t.Fatalf("PatchEntryNode returned error: %v", err)
	}

	out, err := yaml.Marshal(node)
	if err != nil {
		t.Fatalf("marshaling the built node returned error: %v", err)
	}
	s := string(out)
	for _, want := range []string{"patch: |", "target:", "kind: HelmRelease", "foo: bar"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(s), "patches:") {
		t.Error("PatchEntryNode must not include the outer 'patches:' wrapper key — that's MarshalPatchYAML's job")
	}
}

func TestPatchEntryNode_LiteralBlockBodyUsesTwoSpaceIndent(t *testing.T) {
	// PatchEntryNode is what internal/tenantfile.Splice embeds into the
	// tenant file — its literal `patch: |` block body must already be
	// 2-space indented, since splicing doesn't re-flow a literal
	// scalar's text, only the document structure around it.
	doc := PatchDoc{
		TargetKind: "HelmRelease",
		Patch: map[string]any{
			"spec": map[string]any{"values": map[string]any{"foo": "bar"}},
		},
	}
	node, err := PatchEntryNode(doc)
	if err != nil {
		t.Fatalf("PatchEntryNode returned error: %v", err)
	}

	patchField := node.Content[0] // the "patch" key's value scalar
	for i, c := range node.Content {
		if c.Value == "patch" {
			patchField = node.Content[i+1]
			break
		}
	}
	if !strings.Contains(patchField.Value, "\n  values:") || !strings.Contains(patchField.Value, "\n    foo: bar") {
		t.Errorf("literal patch body not 2-space indented; got:\n%s", patchField.Value)
	}
}

func TestPatchEntryNode_MatchesMarshalPatchYAMLContent(t *testing.T) {
	// The single-entry node and the wrapped document must describe the
	// same patch — this pins that MarshalPatchYAML and PatchEntryNode
	// share one construction path (newPatchEntry) and can never drift.
	doc := PatchDoc{TargetKind: "PgCluster", Patch: []JSONPatchOp{{Op: "add", Path: "/spec/x", Value: "y"}}}

	wrapped, err := MarshalPatchYAML(doc)
	if err != nil {
		t.Fatal(err)
	}
	node, err := PatchEntryNode(doc)
	if err != nil {
		t.Fatal(err)
	}
	entryYAML, err := yaml.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"op: add", "path: /spec/x", "kind: PgCluster"} {
		if !strings.Contains(string(wrapped), want) || !strings.Contains(string(entryYAML), want) {
			t.Errorf("both outputs should contain %q\nwrapped:\n%s\nentry:\n%s", want, wrapped, entryYAML)
		}
	}
}
