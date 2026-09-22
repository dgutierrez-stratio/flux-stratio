package ui

import (
	"fmt"
	"io"
	"strings"
)

// diffTag identifies which side of a comparison a diffOp's line belongs to.
type diffTag byte

const (
	tagEqual diffTag = iota
	tagDelete
	tagInsert
)

// diffOp is one line of an edit script turning "before" into "after".
type diffOp struct {
	tag  diffTag
	text string
}

// FileDiff writes a unified diff between before and after to w, in the
// style of `diff -u`: hunks of "@@ -oldStart,oldCount +newStart,newCount
// @@" headers, a few lines of surrounding context, "-" for removed lines
// and "+" for added lines. It is flux-stratio's one diff renderer, reused
// both to preview a tenant-file edit (apps migrate --dry-run) and, by
// default, to show the difference between the rendered GitOps desired
// state and the live legacy cluster (apps diff).
//
// Two identical documents produce no output at all — a caller that wants
// to announce "no differences" checks for that itself before calling
// FileDiff, the same way internal/diff already needs to know whether a
// patch is empty.
func FileDiff(w io.Writer, before, after string) error {
	ops := diffLines(splitLines(before), splitLines(after))
	for _, h := range buildHunks(ops, 3) {
		if _, err := fmt.Fprintf(w, "@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldCount, h.newStart, h.newCount); err != nil {
			return err
		}
		for _, op := range h.ops {
			prefix := ' '
			switch op.tag {
			case tagDelete:
				prefix = '-'
			case tagInsert:
				prefix = '+'
			}
			if _, err := fmt.Fprintf(w, "%c%s\n", prefix, op.text); err != nil {
				return err
			}
		}
	}
	return nil
}

// splitLines splits s on "\n", dropping one trailing empty element left by
// a final newline so a file ending in "\n" and one that doesn't diff the
// same way.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// diffLines computes a minimal edit script turning a into b from a classic
// longest-common-subsequence table, O(len(a)*len(b)) time and space. That
// is more than adequate for what this plugin diffs — tenant YAML files and
// rendered Helm/CR value trees, at most a few hundred lines.
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{tag: tagEqual, text: a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{tag: tagDelete, text: a[i]})
			i++
		default:
			ops = append(ops, diffOp{tag: tagInsert, text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{tag: tagDelete, text: a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{tag: tagInsert, text: b[j]})
	}
	return ops
}

// hunk is one contiguous block of a unified diff: a run of changed lines
// plus up to `context` lines of unchanged text on either side.
type hunk struct {
	oldStart, oldCount int
	newStart, newCount int
	ops                []diffOp
}

// indexedOp tags a diffOp with the 1-based line number it occupies on each
// side it exists on, so hunk boundaries can be expressed as line ranges.
type indexedOp struct {
	diffOp
	oldLine, newLine int
}

// buildHunks groups a full edit script into unified-diff hunks. Changed
// regions (any run containing at least one non-equal op) are expanded by
// `context` unchanged lines on each side; regions whose expanded windows
// overlap or touch are merged into a single hunk, exactly as `diff -u`
// does, so two nearby edits don't print two hunks with duplicated context.
func buildHunks(ops []diffOp, context int) []hunk {
	idx := indexOps(ops)

	var windows [][2]int
	k := 0
	for k < len(idx) {
		if idx[k].tag == tagEqual {
			k++
			continue
		}
		start := k
		for k < len(idx) && idx[k].tag != tagEqual {
			k++
		}
		lo, hi := start-context, k+context
		if lo < 0 {
			lo = 0
		}
		if hi > len(idx) {
			hi = len(idx)
		}
		if len(windows) > 0 && lo <= windows[len(windows)-1][1] {
			windows[len(windows)-1][1] = hi
		} else {
			windows = append(windows, [2]int{lo, hi})
		}
	}

	hunks := make([]hunk, 0, len(windows))
	for _, w := range windows {
		hunks = append(hunks, buildHunk(idx[w[0]:w[1]]))
	}
	return hunks
}

func indexOps(ops []diffOp) []indexedOp {
	idx := make([]indexedOp, len(ops))
	oldLine, newLine := 1, 1
	for k, op := range ops {
		idx[k] = indexedOp{diffOp: op, oldLine: oldLine, newLine: newLine}
		switch op.tag {
		case tagEqual:
			oldLine++
			newLine++
		case tagDelete:
			oldLine++
		case tagInsert:
			newLine++
		}
	}
	return idx
}

func buildHunk(window []indexedOp) hunk {
	h := hunk{ops: make([]diffOp, 0, len(window))}
	for i, op := range window {
		if i == 0 {
			h.oldStart, h.newStart = op.oldLine, op.newLine
		}
		h.ops = append(h.ops, op.diffOp)
		switch op.tag {
		case tagEqual:
			h.oldCount++
			h.newCount++
		case tagDelete:
			h.oldCount++
		case tagInsert:
			h.newCount++
		}
	}
	return h
}
