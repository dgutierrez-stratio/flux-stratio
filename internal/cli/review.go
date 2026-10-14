package cli

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Stratio/flux-stratio/internal/log"
)

// A review is what `apps diff` and `apps migrate` have to say about an app
// beyond the patch: everything they left out of it, and why. It is laid out
// once, as sections ordered by what the operator has to do about them —
// notes first, then what to review, and what needs action last, next to the
// question that follows — instead of one warning line per finding.

// reviewLevel orders and styles a section.
type reviewLevel int

const (
	// levelNote is information: nothing to do, but nothing hidden either.
	levelNote reviewLevel = iota
	// levelReview is a value the operator should look at: the patch leaves it as GitOps renders it.
	levelReview
	// levelAction is something that has to be done, or the migrated workload breaks.
	levelAction
)

func (l reviewLevel) badge() string {
	switch l {
	case levelAction:
		return "[ACTION]"
	case levelReview:
		return "[REVIEW]"
	default:
		return "[NOTE]"
	}
}

func (l reviewLevel) ansi() string {
	switch l {
	case levelAction:
		return "1;31" // bold red
	case levelReview:
		return "1;33" // bold yellow
	default:
		return "1;36" // bold cyan
	}
}

// reviewSection is one kind of finding: a title that says how many and what,
// a hint on what it means or what to do, and the findings themselves as one
// table of aligned rows. Every section has the same shape, so a review reads
// the same whatever it holds; what -v adds is more columns, never another
// layout.
type reviewSection struct {
	Level reviewLevel
	Title string
	Hint  string
	// Header names the columns, or is nil.
	Header []string
	// Rows are the findings; the first column is the thing's name.
	Rows [][]string
	// Lines are printed as they are, indented, after the hint: text that
	// must not be cut to fit a column (a statement, instructions).
	Lines []string
}

// items counts the findings of s: its rows, or 1 for a section that is a
// single statement.
func (s reviewSection) items() int {
	if s.Level == levelNote {
		return 1 // a note is one statement, however many rows back it up
	}
	return max(len(s.Rows), 1)
}

// reviewStyle is how a review is laid out for the terminal it goes to.
type reviewStyle struct {
	// Width is the terminal width; 0 means unknown.
	Width int
	// Color enables ANSI styling.
	Color bool
	// Verbose lists the notes and adds a WHY column to the tables of
	// variables, instead of leaving the notes at a one-line count.
	Verbose bool
}

func styleFor(logger *log.Logger) reviewStyle {
	return reviewStyle{Width: logger.Width(), Color: logger.Color(), Verbose: logger.Verbose()}
}

const (
	defaultWidth = 100
	maxWidth     = 160
	minWidth     = 60
	nameColMax   = 36
)

func (s reviewStyle) width() int {
	w := s.Width
	if w == 0 {
		w = defaultWidth
	}
	return min(max(w, minWidth), maxWidth)
}

func (s reviewStyle) paint(code, text string) string {
	if !s.Color || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s reviewStyle) dim(text string) string { return s.paint("2", text) }

// renderReview lays sections out as lines: a blank line, a badge and title,
// the hint, then each group's rows as an aligned table, and a one-line
// summary at the end. Sections come out notes first, actions last, in the
// order given within a level. No sections, no lines.
func renderReview(sections []reviewSection, st reviewStyle) []string {
	if len(sections) == 0 {
		return nil
	}
	ordered := append([]reviewSection(nil), sections...)
	sort.SliceStable(ordered, func(a, b int) bool { return ordered[a].Level < ordered[b].Level })

	width := st.width()
	var out []string
	counts := map[reviewLevel]int{}
	var hidden []string
	for _, sec := range ordered {
		if sec.Level == levelNote && !st.Verbose {
			// Notes have nothing to act on: a line says what there is, and -v lists it.
			hidden = append(hidden, sec.Title)
			continue
		}
		counts[sec.Level] += sec.items()
		out = append(out, "", st.paint(sec.Level.ansi(), sec.Level.badge())+" "+sec.Title)
		for _, line := range wrap(sec.Hint, width-4) {
			out = append(out, "    "+st.dim(line))
		}
		for _, line := range sec.Lines {
			out = append(out, "    "+line)
		}
		out = append(out, renderTable(sec.Header, sec.Rows, "  ", width, st)...)
	}
	if len(hidden) > 0 {
		line := st.paint(levelNote.ansi(), levelNote.badge()) + " " + strings.Join(hidden, "; ") + st.dim("  (-v lists them)")
		out = append([]string{"", line}, out...)
	}
	if s := summary(counts); s != "" {
		out = append(out, "", st.dim("  "+s))
	}
	return out
}

func summary(counts map[reviewLevel]int) string {
	var parts []string
	if n := counts[levelAction]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d to act on", n))
	}
	if n := counts[levelReview]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d to review", n))
	}
	if n := counts[levelNote]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d note(s)", n))
	}
	return strings.Join(parts, " · ")
}

// renderTable aligns rows (and header) in columns: the first as wide as its
// longest cell up to nameColMax, the others sharing what is left. A cell that
// doesn't fit is cut with an ellipsis — or, with -v, wrapped onto further
// lines so nothing is lost (only the WHY column, when there is one). The last
// column is never padded.
func renderTable(header []string, body [][]string, indent string, width int, st reviewStyle) []string {
	rows := body
	if header != nil {
		rows = append([][]string{header}, rows...)
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return nil
	}
	natural := make([]int, cols)
	for _, r := range rows {
		for i, c := range r {
			natural[i] = max(natural[i], utf8.RuneCountInString(oneLine(c)))
		}
	}
	natural[0] = min(natural[0], nameColMax)

	// With a WHY column, that is what grows lines: the values are already cut
	// to what differs and stay on one.
	hasWhy := header != nil && cols > 1 && len(header) == cols && header[cols-1] == "WHY"

	const gap = 2
	budget := width - len(indent) - gap*(cols-1)
	widths := fitColumns(natural, budget, header)

	var out []string
	for ri, r := range rows {
		cells := make([][]string, cols)
		height := 1
		for i := 0; i < cols; i++ {
			cell := ""
			if i < len(r) {
				cell = oneLine(r[i])
			}
			if st.Verbose && (!hasWhy || i == cols-1) {
				cells[i] = wrapAnywhere(cell, widths[i])
			} else {
				cells[i] = []string{fit(cell, widths[i])}
			}
			height = max(height, len(cells[i]))
		}
		for line := 0; line < height; line++ {
			var b strings.Builder
			b.WriteString(indent)
			for i := 0; i < cols; i++ {
				cell := ""
				if line < len(cells[i]) {
					cell = cells[i][line]
				}
				b.WriteString(cell)
				if i < cols-1 {
					b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)+gap))
				}
			}
			text := strings.TrimRight(b.String(), " ")
			if header != nil && ri == 0 {
				text = st.dim(text)
			}
			out = append(out, text)
		}
	}
	return out
}

// wrapAnywhere breaks v into lines of at most n runes, preferring to break
// after a comma, space or slash, then after a dot or dash, and anywhere when a
// token is longer.
func wrapAnywhere(v string, n int) []string {
	if n < 10 {
		n = 10
	}
	r := []rune(v)
	var lines []string
	for len(r) > n {
		cut := n
	search:
		for _, set := range []string{",/ ", ".-_("} {
			for i := n; i > n/3; i-- {
				if strings.ContainsRune(set, r[i-1]) {
					cut = i
					break search
				}
			}
		}
		lines = append(lines, strings.TrimRight(string(r[:cut]), " "))
		r = []rune(strings.TrimLeft(string(r[cut:]), " "))
	}
	return append(lines, string(r))
}

// fitColumns shrinks the columns to fit budget, widest first: the value
// columns down to a comfortable width (the last column, a WHY, keeps more),
// then further, and the name column only as a last resort.
func fitColumns(natural []int, budget int, header []string) []int {
	widths := append([]int(nil), natural...)
	total := func() int {
		t := 0
		for _, w := range widths {
			t += w
		}
		return t
	}
	last := len(widths) - 1
	why := last > 0 && header != nil && last < len(header) && header[last] == "WHY"
	min := func(i, pass int) int {
		switch {
		case i == 0:
			return 20 // the name column, only in the last pass
		case why && i == last && pass == 0:
			return 40
		case why && i == last:
			return 24
		case pass == 0:
			return 24
		}
		return 12
	}
	for pass := 0; pass < 3 && total() > budget; pass++ {
		for total() > budget {
			widest := -1
			for i := range widths {
				if i == 0 && pass < 2 {
					continue
				}
				floor := min(i, pass)
				if pass == 2 {
					floor = min(i, 1)
				}
				if widths[i] > floor && (widest < 0 || widths[i] > widths[widest]) {
					widest = i
				}
			}
			if widest < 0 {
				break
			}
			widths[widest]--
		}
	}
	return widths
}

// fit cuts s to n runes, ending in an ellipsis when it had to.
func fit(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if n <= 1 {
		return string(r[:max(n, 0)])
	}
	return string(r[:n-1]) + "…"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// wrap breaks text into lines of at most width runes, at spaces.
func wrap(text string, width int) []string {
	if text == "" {
		return nil
	}
	var lines []string
	line := ""
	for _, w := range strings.Fields(text) {
		switch {
		case line == "":
			line = w
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(w) <= width:
			line += " " + w
		default:
			lines = append(lines, line)
			line = w
		}
	}
	return append(lines, line)
}

// compactPair shortens two values that differ to what tells them apart: the
// part they share at the start and the end is cut down to a few characters of
// context, with an ellipsis where it was. Values with nothing in common, or
// short ones, come back whole.
func compactPair(a, b string) (string, string) {
	const context, short = 10, 40
	ra, rb := []rune(a), []rune(b)
	if len(ra) <= short && len(rb) <= short {
		return a, b
	}
	prefix := 0
	for prefix < len(ra) && prefix < len(rb) && ra[prefix] == rb[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(ra)-prefix && suffix < len(rb)-prefix && ra[len(ra)-1-suffix] == rb[len(rb)-1-suffix] {
		suffix++
	}
	start := contextStart(ra, prefix, context)
	end := contextEnd(ra, suffix, context)
	return clip(ra, start, end), clip(rb, contextStart(rb, prefix, context), contextEnd(rb, suffix, context))
}

// separators are where a value is cut for context: a token boundary reads
// better than a mid-word cut.
const separators = "/:.-_=,?&@ "

// contextStart is where to start showing v, given prefix shared characters:
// at most context before the difference, moved forward to a separator.
func contextStart(v []rune, prefix, context int) int {
	start := prefix - context
	if start <= 0 {
		return 0
	}
	for i := start; i < prefix; i++ {
		if strings.ContainsRune(separators, v[i]) {
			return i + 1
		}
	}
	return start
}

// contextEnd is where to stop showing v, given suffix shared characters:
// about context after the difference, moved back to a separator, or forward
// to the next one when the window has none, so a token is not cut mid-word.
func contextEnd(v []rune, suffix, context int) int {
	diffEnd := len(v) - suffix
	end := diffEnd + context
	if end >= len(v) {
		return len(v)
	}
	for i := end; i > diffEnd; i-- {
		if strings.ContainsRune(separators, v[i]) {
			return i
		}
	}
	for i := end; i < min(end+8, len(v)); i++ {
		if strings.ContainsRune(separators, v[i]) {
			return i
		}
	}
	return end
}

func clip(v []rune, start, end int) string {
	s := string(v[start:end])
	if start > 0 {
		s = "…" + s
	}
	if end < len(v) {
		s += "…"
	}
	return s
}

// pair shortens two differing values to what tells them apart.
func (s reviewStyle) pair(rendered, live string) (string, string) {
	return compactPair(rendered, live)
}
