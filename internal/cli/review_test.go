package cli

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Stratio/flux-stratio/internal/diff"
)

func TestRenderReview_NothingToSayIsNoLines(t *testing.T) {
	if got := renderReview(nil, reviewStyle{}); got != nil {
		t.Errorf("renderReview(nil) = %q, want nothing", got)
	}
}

func TestRenderReview_SummaryCountsWhatToDo(t *testing.T) {
	rows := func(n int) [][]string {
		r := make([][]string, n)
		for i := range r {
			r[i] = []string{"x"}
		}
		return r
	}
	out := renderReview([]reviewSection{
		{Level: levelAction, Title: "a", Rows: rows(1)},
		{Level: levelReview, Title: "r", Rows: rows(12)},
		{Level: levelNote, Title: "n", Rows: rows(3)},
	}, reviewStyle{Verbose: true})
	if got := strings.TrimSpace(out[len(out)-1]); got != "1 to act on · 12 to review · 1 note(s)" {
		t.Errorf("summary = %q", got)
	}
}

func TestRenderReview_ColumnsAlignAndTheLastIsNotPadded(t *testing.T) {
	lines := renderTable([]string{"NAME", "GITOPS", "LIVE"},
		[][]string{{"A", "one", "uno"}, {"LONGER_NAME", "two", "dos"}}, "  ", 100, reviewStyle{})
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	col := func(line, cell string) int { return strings.Index(line, cell) }
	if col(lines[1], "one") != col(lines[2], "two") || col(lines[1], "uno") != col(lines[2], "dos") {
		t.Errorf("columns don't line up:\n%s", strings.Join(lines, "\n"))
	}
	for _, l := range lines {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("trailing padding in %q", l)
		}
	}
}

func TestRenderTable_FitsTheWidth(t *testing.T) {
	long := strings.Repeat("x", 120)
	for _, verbose := range []bool{false, true} {
		for _, width := range []int{60, 80, 100} {
			for _, l := range renderTable(nil, [][]string{{"NAME", long, long}}, "    ", width, reviewStyle{Verbose: verbose}) {
				if n := utf8.RuneCountInString(l); n > width {
					t.Errorf("verbose %v, width %d: line is %d wide: %q", verbose, width, n, l)
				}
			}
		}
	}
}

func TestCompactPair(t *testing.T) {
	tests := []struct{ name, a, b, wantA, wantB string }{
		{"short values come back whole", "dg-hdfs-agent", "hdfs1.stratio-datastores", "dg-hdfs-agent", "hdfs1.stratio-datastores"},
		{"a port that only one side has",
			"jdbc:postgresql://pgbouncer-postgreskeos-governance.keos-core:5432/postgreskeos?prepareThreshold=0",
			"jdbc:postgresql://pgbouncer-postgreskeos-governance.keos-core/postgreskeos?prepareThreshold=0",
			"…keos-core:5432/postgreskeos…", "…keos-core/postgreskeos…"},
		{"a renamed host in the middle",
			"https://governance-discovery-manager.keos-core:61000/discovery",
			"https://dg-discovery-manager.keos-core:61000/discovery",
			"https://governance-discovery…", "https://dg-discovery…"},
		{"nothing in common", strings.Repeat("a", 50), strings.Repeat("b", 50), strings.Repeat("a", 50), strings.Repeat("b", 50)},
	}
	for _, tt := range tests {
		gotA, gotB := compactPair(tt.a, tt.b)
		if gotA != tt.wantA || gotB != tt.wantB {
			t.Errorf("%s:\n got  %q / %q\n want %q / %q", tt.name, gotA, gotB, tt.wantA, tt.wantB)
		}
	}
}

// What -v adds to a review is a column, in the same table: without it the
// table has no WHY column, with it every row says why.
func TestChartReview_VerboseAddsAWhyColumn(t *testing.T) {
	review := chartReview{
		Unmapped: []diff.UnmappedDiff{
			{Name: "COMM_DS_URL", Rendered: "a", Live: "b", Reason: diff.UnmappedNoPath, Origin: diff.OriginComposed, Sources: []string{"dependencies.postgres.name"}},
			{Name: "DISCOVERY_BASE_URI", Rendered: "a", Live: "b", Reason: diff.UnmappedNoPath, Origin: diff.OriginFixed},
		},
		Source: "live cluster",
	}
	quiet := strings.Join(renderReview(review.sections(reviewStyle{}), reviewStyle{}), "\n")
	for _, not := range []string{"WHY", "built from", "fixed in the chart", "↳"} {
		if strings.Contains(quiet, not) {
			t.Errorf("without -v the review shows %q:\n%s", not, quiet)
		}
	}
	if !strings.Contains(quiet, "-v says why for each") {
		t.Errorf("without -v the hint should say where the reasons are:\n%s", quiet)
	}

	st := reviewStyle{Verbose: true, Width: 160}
	loud := strings.Join(renderReview(review.sections(st), st), "\n")
	for _, want := range []string{"NAME", "GITOPS", "LIVE", "WHY", "built from dependencies.postgres.name", "fixed in the chart: no value sets it"} {
		if !strings.Contains(loud, want) {
			t.Errorf("-v output missing %q:\n%s", want, loud)
		}
	}
	if strings.Contains(loud, "-v says why") {
		t.Errorf("-v still points at -v:\n%s", loud)
	}
	// One table: no sub-headings between the rows.
	if strings.Contains(loud, "·") && strings.Contains(loud, "assembled") {
		t.Errorf("the rows are split into sub-sections:\n%s", loud)
	}
}

// Notes come first and what needs action last, next to the question that
// follows; the order within a level is the order given.
func TestRenderReview_OrdersBySeverity(t *testing.T) {
	out := strings.Join(renderReview([]reviewSection{
		{Level: levelAction, Title: "act"},
		{Level: levelNote, Title: "note"},
		{Level: levelReview, Title: "first review"},
		{Level: levelReview, Title: "second review"},
	}, reviewStyle{Verbose: true}), "\n")
	last := -1
	for _, want := range []string{"[NOTE] note", "[REVIEW] first review", "[REVIEW] second review", "[ACTION] act"} {
		i := strings.Index(out, want)
		if i < 0 || i < last {
			t.Fatalf("%q out of order in:\n%s", want, out)
		}
		last = i
	}
}

func TestRenderReview_ColorOnlyWhenAsked(t *testing.T) {
	sections := []reviewSection{{Level: levelReview, Title: "t", Hint: "h"}}
	if out := strings.Join(renderReview(sections, reviewStyle{}), "\n"); strings.Contains(out, "\x1b[") {
		t.Errorf("styled output without Color: %q", out)
	}
	if out := strings.Join(renderReview(sections, reviewStyle{Color: true}), "\n"); !strings.Contains(out, "\x1b[1;33m[REVIEW]\x1b[0m") {
		t.Errorf("no colored badge with Color: %q", out)
	}
}

// A cell too long for its column is cut without -v and wrapped with it, so
// the whole text is there for whoever asked.
func TestRenderTable_LongCellsAreCutOrWrapped(t *testing.T) {
	value := strings.Repeat("a=b,", 30)
	rows := [][]string{{"selector", value, "short"}}

	quiet := renderTable(nil, rows, "  ", 60, reviewStyle{})
	if len(quiet) != 1 || !strings.Contains(quiet[0], "…") {
		t.Errorf("without -v the cell should be cut on one line: %q", quiet)
	}

	loud := renderTable(nil, rows, "  ", 60, reviewStyle{Verbose: true})
	if len(loud) < 2 {
		t.Fatalf("with -v the cell should wrap: %q", loud)
	}
	if got := strings.ReplaceAll(strings.Join(loud, ""), " ", ""); !strings.Contains(got, value[:60]) || strings.Contains(got, "…") {
		t.Errorf("a wrapped cell lost text:\n%s", strings.Join(loud, "\n"))
	}
	if !strings.Contains(loud[0], "short") {
		t.Errorf("the other cells belong on the first line: %q", loud[0])
	}
}

// The WHY column gets room before the value columns shrink to nothing.
func TestRenderTable_WhyKeepsRoom(t *testing.T) {
	why := strings.Repeat("built from some.values.path, ", 6)
	lines := renderTable([]string{"NAME", "GITOPS", "LIVE", "WHY"},
		[][]string{{"COMM_DS_URL", strings.Repeat("g", 50), strings.Repeat("l", 50), why}}, "  ", 100, reviewStyle{Verbose: true})
	for _, l := range lines {
		if utf8.RuneCountInString(l) > 100 {
			t.Errorf("line over 100 columns: %q", l)
		}
	}
	idx := strings.Index(lines[0], "WHY")
	if idx < 0 || 100-idx < 24 {
		t.Errorf("the WHY column starts at %d of 100, leaving it under 24 columns:\n%s", idx, strings.Join(lines, "\n"))
	}
}

// Notes have nothing to act on: without -v they are one line saying what
// there is, with the reviews and actions in full; with -v they are listed.
func TestRenderReview_NotesAreOneLineUnlessVerbose(t *testing.T) {
	sections := []reviewSection{
		{Level: levelNote, Title: "carried resources", Rows: [][]string{{"requests.memory", "2048M"}}},
		{Level: levelNote, Title: "2 value(s) excluded by the catalog"},
		{Level: levelReview, Title: "needs a look", Rows: [][]string{{"VAR"}}},
	}
	quiet := strings.Join(renderReview(sections, reviewStyle{}), "\n")
	for _, want := range []string{"[NOTE] carried resources; 2 value(s) excluded by the catalog", "(-v lists them)", "[REVIEW] needs a look", "VAR"} {
		if !strings.Contains(quiet, want) {
			t.Errorf("output missing %q:\n%s", want, quiet)
		}
	}
	if strings.Contains(quiet, "requests.memory") {
		t.Errorf("a note's rows were listed without -v:\n%s", quiet)
	}
	lines := renderReview(sections, reviewStyle{})
	if got := strings.TrimSpace(lines[len(lines)-1]); got != "1 to review" {
		t.Errorf("summary = %q, want it to leave out the hidden notes", got)
	}

	loud := strings.Join(renderReview(sections, reviewStyle{Verbose: true}), "\n")
	for _, want := range []string{"[NOTE] carried resources", "requests.memory", "[NOTE] 2 value(s) excluded by the catalog"} {
		if !strings.Contains(loud, want) {
			t.Errorf("-v output missing %q:\n%s", want, loud)
		}
	}
	if strings.Contains(loud, "-v lists them") {
		t.Errorf("-v still points at -v:\n%s", loud)
	}
}

// Only notes: one line, and no empty summary after it.
func TestRenderReview_OnlyNotesWithoutVerbose(t *testing.T) {
	lines := renderReview([]reviewSection{{Level: levelNote, Title: "carried"}}, reviewStyle{})
	if got := strings.Join(lines, "\n"); !strings.Contains(got, "[NOTE] carried") || strings.Count(got, "\n") > 2 {
		t.Errorf("output = %q, want just the one note line", got)
	}
}

// Whatever is cut, what differs stays: the two sides never compact to the
// same text.
func TestCompactPair_StillTellsThemApart(t *testing.T) {
	a := "https://governance-businessglossary-api.keos-core:60000/dictionary/user/catalog/v1/serviceRegistry"
	b := "https://dg-businessglossary-api.keos-core:60000/dictionary/user/catalog/v1/serviceRegistry"
	ca, cb := compactPair(a, b)
	if ca == cb || !strings.Contains(ca, "governance") || !strings.Contains(cb, "dg-") {
		t.Errorf("compact = %q / %q", ca, cb)
	}
}

// One workload's findings don't repeat its name on every row; several
// workloads' do.
func TestChartReview_NamesTheWorkloadOnlyWhenThereAreSeveral(t *testing.T) {
	one := sectionsOutput(chartReview{
		Unmapped: []diff.UnmappedDiff{{Workload: "dg-hdfs-agent", Name: "A", Rendered: "1", Live: "2", Reason: diff.UnmappedNoPath}},
		Source:   "live cluster",
	}.sections(reviewStyle{}))
	if strings.Contains(one, "dg-hdfs-agent/A") {
		t.Errorf("a single workload's rows carry its name:\n%s", one)
	}
	two := sectionsOutput(chartReview{
		Unmapped: []diff.UnmappedDiff{
			{Workload: "rocket", Name: "A", Rendered: "1", Live: "2", Reason: diff.UnmappedNoPath},
			{Workload: "rocket-debug", Name: "A", Rendered: "1", Live: "2", Reason: diff.UnmappedNoPath},
		},
		Source: "live cluster",
	}.sections(reviewStyle{}))
	if !strings.Contains(two, "rocket/A") || !strings.Contains(two, "rocket-debug/A") {
		t.Errorf("several workloads' rows don't say which is which:\n%s", two)
	}
}

// Workload settings name their workload as soon as the review spans several,
// in every table, so a row is never ambiguous.
func TestChartReview_SettingsNameTheirWorkloadInEveryTable(t *testing.T) {
	out := sectionsOutput(chartReview{
		SpecDiffs: []diff.SpecDiff{
			{Workload: "rocket", Field: "replicas", Rendered: "1", Live: "2", Severity: diff.SpecInfo},
			{Workload: "rocket-debug", Field: "image", Rendered: "a:1", Live: "a:2", Severity: diff.SpecRisk},
		},
		Source: "live cluster",
	}.sections(reviewStyle{}))
	for _, want := range []string{"WORKLOAD", "rocket ", "rocket-debug"} {
		if strings.Count(out, want) < 2 && want == "WORKLOAD" {
			t.Errorf("each table should have a WORKLOAD column:\n%s", out)
		}
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	single := sectionsOutput(chartReview{
		SpecDiffs: []diff.SpecDiff{{Workload: "rocket", Field: "replicas", Rendered: "1", Live: "2", Severity: diff.SpecRisk}},
		Source:    "live cluster",
	}.sections(reviewStyle{}))
	if strings.Contains(single, "WORKLOAD") {
		t.Errorf("a review about one workload has a WORKLOAD column:\n%s", single)
	}
}

func TestCompressPaths(t *testing.T) {
	got := compressPaths([]string{
		"dependencies.postgres", "dependencies.postgres.host", "dependencies.postgres.name", "dependencies.postgres.port",
		"dependencies.postgres.pgBouncer.enabled", "dependencies.postgres.pgBouncer.name", "dependencies.connectors.config.hdfs.connectorJar", "top",
	})
	want := "dependencies.postgres.(host, name, port), dependencies.postgres.pgBouncer.(enabled, name), dependencies.connectors.config.hdfs.connectorJar, top"
	if got != want {
		t.Errorf("compressPaths =\n %s\nwant\n %s", got, want)
	}
}

func TestWrapAnywhere_LeavesNoStraySpaces(t *testing.T) {
	for _, l := range wrapAnywhere("a.b, c.d, e.f, g.h, i.j, k.l, m.n, o.p", 14) {
		if l != strings.TrimSpace(l) {
			t.Errorf("line %q has a stray space", l)
		}
	}
}
