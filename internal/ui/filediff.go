package ui

import (
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

// FileDiff writes a plain unified diff between before and after to w, in the
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
// patch is empty. FileDiffWith is the styled one (labels, colour, headers).
func FileDiff(w io.Writer, before, after string) error {
	return FileDiffWith(w, before, after, DiffOptions{})
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

// maxLCSCells caps the longest-common-subsequence table diffLines builds
// (cells, 8 bytes each): two documents of thousands of lines that share
// nothing would otherwise ask for gigabytes. Past it the differing middle is
// shown as removed in full and added in full — correct, if not minimal.
var maxLCSCells = 16_000_000

// diffLines computes an edit script turning a into b. What the two share at
// the start and at the end is peeled off first (most diffs are a few changes in
// a long document), and the middle gets a classic longest-common-subsequence
// table, O(len*len) time and space — within maxLCSCells, past which the
// middle is one block of deletions and one of insertions.
func diffLines(a, b []string) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ops := make([]diffOp, 0, len(a)+len(b))
	for _, l := range a[:pre] {
		ops = append(ops, diffOp{tag: tagEqual, text: l})
	}
	ops = append(ops, diffMiddle(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffOp{tag: tagEqual, text: l})
	}
	return ops
}

func diffMiddle(a, b []string) []diffOp {
	n, m := len(a), len(b)
	if n == 0 || m == 0 || (n+1)*(m+1) > maxLCSCells {
		ops := make([]diffOp, 0, n+m)
		for _, l := range a {
			ops = append(ops, diffOp{tag: tagDelete, text: l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{tag: tagInsert, text: l})
		}
		return ops
	}
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
