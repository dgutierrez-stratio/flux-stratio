package ui

import (
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
