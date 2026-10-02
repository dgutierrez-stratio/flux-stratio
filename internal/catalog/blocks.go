package catalog

import "regexp"

// blockStartRe matches the start of a top-level component loop: `<<- range
// $component := $<key> >>`. Only loops using the loop variable literally
// named $component are matched — nested lookups that borrow a different
// variable name (e.g. `range $p := $postgres` inside genai's template,
// used only to read a dependency's own gosec-agent name for a dependsOn
// reference) are deliberately invisible to this regex, so they are never
// mistaken for a second declaration of the postgres component itself.
var blockStartRe = regexp.MustCompile(`<<-?\s*range\s+\$component\s*:=\s*\$(\w+)\s*>>`)

// componentBlock is the template text belonging to one top-level component
// loop.
type componentBlock struct {
	key  string
	text string
}

// splitComponentBlocks splits a resourcesTemplate string into per-component
// blocks: each block runs from just after a `range $component := $<key>`
// marker to the start of the next one (or end of file). This mirrors the
// Python client's own strategy and the same convention it depends on —
// component loops are sequential and never overlap at the top level of a
// template file — rather than a real Go-template parser, which this
// package deliberately doesn't need: it extracts static facts for
// validation, not rendered output (see design decision 6 in the project
// plan: actual rendering is left to `flux-operator build rset`).
func splitComponentBlocks(template string) []componentBlock {
	matches := blockStartRe.FindAllStringSubmatchIndex(template, -1)
	blocks := make([]componentBlock, 0, len(matches))
	for i, m := range matches {
		start := m[1] // end of the whole match
		end := len(template)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		blocks = append(blocks, componentBlock{key: template[m[2]:m[3]], text: template[start:end]})
	}
	return blocks
}
