package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/prepare"
	"github.com/Stratio/flux-stratio/internal/ui"
)

// Everything a prepare step says is laid out like an app's review: a badge,
// a title, a hint and a table — and always [ACTION], since a step changes
// the cluster, is left to the operator, or was left undone.
func TestPrepareSections_AreActionsInTheReviewLayout(t *testing.T) {
	app := config.App{ID: "dg-hdfs-agent", Prepare: "prepare-dg-agent"}
	step := prepare.Find("prepare-dg-agent")
	for name, sec := range map[string]reviewSection{
		"ops":      prepareOpsSection(step, app, nil, false, false),
		"prepared": preparedSection(step),
		"skipped":  skippedPrepareSection(app, "stratio", "declined"),
		"summary":  skippedPreparesSection([]config.App{app})[0],
	} {
		if sec.Level != levelAction {
			t.Errorf("%s: level = %v, want [ACTION]", name, sec.Level)
		}
	}

	var out bytes.Buffer
	showSections(log.New(&out, false), skippedPrepareSection(app, "stratio", "--skip-prepare"))
	if !strings.Contains(out.String(), "[ACTION] prepare step \"prepare-dg-agent\" was NOT run (--skip-prepare)") ||
		!strings.Contains(out.String(), "\n  1 to act on") {
		t.Errorf("not laid out like a review:\n%s", out.String())
	}
}

// The question's hint depends on how the step runs: --dry-run asks nothing,
// --yes answers the question, otherwise it says what n does.
func TestPrepareOpsSection_HintFollowsHowTheStepRuns(t *testing.T) {
	app := config.App{ID: "dg-hdfs-agent"}
	step := prepare.Find("prepare-dg-agent")
	for _, tt := range []struct {
		dryRun, yes bool
		want, not   string
	}{
		{dryRun: true, want: "Dry run: nothing is asked or changed", not: "Answer n"},
		{yes: true, want: "Runs now (--yes)", not: "Answer n"},
		{want: "Answer n at the question to skip only this step", not: "--yes"},
	} {
		h := prepareOpsSection(step, app, nil, tt.dryRun, tt.yes).Hint
		if !strings.Contains(h, tt.want) || strings.Contains(h, tt.not) {
			t.Errorf("dryRun=%v yes=%v: hint %q, want %q and not %q", tt.dryRun, tt.yes, h, tt.want, tt.not)
		}
	}
}

// A statement is shown whole: lines are never cut to fit a column, however
// narrow the terminal.
func TestPrepareQuerySection_ShowsTheSQLWhole(t *testing.T) {
	step := prepare.Find("prepare-genai")
	sec := prepareQuerySection(step, config.App{ID: "genai"}, "stratio", "stratio-datastores/psql-0", 7, false)
	lines := renderReview([]reviewSection{sec}, reviewStyle{Width: 60})
	long := `    SET chain_params = replace(chain_params::text, 'dg-businessglossary-api', 'governance-businessglossary-api')::jsonb,`
	found := false
	for _, l := range lines {
		if l == long {
			found = true
		}
	}
	if !found {
		t.Errorf("a SQL line was cut or wrapped:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(flat(strings.Join(lines, " ")), "against pod stratio-datastores/psql-0") || strings.Contains(strings.Join(lines, "\n"), "<tenant>") {
		t.Errorf("pod missing or tenant placeholder left:\n%s", strings.Join(lines, "\n"))
	}
}

// A diff is plain text unless stdout is a terminal: captured or piped output
// (what these tests, a file or a pager get) carries no escape codes.
func TestDiffOptions_PlainWhenStdoutIsNotATerminal(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	o := diffOptions(cmd, "a", "b", ui.SectionsYAML)
	if o.Color || o.OldLabel != "a" || o.NewLabel != "b" || o.Sections != ui.SectionsYAML {
		t.Errorf("diffOptions = %+v", o)
	}
	if got := sectionsFor("virtualizer"); got != ui.SectionsWorkload {
		t.Errorf("a chart compares by workload, got %v", got)
	}
	if got := sectionsFor(""); got != ui.SectionsYAML {
		t.Errorf("a manifest compares by YAML path, got %v", got)
	}
}

// Colour needs a real terminal, no NO_COLOR (even when set empty, which is
// what os.LookupEnv reports) and a TERM that is not "dumb".
func TestColorWanted(t *testing.T) {
	for _, tc := range []struct {
		name       string
		isTerminal bool
		noColor    bool
		term       string
		want       bool
	}{
		{"a terminal", true, false, "xterm-256color", true},
		{"a terminal with no TERM set", true, false, "", true},
		{"piped or redirected", false, false, "xterm-256color", false},
		{"NO_COLOR set", true, true, "xterm-256color", false},
		{"a dumb terminal", true, false, "dumb", false},
		{"nothing wanted anywhere", false, true, "dumb", false},
	} {
		if got := colorWanted(tc.isTerminal, tc.noColor, tc.term); got != tc.want {
			t.Errorf("%s: colorWanted = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A command whose stdout is a file that is not a terminal (a redirect, a
// pipe) or a buffer never gets escape codes, whatever the environment says.
func TestStdoutColor_OffOutsideATerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	if stdoutColor(cmd) {
		t.Error("colour on a buffer")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	cmd.SetOut(w)
	if stdoutColor(cmd) {
		t.Error("colour on a pipe")
	}
}

// What the console diff says about its sides is stable: tests, docs and the
// habit of the operator rely on these words.
func TestDiffLabels(t *testing.T) {
	if desiredLabel != "desired state (tenant file as rendered)" {
		t.Errorf("desiredLabel = %q", desiredLabel)
	}
	if tenantFileCurrent != "tenant file (current)" || tenantFileWithNew != "tenant file (with the change)" {
		t.Errorf("tenant labels = %q / %q", tenantFileCurrent, tenantFileWithNew)
	}
}
