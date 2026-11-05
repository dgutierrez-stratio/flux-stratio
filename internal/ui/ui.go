// Package ui renders flux-stratio's result data — the actual output of a
// command, as opposed to internal/log's progress narration. Result data
// always goes to stdout; internal/log's progress lines always go to stderr
// (see the project's "Console output" design rule), so the two are never
// mixed on the same stream. Just two primitives, each reused by more than
// one command, instead of a table/list/patch renderer per command.
package ui

import "io"

// Patch writes patchYAML — a full `patches: [...]` block, as produced by
// internal/diff — to w, unmodified. It is flux-stratio's single call site
// for this output, unlike the Python client, which carried three
// near-identical copies of the same emitter (cmd_patch.py's two branches
// and cmd_patch_chart.py's own).
func Patch(w io.Writer, patchYAML []byte) error {
	_, err := w.Write(patchYAML)
	return err
}
