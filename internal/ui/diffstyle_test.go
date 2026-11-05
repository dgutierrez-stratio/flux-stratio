package ui

import (
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func diffWith(t *testing.T, before, after string, o DiffOptions) string {
	t.Helper()
	var b strings.Builder
	if err := FileDiffWith(&b, before, after, o); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestFileDiffWith_LabelsNameTheSides(t *testing.T) {
	got := diffWith(t, "a\nb\n", "a\nX\n", DiffOptions{OldLabel: "desired state", NewLabel: "live cluster"})
	want := "--- desired state\n+++ live cluster\n@@ -1,2 +1,2 @@\n a\n-b\n+X\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	// No difference, no header either.
	if got := diffWith(t, "a\n", "a\n", DiffOptions{OldLabel: "x", NewLabel: "y"}); got != "" {
		t.Errorf("identical documents printed %q", got)
	}
}

func TestFileDiffWith_PlainTextCarriesNoEscapes(t *testing.T) {
	got := diffWith(t, "K=abc\n", "K=abd\n", DiffOptions{OldLabel: "a", NewLabel: "b", Sections: SectionsYAML})
	if strings.Contains(got, "\x1b") {
		t.Errorf("escape codes without Color:\n%q", got)
	}
}

// Chart-mode lines are "workload/KEY=value": context is dropped (sorted
// neighbours are unrelated) and the workload is named where it changes.
func TestFileDiffWith_WorkloadSections(t *testing.T) {
	before := join("monitor/A=1", "monitor/B=2", "monitor/C=3", "ui/A=1", "ui/B=2")
	after := join("monitor/A=1", "monitor/B=9", "monitor/C=3", "ui/A=1", "ui/B=8", "ui/Z=1")
	got := diffWith(t, before, after, DiffOptions{Sections: SectionsWorkload})
	want := "@@ monitor @@\n-monitor/B=2\n+monitor/B=9\n@@ ui @@\n-ui/B=2\n+ui/B=8\n+ui/Z=1\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	// One workload: no prefix, so no header at all — just the changes.
	got = diffWith(t, join("A=1", "B=2"), join("A=1", "B=3"), DiffOptions{Sections: SectionsWorkload})
	if got != "-B=2\n+B=3\n" {
		t.Errorf("single workload: %q", got)
	}

	// A change spanning the boundary between two workloads names both.
	got = diffWith(t, join("a/X=1", "b/Y=1"), join("a/X=1", "b/Y=2"), DiffOptions{Sections: SectionsWorkload})
	if !strings.Contains(got, "@@ b @@") || strings.Contains(got, "@@ a @@") {
		t.Errorf("only b changed:\n%s", got)
	}
}

// A YAML hunk is headed with the keys around its first change.
func TestFileDiffWith_YAMLBreadcrumb(t *testing.T) {
	before := join("spec:", "  template:", "    spec:", "      containers:", "      - name: app", "        image: a:1", "status: ok")
	after := join("spec:", "  template:", "    spec:", "      containers:", "      - name: app", "        image: a:2", "status: ok")
	got := diffWith(t, before, after, DiffOptions{Sections: SectionsYAML})
	if !strings.HasPrefix(got, "@@ -3,5 +3,5 @@ spec.template.spec.containers\n") {
		t.Errorf("header:\n%s", got)
	}

	// A top-level change has no path, and the header stays the plain one.
	got = diffWith(t, "a: 1\nb: 2\n", "a: 1\nb: 3\n", DiffOptions{Sections: SectionsYAML})
	if !strings.HasPrefix(got, "@@ -1,2 +1,2 @@\n") {
		t.Errorf("top-level change: %q", got)
	}
}

func TestFileDiffWith_Color(t *testing.T) {
	got := diffWith(t, "a\nVAULT_ROLE=stratio-apps_monitor\n", "a\nVAULT_ROLE=stratio-apps-monitor\n", DiffOptions{OldLabel: "desired", NewLabel: "live", Color: true})
	for _, want := range []string{
		"\x1b[1;31m--- desired\x1b[0m", "\x1b[1;32m+++ live\x1b[0m", "\x1b[36m@@ -1,2 +1,2 @@\x1b[0m",
		// only the differing character is reversed, on each side
		"\x1b[31m-VAULT_ROLE=stratio-apps\x1b[7m_\x1b[27mmonitor\x1b[0m",
		"\x1b[32m+VAULT_ROLE=stratio-apps\x1b[7m-\x1b[27mmonitor\x1b[0m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%q", want, got)
		}
	}
	if !strings.Contains(got, "\n a\n") {
		t.Errorf("context lines must stay uncoloured:\n%q", got)
	}
}

func TestPairMarks(t *testing.T) {
	ops := []diffOp{
		{tag: tagDelete, text: "wl/A=old"}, {tag: tagDelete, text: "wl/B=x"},
		{tag: tagInsert, text: "wl/B=y"}, {tag: tagInsert, text: "wl/C=new"},
	}
	m := pairMarks(ops)
	// A is only removed and C only added: nothing to compare. B differs wholly.
	if m[0] != [2]int{} || m[3] != [2]int{} {
		t.Errorf("unpaired lines marked: %v", m)
	}
	if m[1] != [2]int{5, 6} || m[2] != [2]int{5, 6} {
		t.Errorf("B marks = %v %v, want {5 6} on both", m[1], m[2])
	}
	// Same value, same key: nothing differs.
	if m := pairMarks([]diffOp{{tag: tagDelete, text: "K=v"}, {tag: tagInsert, text: "K=v"}}); m[0][1] > m[0][0] || m[1][1] > m[1][0] {
		t.Errorf("equal values marked: %v", m)
	}
}

func TestWorkloadOf(t *testing.T) {
	for line, want := range map[string]string{
		"virtualizer/VR_POD=a": "virtualizer",
		"VR_POD=a/b":           "", // the "/" belongs to the value
		"plain":                "",
		"/x=1":                 "",
	} {
		if got := workloadOf(line); got != want {
			t.Errorf("workloadOf(%q) = %q, want %q", line, got, want)
		}
	}
}

// A difference inside a word is shown as the whole word; a separator that
// differs stays narrow.
func TestDifferingRanges_WholeWordsButNarrowSeparators(t *testing.T) {
	a, b := differingRanges("K=false", "K=true", 2)
	if a != [2]int{2, 7} || b != [2]int{2, 6} {
		t.Errorf("false/true = %v %v, want the whole words {2 7} {2 6}", a, b)
	}
	a, b = differingRanges("K=stratio-apps_monitor", "K=stratio-apps-monitor", 2)
	if a != [2]int{14, 15} || b != [2]int{14, 15} {
		t.Errorf("_ for - = %v %v, want just the separator {14 15}", a, b)
	}
	a, b = differingRanges("K=https://a.b/x1/c", "K=https://a.b/x2/c", 2)
	if a != [2]int{14, 16} || b != [2]int{14, 16} {
		t.Errorf("x1/x2 = %v %v, want the word {14 16}", a, b)
	}
}

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func TestFileDiffWith_ReturnsWriteErrors(t *testing.T) {
	if err := FileDiffWith(failingWriter{}, "a\n", "b\n", DiffOptions{}); err == nil {
		t.Error("a failing writer's error was swallowed")
	}
	// Nothing to write, nothing to fail.
	if err := FileDiffWith(failingWriter{}, "a\n", "a\n", DiffOptions{}); err != nil {
		t.Errorf("identical documents wrote something: %v", err)
	}
}

func TestFileDiffWith_Labels(t *testing.T) {
	// One label alone still heads the diff, and the other side stays empty.
	got := diffWith(t, "a\n", "b\n", DiffOptions{OldLabel: "only old"})
	if !strings.HasPrefix(got, "--- only old\n+++ \n") {
		t.Errorf("one label: %q", got)
	}
	// A label cannot break the header across lines.
	got = diffWith(t, "a\n", "b\n", DiffOptions{OldLabel: "one\ntwo\r\nthree", NewLabel: "  spaced   out "})
	if !strings.HasPrefix(got, "--- one two three\n+++ spaced out\n") {
		t.Errorf("multi-line label: %q", got)
	}
}

func TestFileDiffWith_UnknownSectionsIsPlain(t *testing.T) {
	got := diffWith(t, "a\nb\n", "a\nX\n", DiffOptions{Sections: Sections(99)})
	if got != "@@ -1,2 +1,2 @@\n a\n-b\n+X\n" {
		t.Errorf("got %q", got)
	}
}

// One side empty: every line is an addition (or a deletion), nothing panics,
// and the YAML breadcrumb reads the side the line is on.
func TestFileDiffWith_EmptySides(t *testing.T) {
	doc := join("spec:", "  a:", "    b: 1")
	for _, mode := range []Sections{SectionsNone, SectionsWorkload, SectionsYAML} {
		added := diffWith(t, "", doc, DiffOptions{Sections: mode})
		removed := diffWith(t, doc, "", DiffOptions{Sections: mode})
		for _, line := range []string{"+spec:", "+  a:", "+    b: 1"} {
			if !strings.Contains(added, line+"\n") {
				t.Errorf("mode %d: added lacks %q:\n%s", mode, line, added)
			}
		}
		if !strings.Contains(removed, "-    b: 1\n") || strings.Contains(removed, "\n+") {
			t.Errorf("mode %d: removed:\n%s", mode, removed)
		}
	}
	if got := diffWith(t, "", "", DiffOptions{Color: true, OldLabel: "x"}); got != "" {
		t.Errorf("two empty documents: %q", got)
	}
}

func TestFileDiffWith_WorkloadSectionsWithColor(t *testing.T) {
	got := diffWith(t, join("a/K=1", "b/K=1"), join("a/K=2", "b/K=2"), DiffOptions{Sections: SectionsWorkload, Color: true})
	for _, want := range []string{"\x1b[36m@@ a @@\x1b[0m\n", "\x1b[36m@@ b @@\x1b[0m\n"} {
		if strings.Count(got, want) != 1 {
			t.Errorf("want exactly one %q in:\n%q", want, got)
		}
	}
	// The pair of a workload is marked after regrouping, not across workloads.
	if !strings.Contains(got, "\x1b[31m-a/K=\x1b[7m1\x1b[27m\x1b[0m\n\x1b[32m+a/K=\x1b[7m2\x1b[27m\x1b[0m\n") {
		t.Errorf("a's removal and addition are not adjacent and paired:\n%q", got)
	}

	// A run of changes that interleaves workloads is regrouped: each header
	// once, a workload's removals then its additions.
	got = diffWith(t, join("a/K=1", "b/K=1"), join("a/K=2", "b/K=2"), DiffOptions{Sections: SectionsWorkload})
	if got != "@@ a @@\n-a/K=1\n+a/K=2\n@@ b @@\n-b/K=1\n+b/K=2\n" {
		t.Errorf("not regrouped:\n%s", got)
	}
	// A line with no workload between two of one workload doesn't repeat its header.
	got = diffWith(t, join("a/K=1", "plain=1", "a/L=1"), join("a/K=2", "plain=2", "a/L=2"), DiffOptions{Sections: SectionsWorkload})
	if strings.Count(got, "@@ a @@") != 1 {
		t.Errorf("header repeated:\n%s", got)
	}
}

func TestGroupByWorkload(t *testing.T) {
	del := func(s string) diffOp { return diffOp{tag: tagDelete, text: s} }
	ins := func(s string) diffOp { return diffOp{tag: tagInsert, text: s} }
	text := func(ops []diffOp) string {
		var out []string
		for _, op := range ops {
			out = append(out, string("-+"[map[diffTag]int{tagDelete: 0, tagInsert: 1}[op.tag]])+op.text)
		}
		return strings.Join(out, " ")
	}
	got := text(groupByWorkload([]diffOp{del("a/1=x"), del("b/1=x"), del("a/2=x"), ins("a/1=y"), ins("b/1=y"), ins("c/1=y"), del("plain=1")}))
	if got != "-a/1=x -a/2=x +a/1=y -b/1=x +b/1=y +c/1=y -plain=1" {
		t.Errorf("grouped: %s", got)
	}
	// One workload, or none: the same slice comes back untouched.
	same := []diffOp{del("a/1=x"), ins("a/1=y")}
	if got := groupByWorkload(same); &got[0] != &same[0] {
		t.Error("a single group was copied")
	}
	if got := groupByWorkload(nil); len(got) != 0 {
		t.Errorf("nil: %v", got)
	}
}

// A hunk's first change decides its path: the old side for a deletion, the
// new side for an insertion, never an index out of range.
func TestHunkPath(t *testing.T) {
	cases := []struct {
		name          string
		before, after []string
		want          string
	}{
		{"change under a key", []string{"a:", "  b:", "    c: 1"}, []string{"a:", "  b:", "    c: 2"}, "a.b"},
		{"pure insertion reads the new side", []string{"a:", "  b: 1"}, []string{"a:", "  b: 1", "  c:", "    d: 1"}, "a"},
		{"pure deletion reads the old side", []string{"a:", "  b:", "    c: 1", "x: 1"}, []string{"a:", "  b:", "x: 1"}, "a.b"},
		{"top level has no path", []string{"a: 1"}, []string{"a: 2"}, ""},
		{"insertion into an empty document", nil, []string{"a: 1"}, ""},
		{"deletion of everything starts at the top", []string{"a:", "  b: 1"}, nil, ""},
		{"deletion of the last line", []string{"a:", "  b: 1"}, []string{"a:"}, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hunks := buildHunks(diffLines(tc.before, tc.after), 3)
			if len(hunks) == 0 {
				t.Fatal("no hunk")
			}
			if got := hunkPath(hunks[0], tc.before, tc.after); got != tc.want {
				t.Errorf("hunkPath = %q, want %q", got, tc.want)
			}
		})
	}
	if got := hunkPath(hunk{}, nil, nil); got != "" {
		t.Errorf("empty hunk: %q", got)
	}
}

func TestBreadcrumb(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		idx   int
		want  string
	}{
		{"first line", []string{"a: 1"}, 0, ""},
		{"nested", []string{"a:", "  b:", "    c: 1"}, 2, "a.b"},
		{"blank lines and comments are skipped", []string{"a:", "", "  # note", "  b:", "    c: 1"}, 4, "a.b"},
		{"siblings are not parents", []string{"a:", "  x: 1", "  y: 2", "  z: 3"}, 3, "a"},
		{"a list item's own fields are siblings", []string{"c:", "  - name: app", "    image: x"}, 2, "c"},
		{"a list item with a key is a parent of what is under it", []string{"c:", "  - name: app", "      env: 1"}, 2, "c.name"},
		{"a keyless line encloses nothing", []string{"a:", "  b:", "    - one", "    - two", "    - three: 3"}, 4, "a.b"},
		{"a scalar continuation does not hide the parents", []string{"a:", "  b:", "x", "    c: 1"}, 3, "a.b"},
		{"only the last four keys", []string{"a:", " b:", "  c:", "   d:", "    e:", "     f:", "      g: 1"}, 6, "c.d.e.f"},
		{"colon in a value", []string{"a:", "  url: http://x:80", "  b: 1"}, 2, "a"},
		{"tab indented lines count as top level", []string{"a:", "\tb: 1"}, 1, ""},
		{"negative index", []string{"a: 1"}, -1, ""},
		{"index past the end", []string{"a: 1"}, 5, ""},
		{"no lines", nil, 0, ""},
	}
	for _, tc := range cases {
		if got := breadcrumb(tc.lines, tc.idx); got != tc.want {
			t.Errorf("%s: breadcrumb = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestKeyLen(t *testing.T) {
	for line, want := range map[string]int{
		"KEY=value":            4,
		"wl/KEY=value":         7,
		"KEY=":                 4,
		"=value":               0, // no key
		"  image: a:1":         9,
		"- name: app":          8,
		"a: b=c":               3, // the "=" belongs to the value
		"KEY=a: b":             4, // the first separator wins when the key is bare
		"plain":                0,
		"":                     0,
		"ключ=значение":        len("ключ="),
		"has space=value: x=1": len("has space=value: "),
	} {
		if got := keyLen(line); got != want {
			t.Errorf("keyLen(%q) = %d, want %d", line, got, want)
		}
	}
}

func TestPairMarks_Shapes(t *testing.T) {
	del := func(s string) diffOp { return diffOp{tag: tagDelete, text: s} }
	ins := func(s string) diffOp { return diffOp{tag: tagInsert, text: s} }
	eq := func(s string) diffOp { return diffOp{tag: tagEqual, text: s} }
	none := [2]int{}

	t.Run("no ops", func(t *testing.T) {
		if m := pairMarks(nil); len(m) != 0 {
			t.Errorf("%v", m)
		}
	})
	t.Run("deletions with no insertions, and the reverse", func(t *testing.T) {
		for _, m := range pairMarks([]diffOp{del("A=1"), del("B=2")}) {
			if m != none {
				t.Errorf("marked without a partner: %v", m)
			}
		}
		for _, m := range pairMarks([]diffOp{ins("A=1")}) {
			if m != none {
				t.Errorf("marked without a partner: %v", m)
			}
		}
	})
	t.Run("an insertion first is never paired with a later deletion", func(t *testing.T) {
		for _, m := range pairMarks([]diffOp{ins("A=1"), del("A=2")}) {
			if m != none {
				t.Errorf("paired across the wrong direction: %v", m)
			}
		}
	})
	t.Run("an equal line between runs separates them", func(t *testing.T) {
		m := pairMarks([]diffOp{del("A=1"), eq("x"), ins("A=2")})
		if m[0] != none || m[2] != none {
			t.Errorf("paired across an equal line: %v", m)
		}
	})
	t.Run("repeated keys pair one to one in order", func(t *testing.T) {
		m := pairMarks([]diffOp{del("K=aa"), del("K=bb"), ins("K=ac"), ins("K=bd")})
		if m[0] != [2]int{2, 4} || m[2] != [2]int{2, 4} || m[1] != [2]int{2, 4} || m[3] != [2]int{2, 4} {
			t.Errorf("marks = %v", m)
		}
	})
	t.Run("more insertions than deletions", func(t *testing.T) {
		m := pairMarks([]diffOp{del("A=1"), ins("A=2"), ins("A=3"), ins("B=1")})
		if m[0] == none || m[1] == none {
			t.Errorf("the first pair was not marked: %v", m)
		}
		if m[2] != none || m[3] != none {
			t.Errorf("leftover insertions marked: %v", m)
		}
	})
	t.Run("keys that differ do not pair", func(t *testing.T) {
		for _, m := range pairMarks([]diffOp{del("A=1"), ins("B=1")}) {
			if m != none {
				t.Errorf("paired different keys: %v", m)
			}
		}
	})
	t.Run("lines without a key never pair", func(t *testing.T) {
		for _, m := range pairMarks([]diffOp{del("one"), ins("two"), del(""), ins("")}) {
			if m != none {
				t.Errorf("keyless line marked: %v", m)
			}
		}
	})
	t.Run("YAML lines pair on their key", func(t *testing.T) {
		m := pairMarks([]diffOp{del("  image: a:1"), ins("  image: a:2")})
		if m[0] != [2]int{11, 12} || m[1] != [2]int{11, 12} {
			t.Errorf("marks = %v", m)
		}
	})
}

func TestDifferingRanges_Edges(t *testing.T) {
	cases := []struct {
		name, a, b string
		key        int
		wantA      [2]int
		wantB      [2]int
	}{
		{"identical values mark nothing", "K=v", "K=v", 2, [2]int{3, 3}, [2]int{3, 3}},
		{"an empty value against a value", "K=", "K=abc", 2, [2]int{2, 2}, [2]int{2, 5}},
		{"a value against an empty one", "K=abc", "K=", 2, [2]int{2, 5}, [2]int{2, 2}},
		{"an insertion in the middle stays narrow", "K=ab-cd", "K=ab-xx-cd", 2, [2]int{5, 5}, [2]int{5, 8}},
		{"nothing in common", "K=abc", "K=xyz", 2, [2]int{2, 5}, [2]int{2, 5}},
		{"a shared prefix only", "K=hello1", "K=hello22", 2, [2]int{2, 8}, [2]int{2, 9}},
		{"a change at the start of a token", "K=a.bcd", "K=a.xcd", 2, [2]int{4, 7}, [2]int{4, 7}},
		{"multi-byte runes count as one", "K=café", "K=cafè", 2, [2]int{2, 6}, [2]int{2, 6}},
		{"a multi-byte key is counted in runes", "ключ=1", "ключ=2", len("ключ="), [2]int{5, 6}, [2]int{5, 6}},
	}
	for _, tc := range cases {
		a, b := differingRanges(tc.a, tc.b, tc.key)
		if a != tc.wantA || b != tc.wantB {
			t.Errorf("%s: got %v %v, want %v %v", tc.name, a, b, tc.wantA, tc.wantB)
		}
	}
}

// The reversed part of a coloured line is a range of runes, so a multi-byte
// value is never cut inside a character and the line keeps all of its text.
func TestFileDiffWith_ColorKeepsMultiByteText(t *testing.T) {
	got := diffWith(t, "ключ=значение один\n", "ключ=значение два\n", DiffOptions{Color: true})
	if stripANSI(got) != "@@ -1,1 +1,1 @@\n-ключ=значение один\n+ключ=значение два\n" {
		t.Errorf("text changed by styling:\n%q", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("invalid UTF-8 in:\n%q", got)
	}
	if !strings.Contains(got, ansiMarkOn+"один"+ansiMarkOff) || !strings.Contains(got, ansiMarkOn+"два"+ansiMarkOff) {
		t.Errorf("the differing words are not marked:\n%q", got)
	}
}

// Text that is not valid UTF-8 is never rewritten by the highlight.
func TestFileDiffWith_ColorLeavesInvalidUTF8Alone(t *testing.T) {
	got := diffWith(t, "K=a\xffb\n", "K=a\xfeb\n", DiffOptions{Color: true})
	if stripANSI(got) != "@@ -1,1 +1,1 @@\n-K=a\xffb\n+K=a\xfeb\n" {
		t.Errorf("bytes changed: %q", got)
	}
}

// line() never trusts the range it is handed.
func TestLine_IgnoresBadRanges(t *testing.T) {
	o := DiffOptions{Color: true}
	for _, mark := range [][2]int{{0, 0}, {3, 1}, {0, 99}, {-1, 2}} {
		got := o.line(diffOp{tag: tagInsert, text: "abc"}, mark)
		if strings.Contains(got, ansiMarkOn) && mark != [2]int{-1, 2} {
			t.Errorf("mark %v applied: %q", mark, got)
		}
		if stripANSI(got) != "+abc" {
			t.Errorf("mark %v changed the text: %q", mark, got)
		}
	}
	if got := o.line(diffOp{tag: tagEqual, text: "ctx"}, [2]int{0, 2}); got != " ctx" {
		t.Errorf("context line coloured: %q", got)
	}
}

func TestWorkloadOf_Edges(t *testing.T) {
	for line, want := range map[string]string{
		"a/K=1":           "a",
		"my-app.v2_x/K=1": "my-app.v2_x",
		"a:b/K=1":         "", // not a Kubernetes name
		"a b/K=1":         "",
		"https://x/y":     "", // the "=" rule does not save a URL, the name rule does
		"K=https://x/y":   "",
		"":                "",
		"/":               "",
		"a/":              "a",
		"ключ/K=1":        "",
	} {
		if got := workloadOf(line); got != want {
			t.Errorf("workloadOf(%q) = %q, want %q", line, got, want)
		}
	}
}

// Styling must never change what a diff says. For random documents, in every
// mode, the coloured diff with its escape codes removed is the plain one —
// and the plain one is a well-formed unified diff: every line starts with a
// marker and every header's counts match the lines under it.
func TestFileDiffWith_StylingNeverChangesTheText(t *testing.T) {
	rnd := rand.New(rand.NewSource(11))
	words := []string{"a", "b", "K=1", "K=2", "wl/K=x", "wl/K=y", "  k: v", "  k: w", "ключ=é", "", "x=a/b", "- n: 1"}
	gen := func() string {
		lines := make([]string, rnd.Intn(12))
		for i := range lines {
			lines[i] = words[rnd.Intn(len(words))]
		}
		return strings.Join(lines, "\n")
	}
	for trial := 0; trial < 1500; trial++ {
		before, after := gen(), gen()
		for _, mode := range []Sections{SectionsNone, SectionsWorkload, SectionsYAML} {
			plain := diffWith(t, before, after, DiffOptions{Sections: mode, OldLabel: "o", NewLabel: "n"})
			colored := diffWith(t, before, after, DiffOptions{Sections: mode, OldLabel: "o", NewLabel: "n", Color: true})
			if stripANSI(colored) != plain {
				t.Fatalf("mode %d: styling changed the text\nbefore %q\nafter %q\nplain   %q\ncolored %q", mode, before, after, plain, colored)
			}
			if strings.Contains(plain, "\x1b") {
				t.Fatalf("mode %d: escape code in plain output: %q", mode, plain)
			}
			if mode != SectionsWorkload {
				checkUnifiedCounts(t, plain)
			}
		}
	}
}

var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+),(\d+) \+(\d+),(\d+) @@`)

// checkUnifiedCounts verifies each "@@ -a,b +c,d @@" header against its lines.
func checkUnifiedCounts(t *testing.T, out string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	var oldWant, newWant, oldGot, newGot int
	inHunk := false
	finish := func() {
		if inHunk && (oldGot != oldWant || newGot != newWant) {
			t.Fatalf("hunk counts -%d +%d, lines say -%d +%d in:\n%s", oldWant, newWant, oldGot, newGot, out)
		}
	}
	for i, l := range lines {
		if out == "" {
			return
		}
		if i < 2 && (strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ")) {
			continue
		}
		if m := hunkHeaderRE.FindStringSubmatch(l); m != nil {
			finish()
			inHunk = true
			oldWant, _ = strconv.Atoi(m[2])
			newWant, _ = strconv.Atoi(m[4])
			oldGot, newGot = 0, 0
			continue
		}
		switch {
		case strings.HasPrefix(l, " "):
			oldGot++
			newGot++
		case strings.HasPrefix(l, "-"):
			oldGot++
		case strings.HasPrefix(l, "+"):
			newGot++
		default:
			t.Fatalf("line %q has no marker in:\n%s", l, out)
		}
	}
	finish()
}

// The fuzzer's job is only to find a panic or a styling that alters text; the
// seeds run with every `go test`.
func FuzzFileDiffWith(f *testing.F) {
	f.Add("a: 1\nb: 2", "a: 1\nb: 3", uint8(0))
	f.Add("wl/K=1\nwl/L=2", "wl/K=2", uint8(1))
	f.Add("", "x: y", uint8(2))
	f.Add("ключ=а\r\nb", "ключ=б\r\nb", uint8(2))
	f.Add("a:\n\t- b: 1\n", "a:\n\t- b: 2\n", uint8(2))
	f.Fuzz(func(t *testing.T, before, after string, mode uint8) {
		o := DiffOptions{Sections: Sections(mode % 4), OldLabel: before, NewLabel: after}
		var plain, colored strings.Builder
		if err := FileDiffWith(&plain, before, after, o); err != nil {
			t.Fatal(err)
		}
		o.Color = true
		if err := FileDiffWith(&colored, before, after, o); err != nil {
			t.Fatal(err)
		}
		if stripANSI(colored.String()) != plain.String() {
			t.Fatalf("styling changed the text for %q -> %q", before, after)
		}
	})
}
