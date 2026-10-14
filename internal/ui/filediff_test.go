package ui

import (
	"math/rand"
	"strings"
	"testing"
)

func TestFileDiff(t *testing.T) {
	cases := []struct {
		name, before, after, want string
	}{
		{
			name:   "identical documents produce no output",
			before: "a\nb\nc\n",
			after:  "a\nb\nc\n",
			want:   "",
		},
		{
			name:   "single line changed in the middle",
			before: "a\nb\nc\n",
			after:  "a\nX\nc\n",
			want:   "@@ -1,3 +1,3 @@\n a\n-b\n+X\n c\n",
		},
		{
			name:   "two changes far apart merge into one hunk when contexts touch",
			before: join("l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10"),
			after:  join("l1", "X2", "l3", "l4", "l5", "l6", "l7", "l8", "X9", "l10"),
			want:   "@@ -1,10 +1,10 @@\n l1\n-l2\n+X2\n l3\n l4\n l5\n l6\n l7\n l8\n-l9\n+X9\n l10\n",
		},
		{
			name:   "changes at both edges keep only the available context",
			before: join("l1", "l2", "l3", "l4", "l5", "l6", "l7"),
			after:  join("X1", "l2", "l3", "l4", "l5", "l6", "X7"),
			want:   "@@ -1,7 +1,7 @@\n-l1\n+X1\n l2\n l3\n l4\n l5\n l6\n-l7\n+X7\n",
		},
		{
			name:   "pure insertion",
			before: "a\nb\n",
			after:  "a\nX\nY\nb\n",
			want:   "@@ -1,2 +1,4 @@\n a\n+X\n+Y\n b\n",
		},
		{
			name:   "pure deletion",
			before: "a\nb\nc\nd\n",
			after:  "a\nd\n",
			want:   "@@ -1,4 +1,2 @@\n a\n-b\n-c\n d\n",
		},
		{
			name:   "missing trailing newline does not add a spurious empty line",
			before: "a\nb",
			after:  "a\nX",
			want:   "@@ -1,2 +1,2 @@\n a\n-b\n+X\n",
		},
		{
			name:   "both documents empty",
			before: "",
			after:  "",
			want:   "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf strings.Builder
			if err := FileDiff(&buf, c.before, c.after); err != nil {
				t.Fatalf("FileDiff returned error: %v", err)
			}
			if got := buf.String(); got != c.want {
				t.Errorf("FileDiff(%q, %q):\n got:  %q\n want: %q", c.before, c.after, got, c.want)
			}
		})
	}
}

func TestFileDiff_TwoDistantChangesStaySeparateHunks(t *testing.T) {
	// 30 unchanged lines between the two edits is well beyond 2*context (6),
	// so this must produce two independent hunks, not one merged block.
	lines := make([]string, 0, 32)
	lines = append(lines, "start")
	for i := 0; i < 30; i++ {
		lines = append(lines, "same")
	}
	lines = append(lines, "end")
	before := strings.Join(lines, "\n") + "\n"

	changed := make([]string, len(lines))
	copy(changed, lines)
	changed[0] = "START"
	changed[len(changed)-1] = "END"
	after := strings.Join(changed, "\n") + "\n"

	var buf strings.Builder
	if err := FileDiff(&buf, before, after); err != nil {
		t.Fatalf("FileDiff returned error: %v", err)
	}
	got := buf.String()
	if n := strings.Count(got, "@@"); n != 4 { // two "@@ ... @@" headers = 4 occurrences of "@@"
		t.Errorf("expected 2 separate hunks (4 \"@@\" markers), got %d in:\n%s", n, got)
	}
}

func join(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// replay rebuilds both documents from an edit script: the old one is every
// line that is not an insertion, the new one every line that is not a deletion.
func replay(ops []diffOp) (oldDoc, newDoc []string) {
	for _, op := range ops {
		if op.tag != tagInsert {
			oldDoc = append(oldDoc, op.text)
		}
		if op.tag != tagDelete {
			newDoc = append(newDoc, op.text)
		}
	}
	return oldDoc, newDoc
}

func sameLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Whatever the two documents are, the script turns the first into the second:
// replaying it yields both back, exactly. Random documents over a small
// alphabet make long shared runs, repeated lines and empty sides likely.
func TestDiffLines_ReplaysBothDocuments(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	gen := func() []string {
		out := make([]string, rnd.Intn(14))
		for i := range out {
			out[i] = string(rune('a' + rnd.Intn(4)))
		}
		return out
	}
	for trial := 0; trial < 3000; trial++ {
		a, b := gen(), gen()
		gotA, gotB := replay(diffLines(a, b))
		if !sameLines(gotA, a) || !sameLines(gotB, b) {
			t.Fatalf("a=%q b=%q: replay gave %q and %q", a, b, gotA, gotB)
		}
	}
}

// The script is minimal for what the peeling leaves: a single changed line in
// a long document is one deletion and one insertion, however long it is.
func TestDiffLines_OnlyTheChangeDiffers(t *testing.T) {
	a, b := make([]string, 500), make([]string, 500)
	for i := range a {
		a[i], b[i] = strings.Repeat("x", i%7), strings.Repeat("x", i%7)
	}
	a[250], b[250] = "old", "new"
	changed := 0
	for _, op := range diffLines(a, b) {
		if op.tag != tagEqual {
			changed++
		}
	}
	if changed != 2 {
		t.Errorf("%d changed ops, want 2", changed)
	}
}

func TestDiffLines_EmptySides(t *testing.T) {
	for name, tc := range map[string]struct{ a, b []string }{
		"both empty": {}, "old empty": {b: []string{"x", "y"}}, "new empty": {a: []string{"x", "y"}},
	} {
		gotA, gotB := replay(diffLines(tc.a, tc.b))
		if !sameLines(gotA, tc.a) || !sameLines(gotB, tc.b) {
			t.Errorf("%s: replay gave %q and %q", name, gotA, gotB)
		}
	}
}

// Two long documents that share nothing must not ask for a table of
// gigabytes: past the cap the middle is all deleted, then all inserted — still
// a correct script — while the shared ends are kept as they are.
func TestDiffLines_HugeInputsFallBackToABlock(t *testing.T) {
	defer func(old int) { maxLCSCells = old }(maxLCSCells)
	maxLCSCells = 50

	a := []string{"head", "a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "tail"}
	b := []string{"head", "b1", "b2", "b3", "b4", "b5", "b6", "b7", "b8", "tail"}
	ops := diffLines(a, b)
	gotA, gotB := replay(ops)
	if !sameLines(gotA, a) || !sameLines(gotB, b) {
		t.Fatalf("fallback does not replay: %q / %q", gotA, gotB)
	}
	want := "=head -a1 -a2 -a3 -a4 -a5 -a6 -a7 -a8 +b1 +b2 +b3 +b4 +b5 +b6 +b7 +b8 =tail"
	var got []string
	for _, op := range ops {
		got = append(got, map[diffTag]string{tagEqual: "=", tagDelete: "-", tagInsert: "+"}[op.tag]+op.text)
	}
	if strings.Join(got, " ") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, " "), want)
	}

	// And a real 6000x6000 pair with nothing in common finishes at once.
	maxLCSCells = 16_000_000
	big1, big2 := make([]string, 6000), make([]string, 6000)
	for i := range big1 {
		big1[i], big2[i] = "a"+strings.Repeat("-", i%50)+string(rune('0'+i%10)), "b"+strings.Repeat("-", i%50)
	}
	gotA, gotB = replay(diffLines(big1, big2))
	if !sameLines(gotA, big1) || !sameLines(gotB, big2) {
		t.Error("the 6000-line fallback does not replay")
	}
}

// Fuzzed documents split into lines: the script always replays both of them.
func FuzzDiffLines(f *testing.F) {
	f.Add("a\nb\nc", "a\nX\nc")
	f.Add("", "x")
	f.Add("same\nsame\nsame", "same")
	f.Fuzz(func(t *testing.T, before, after string) {
		a, b := splitLines(before), splitLines(after)
		gotA, gotB := replay(diffLines(a, b))
		if !sameLines(gotA, a) || !sameLines(gotB, b) {
			t.Fatalf("replay of %q -> %q gave %q / %q", before, after, gotA, gotB)
		}
	})
}
