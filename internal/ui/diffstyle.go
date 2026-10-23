package ui

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Sections says how a diff names where in the document a change is, in place
// of the line numbers of a plain unified diff — which mean nothing in a sorted
// list of variables, or in a tenant file the reader doesn't count lines of.
type Sections int

const (
	// SectionsNone keeps the plain "@@ -a,b +c,d @@" headers.
	SectionsNone Sections = iota
	// SectionsWorkload is for lines of the form "workload/KEY=value" (a
	// chart-mode comparison of several workloads): context lines are dropped,
	// since neighbouring variables have nothing to do with each other, and
	// "@@ workload @@" is printed where the workload changes. Lines without a
	// workload (a chart with one) are listed with no header.
	SectionsWorkload
	// SectionsYAML is for YAML text: the usual hunks, each headed with the
	// keys that enclose its first change ("spec.template.spec").
	SectionsYAML
)

// DiffOptions styles FileDiffWith. The zero value is FileDiff.
type DiffOptions struct {
	// OldLabel and NewLabel name the two sides in "---" and "+++" lines
	// that head the diff, so it says what is being compared. Both empty: no
	// header.
	OldLabel, NewLabel string
	// Color paints removed lines red, added ones green, headers cyan, and
	// marks, within a removed/added pair of lines for the same key, the part
	// of the value that differs. Plain text carries none of that, so without
	// Color the output is exactly the unified diff.
	Color    bool
	Sections Sections
}

const (
	ansiReset   = "\x1b[0m"
	ansiRed     = "31"
	ansiGreen   = "32"
	ansiCyan    = "36"
	ansiBoldRed = "1;31"
	ansiBoldGrn = "1;32"
	ansiMarkOn  = "\x1b[7m"  // reverse video: the part of a value that differs
	ansiMarkOff = "\x1b[27m" // back to the line's own colour
)

// oneLine keeps a label on its line: a newline in it would break the header.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func (o DiffOptions) paint(code, text string) string {
	if !o.Color || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + ansiReset
}

// FileDiffWith writes the unified diff between before and after like
// FileDiff, styled by o: labelled sides, colour, and headers that say where
// a change is. Identical documents produce no output.
func FileDiffWith(w io.Writer, before, after string, o DiffOptions) error {
	oldLines, newLines := splitLines(before), splitLines(after)
	context := 3
	if o.Sections == SectionsWorkload {
		context = 0
	}
	hunks := buildHunks(diffLines(oldLines, newLines), context)
	if len(hunks) == 0 {
		return nil
	}

	var b strings.Builder
	if o.OldLabel != "" || o.NewLabel != "" {
		b.WriteString(o.paint(ansiBoldRed, "--- "+oneLine(o.OldLabel)) + "\n")
		b.WriteString(o.paint(ansiBoldGrn, "+++ "+oneLine(o.NewLabel)) + "\n")
	}
	section := ""
	for _, h := range hunks {
		switch o.Sections {
		case SectionsWorkload:
			// headers are written per line, where the workload changes
		case SectionsYAML:
			header := fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.oldStart, h.oldCount, h.newStart, h.newCount)
			if path := hunkPath(h, oldLines, newLines); path != "" {
				header += " " + path
			}
			b.WriteString(o.paint(ansiCyan, header) + "\n")
		default:
			b.WriteString(o.paint(ansiCyan, fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.oldStart, h.oldCount, h.newStart, h.newCount)) + "\n")
		}
		ops := h.ops
		if o.Sections == SectionsWorkload {
			ops = groupByWorkload(ops)
		}
		marks := pairMarks(ops)
		for i, op := range ops {
			if o.Sections == SectionsWorkload {
				if s := workloadOf(op.text); s != "" && s != section {
					section = s
					b.WriteString(o.paint(ansiCyan, "@@ "+s+" @@") + "\n")
				}
			}
			b.WriteString(o.line(op, marks[i]) + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// groupByWorkload reorders a run of changes so each workload's lines are
// together — its removals, then its additions, as they were — and a workload's
// header is printed once however the removals and additions of several
// workloads were interleaved by the line diff. Workloads come in the order
// they first appear; lines with no workload are a group of their own.
func groupByWorkload(ops []diffOp) []diffOp {
	var order []string
	groups := map[string][]diffOp{}
	for _, op := range ops {
		w := workloadOf(op.text)
		if _, ok := groups[w]; !ok {
			order = append(order, w)
		}
		groups[w] = append(groups[w], op)
	}
	if len(order) == 1 {
		return ops
	}
	out := make([]diffOp, 0, len(ops))
	for _, w := range order {
		out = append(out, groups[w]...)
	}
	return out
}

// line renders one op: its prefix and text, painted when o.Color, with the
// rune range mark of the text reversed.
func (o DiffOptions) line(op diffOp, mark [2]int) string {
	prefix, code := " ", ""
	switch op.tag {
	case tagDelete:
		prefix, code = "-", ansiRed
	case tagInsert:
		prefix, code = "+", ansiGreen
	}
	if !o.Color || code == "" {
		return prefix + op.text
	}
	// Text that isn't valid UTF-8 (a binary value) is left byte for byte: a
	// rune conversion would rewrite it.
	r := []rune(op.text)
	if !utf8.ValidString(op.text) || mark[0] < 0 || mark[1] <= mark[0] || mark[1] > len(r) {
		return "\x1b[" + code + "m" + prefix + op.text + ansiReset
	}
	return "\x1b[" + code + "m" + prefix + string(r[:mark[0]]) + ansiMarkOn + string(r[mark[0]:mark[1]]) + ansiMarkOff + string(r[mark[1]:]) + ansiReset
}

// workloadOf is the workload of a "workload/KEY=value" line: what precedes
// the first "/" when no "=" comes before it. "" for any other line.
func workloadOf(text string) string {
	slash := strings.Index(text, "/")
	if slash <= 0 {
		return ""
	}
	if eq := strings.Index(text, "="); eq >= 0 && eq < slash {
		return ""
	}
	for _, r := range text[:slash] { // a Kubernetes name: anything else is not a workload
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '.' && r != '_' {
			return ""
		}
	}
	return text[:slash]
}

// keyLen is how many bytes of a line are its key and separator: up to and
// including the "=" of "KEY=value", or the ": " of a YAML "key: value" line.
// 0 when the line has neither.
func keyLen(text string) int {
	if i := strings.Index(text, "="); i > 0 && !strings.ContainsAny(text[:i], " :") {
		return i + 1
	}
	if i := strings.Index(text, ": "); i > 0 {
		return i + 2
	}
	return 0
}

// pairMarks finds, in each run of removed lines followed by added ones, the
// pairs that set the same key, and returns for every op the rune range of its
// value that differs from its partner's (the part left once what the two
// values share at the start and at the end is cut away). Ops with no partner,
// or whose values only differ by being absent, have an empty range.
func pairMarks(ops []diffOp) [][2]int {
	marks := make([][2]int, len(ops))
	for i := 0; i < len(ops); {
		if ops[i].tag != tagDelete {
			i++
			continue
		}
		delEnd := i
		for delEnd < len(ops) && ops[delEnd].tag == tagDelete {
			delEnd++
		}
		insEnd := delEnd
		for insEnd < len(ops) && ops[insEnd].tag == tagInsert {
			insEnd++
		}
		used := map[int]bool{}
		for d := i; d < delEnd; d++ {
			kd := keyLen(ops[d].text)
			if kd == 0 {
				continue
			}
			for n := delEnd; n < insEnd; n++ {
				kn := keyLen(ops[n].text)
				if used[n] || kn != kd || ops[n].text[:kn] != ops[d].text[:kd] {
					continue
				}
				used[n] = true
				marks[d], marks[n] = differingRanges(ops[d].text, ops[n].text, kd)
				break
			}
		}
		i = insEnd
	}
	return marks
}

// differingRanges returns the rune ranges of a and b, both with a key of
// key bytes, that differ once their common value prefix and suffix are cut.
// A difference that sits inside a word (false/true, which share only an
// "e") is widened to the whole word, so it reads as one change; one that is a
// separator itself (an "_" for a "-") stays as narrow as it is.
func differingRanges(a, b string, key int) ([2]int, [2]int) {
	base := utf8.RuneCountInString(a[:key])
	ra, rb := []rune(a[key:]), []rune(b[key:])
	p := 0
	for p < len(ra) && p < len(rb) && ra[p] == rb[p] {
		p++
	}
	s := 0
	for s < len(ra)-p && s < len(rb)-p && ra[len(ra)-1-s] == rb[len(rb)-1-s] {
		s++
	}
	midA, midB := ra[p:len(ra)-s], rb[p:len(rb)-s]
	if len(midA) > 0 && len(midB) > 0 && !hasSeparator(midA) && !hasSeparator(midB) {
		for p > 0 && !isSeparator(ra[p-1]) {
			p--
		}
		for k := len(ra) - s; s > 0 && !isSeparator(ra[k]); k++ {
			s--
		}
	}
	return [2]int{base + p, base + len(ra) - s}, [2]int{base + p, base + len(rb) - s}
}

func isSeparator(r rune) bool { return strings.ContainsRune("/:.-_=,?&@ ;|()[]{}'\"", r) }

func hasSeparator(r []rune) bool {
	for _, c := range r {
		if isSeparator(c) {
			return true
		}
	}
	return false
}

// hunkPath is the keys that enclose the first change of h, as "a.b.c": found
// by walking up the side of the document that line is on, to the nearest
// line with a smaller indent each time.
func hunkPath(h hunk, oldLines, newLines []string) string {
	oi, ni := h.oldStart-1, h.newStart-1
	for _, op := range h.ops {
		switch op.tag {
		case tagEqual:
			oi++
			ni++
		case tagDelete:
			return breadcrumb(oldLines, oi)
		default:
			return breadcrumb(newLines, ni)
		}
	}
	return ""
}

func breadcrumb(lines []string, idx int) string {
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	cur := indent(lines[idx])
	if strings.HasPrefix(strings.TrimSpace(lines[idx]), "- ") {
		cur += 2
	}
	var keys []string
	for i := idx - 1; i >= 0 && cur > 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		// "- key: v" holds its fields two columns further in than its dash.
		ind := indent(lines[i])
		if strings.HasPrefix(t, "- ") {
			ind += 2
		}
		if ind >= cur {
			continue
		}
		t = strings.TrimPrefix(t, "- ")
		k := strings.Index(t, ":")
		if k <= 0 {
			continue // a scalar or a list item with no key encloses nothing
		}
		cur = ind
		keys = append([]string{t[:k]}, keys...)
	}
	if len(keys) > 4 {
		keys = keys[len(keys)-4:]
	}
	return strings.Join(keys, ".")
}
