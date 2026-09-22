package catalog

import (
	"strings"
	"testing"
)

func TestSplitComponentBlocks_Basic(t *testing.T) {
	tmpl := `
    <<- range $component := $foo >>
    foo body
    <<- end >>
    <<- range $component := $bar >>
    bar body
    <<- end >>
`
	blocks := splitComponentBlocks(tmpl)
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].key != "foo" || blocks[1].key != "bar" {
		t.Errorf("keys = %q, %q", blocks[0].key, blocks[1].key)
	}
	if !strings.Contains(blocks[0].text, "foo body") || strings.Contains(blocks[0].text, "bar body") {
		t.Errorf("block[0] text should contain only its own body: %q", blocks[0].text)
	}
	if !strings.Contains(blocks[1].text, "bar body") {
		t.Errorf("block[1] text = %q, want it to contain \"bar body\"", blocks[1].text)
	}
}

func TestSplitComponentBlocks_LastBlockRunsToEOF(t *testing.T) {
	tmpl := `<<- range $component := $only >>
tail content here
`
	blocks := splitComponentBlocks(tmpl)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if !strings.Contains(blocks[0].text, "tail content here") {
		t.Errorf("block text = %q, want it to reach EOF", blocks[0].text)
	}
}

func TestSplitComponentBlocks_NestedDifferentLoopVarIgnored(t *testing.T) {
	// A nested `range $p := $postgres` (a dependency lookup, not a second
	// declaration of the postgres component) must not be mistaken for a
	// second top-level block.
	tmpl := `
    <<- range $component := $genai >>
    <<- range $p := $postgres >>
    nested lookup, not a new block
    <<- end >>
    genai body
    <<- end >>
`
	blocks := splitComponentBlocks(tmpl)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1 (nested $p loop must not split)", len(blocks))
	}
	if blocks[0].key != "genai" {
		t.Errorf("key = %q, want %q", blocks[0].key, "genai")
	}
}

func TestSplitComponentBlocks_NoMatches(t *testing.T) {
	if got := splitComponentBlocks("no component loops here"); len(got) != 0 {
		t.Errorf("len(blocks) = %d, want 0", len(got))
	}
}
