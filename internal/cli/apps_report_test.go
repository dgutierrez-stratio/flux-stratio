package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
	"github.com/Stratio/flux-stratio/internal/ui"
)

// reviewOutput is what reportChartReview prints for r with -v, undecorated:
// notes listed and values whole.
func reviewOutput(r chartReview) string {
	var buf bytes.Buffer
	reportChartReview(log.New(&buf, true).WithTerminal(maxWidth, false), r)
	// A long cell wraps onto further lines: compare the text, not the breaks.
	return strings.Join(strings.Fields(buf.String()), " ")
}

// sectionsOutput is what a list of sections renders to, undecorated, with
// the notes listed.
func sectionsOutput(sections []reviewSection) string {
	return strings.Join(renderReview(sections, reviewStyle{Verbose: true}), "\n")
}

func TestReportChartReview(t *testing.T) {
	out := reviewOutput(chartReview{
		Unmapped: []diff.UnmappedDiff{
			{Name: "VAULT_ROLE", Rendered: "genai_genai-ui", Live: "legacy", Reason: diff.UnmappedAmbiguous,
				Candidates: []string{"genaiApi.general.identity.approlename", "genaiUi.general.identity.approlename"}},
			{Workload: "rocket", Name: "TENANT", Rendered: "a", Live: "b", Reason: diff.UnmappedConflict, Candidates: []string{"rocketCommon.tenant"}},
			{Name: "KERBEROS_REALM_NAME", Rendered: "eosdev.int", Live: "EOSDEV.INT", Reason: diff.UnmappedConflict,
				Candidates: []string{"cluster.domain"}, Shared: []string{"PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN=eosdev.int"}},
			{Name: "HARDCODED", Rendered: "1", Live: "2", Reason: diff.UnmappedInline},
		},
		Missing: []string{"Deployment stratio-genai/genai-ui"},
		LiveOnly: []diff.LiveOnlyVar{
			{Workload: "rocket-debug", Name: "LEGACY_FLAG", Live: "true"},
			{Name: "EXTRA_JARS", Live: "a.jar,b.jar"},
		},
		Source: "backup",
	})
	for _, want := range []string{
		"[REVIEW] 1 rendered workload(s) not found in the backup",
		"Deployment stratio-genai/genai-ui",
		"[REVIEW] 4 variable(s) differ and can't be written to the patch",
		// one table: the reason for each is a column, not a sub-heading
		"NAME GITOPS LIVE WHY",
		"could be set by genaiApi.general.identity.approlename, genaiUi.general.identity.approlename",
		"its path, cluster.domain, also sets PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN=eosdev.int",
		"set in the container's own env, not through values",
		// two workloads involved: every row names its own
		"rocket/TENANT", "rocket-debug/LEGACY_FLAG",
		"[REVIEW] 2 variable(s) in the backup aren't rendered by the chart",
		"EXTRA_JARS", "a.jar,b.jar",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "⚠") {
		t.Errorf("a review is laid out by section, not as ⚠ lines:\n%s", out)
	}
}

func TestReportChartReview_ExcludedIsInformationOnly(t *testing.T) {
	review := chartReview{
		Excluded: []diff.ExcludedDiff{
			{Workload: "genai-api", Name: "VAULT_ROLE", Rendered: "genai_genai-api", Live: "legacy-role", Path: "genaiApi.general.identity.approlename"},
			{Name: "ROLE", Rendered: "a", Live: "b", Path: "x.role"},
		},
		Source: "backup",
	}
	if review.hasWarnings() {
		t.Error("hasWarnings() = true with only Excluded set: it must not make --yes stop")
	}

	out := reviewOutput(review)
	for _, want := range []string{
		"[NOTE] 2 value(s) excluded by the catalog",
		"VAULT_ROLE", "genai_genai-api", "legacy-role", "genaiApi.general.identity.approlename",
		"x.role",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[REVIEW]") || strings.Contains(out, "[ACTION]") {
		t.Errorf("excluded differences were presented as something to review:\n%s", out)
	}
}

func TestReportChartReview_NothingToReportIsSilent(t *testing.T) {
	var buf bytes.Buffer
	reportChartReview(log.New(&buf, false), chartReview{Source: "live cluster"})
	if buf.Len() != 0 {
		t.Errorf("output = %q, want nothing", buf.String())
	}
}

// TestReportNoChange_UnmappedIsNotNoDifferences pins the regression the
// Go port had: differences no patch could carry were dropped, and the
// diff reported "no differences".
func TestReportNoChange_UnmappedIsNotNoDifferences(t *testing.T) {
	var buf bytes.Buffer
	reportNoChange(log.New(&buf, false), false, 0, chartReview{Unmapped: []diff.UnmappedDiff{{Name: "X"}}})
	if out := buf.String(); strings.Contains(out, "no differences") || !strings.Contains(out, "need manual review") {
		t.Errorf("output = %q, want a manual-review warning, not \"no differences\"", out)
	}
}

func TestLegacyAgentSection(t *testing.T) {
	app := config.App{Object: "opensearch1-gosec-agent"}
	if got := legacyAgentSection(app, 0); got != nil {
		t.Errorf("no legacy patch: %v, want nothing", got)
	}
	out := sectionsOutput(legacyAgentSection(app, 1))
	for _, want := range []string{"opensearch1-gosec-agent", "config.agent.patches", "move or remove"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %s", want, out)
		}
	}
}

func TestUnresolvedDepsSection(t *testing.T) {
	app := config.App{Object: "rocket", Kustomization: "apps-rocket"}
	out := sectionsOutput(unresolvedDepsSection(app, []tenantfile.UnresolvedDependency{
		{Key: "dgAgent", Name: "dg-agent", Declared: []string{"dg-hdfs-agent"}},
		{Key: "virtualizer", Name: "virtualizer"},
	}))
	for _, want := range []string{
		"rocket depends on 2 entr(y/ies) the tenant file doesn't declare",
		"Flux holds apps-rocket back",
		"dgAgent", "dg-agent", "dg-hdfs-agent",
		"virtualizer", "none declared",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestConfirmWarnings: warnings stop the app until the operator says to
// go on — a decline is errNotConfirmed, never a quiet success — and
// --yes doesn't answer for them: only --accept-warnings does.
func TestConfirmWarnings(t *testing.T) {
	app := config.App{Name: "Rocket rocket"}
	tests := []struct {
		name         string
		warned       bool
		flags        migrateFlags
		stdin        string
		wantErr      string
		notConfirmed bool
		prompts      bool
	}{
		{name: "no warnings"},
		{name: "accepted", warned: true, stdin: "y\n", prompts: true},
		{name: "declined", warned: true, stdin: "n\n", prompts: true, wantErr: "not confirmed", notConfirmed: true},
		{name: "dry run never asks", warned: true, flags: migrateFlags{dryRun: true}},
		{name: "yes stops on warnings", warned: true, flags: migrateFlags{yes: true}, wantErr: "--accept-warnings"},
		{name: "yes with accept-warnings goes on", warned: true, flags: migrateFlags{yes: true, acceptWarnings: true}},
		{name: "yes without warnings goes on", flags: migrateFlags{yes: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetIn(strings.NewReader(tt.stdin))
			cmd.SetErr(&stderr)
			err := confirmWarnings(cmd, app, tt.warned, tt.flags, "Show the patch")
			if tt.wantErr == "" && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
			if got := errors.Is(err, errNotConfirmed); got != tt.notConfirmed {
				t.Errorf("errors.Is(err, errNotConfirmed) = %v, want %v", got, tt.notConfirmed)
			}
			if asked := strings.Contains(stderr.String(), "Review the warnings above"); asked != tt.prompts {
				t.Errorf("prompted = %v, want %v:\n%s", asked, tt.prompts, stderr.String())
			}
		})
	}
}

func TestReportChartReview_SpecDiffs(t *testing.T) {
	review := chartReview{
		SpecDiffs: []diff.SpecDiff{
			{Workload: "dg-hdfs-agent", Container: "agent", Field: "resources.limits.memory", Rendered: "2048Mi", Live: "4096M", Severity: diff.SpecRisk},
			{Workload: "intelligence", Container: "intelligence", Field: "resources.limits.memory", Rendered: "512M", Severity: diff.SpecRisk},
			{Workload: "rocket", Container: "rocket", Field: "resources.limits.cpu", Rendered: "1", Live: "4", Severity: diff.SpecInfo},
		},
		Source: "live cluster",
	}
	out := reviewOutput(review)
	for _, want := range []string{
		"[REVIEW] 2 workload setting(s) differ from the live cluster and can break the migrated workload",
		"dg-hdfs-agent/agent", "resources.limits.memory", "2048Mi", "4096M",
		"intelligence", "512M", "unset",
		"[NOTE] 1 other workload setting(s) differ from the live cluster",
		"rocket", "resources.limits.cpu",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !review.hasWarnings() {
		t.Error("hasWarnings() = false with risky spec diffs, want true: --yes must stop")
	}
}

// An immutable selector is something to act on, shown whole and apart from
// the other settings.
func TestReportChartReview_SelectorNeedsAction(t *testing.T) {
	out := reviewOutput(chartReview{
		SpecDiffs: []diff.SpecDiff{{
			Workload: "dg-hdfs-agent", Field: diff.SelectorField, Severity: diff.SpecRisk,
			Rendered: "app.kubernetes.io/component=dg-agent,app.kubernetes.io/instance=dg-hdfs-agent",
			Live:     "cct.stratio.com/application_id=dg-hdfs-agent.stratio-datastores",
		}},
		Source: "live cluster",
	})
	for _, want := range []string{
		"[ACTION] 1 workload(s) can't be updated in place: their selector is immutable",
		"delete it by hand before Flux installs the release",
		"component=dg-agent,instance=dg-hdfs-agent",
		"cct.stratio.com/application_id=dg-hdfs-agent.stratio-datastores",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// The [ACTION] names the legacy object that blocks (not the rendered
// workload's name) and what to do about it, by the app's prepare step.
func TestReportChartReview_SelectorActionNamesTheLegacyObject(t *testing.T) {
	selector := diff.SpecDiff{
		Workload: "eureka-agent", Field: diff.SelectorField, Severity: diff.SpecRisk,
		LiveRef:  "Deployment stratio-datastores/eureka-agent",
		Rendered: "app.kubernetes.io/name=eureka-agent",
		Live:     "cct.stratio.com/application_id=eureka-agent.stratio-datastores",
	}
	for name, tt := range map[string]struct {
		prepare string
		want    []string
	}{
		"no step":                       {"", []string{"No prepare step exists for this app", "delete it by hand before Flux installs the release", "stratio-datastores/eureka-agent"}},
		"a step that doesn't delete it": {"prepare-datamarket-agent", []string{`Prepare step "prepare-datamarket-agent" doesn't delete it`, "delete it by hand"}},
		"an unknown step is no one":     {"nope", []string{"No prepare step exists for this app"}},
	} {
		t.Run(name, func(t *testing.T) {
			out := flat(reviewOutput(chartReview{SpecDiffs: []diff.SpecDiff{selector}, Prepare: tt.prepare, Source: "live cluster"}))
			if !strings.Contains(out, "[ACTION] 1 workload(s) can't be updated in place") {
				t.Errorf("no [ACTION]:\n%s", out)
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "Only an app with a prepare step does it for you") {
				t.Errorf("the old, false sentence is back:\n%s", out)
			}
		})
	}
}

// A legacy workload that is another object than the one the chart renders is
// a [NOTE], never an [ACTION]: it keeps running next to the new one.
func TestReportChartReview_LegacyKeepsRunningIsANote(t *testing.T) {
	note := diff.SpecDiff{
		Workload: "psql-gosec-agent", Field: diff.LegacyRunsField, Severity: diff.SpecInfo,
		LiveRef: "Deployment stratio-datastores/psql-agent", Live: "2", Rendered: "Deployment stratio-datastores/psql-gosec-agent",
	}
	review := chartReview{SpecDiffs: []diff.SpecDiff{note}, Source: "live cluster"}
	out := flat(reviewOutput(review))
	for _, want := range []string{
		"[NOTE] 1 legacy workload(s) keep running next to the new one",
		"stratio-datastores/psql-agent", "Deployment stratio-datastores/psql-gosec-agent",
		"scaled down or removed by hand",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[ACTION]") || strings.Contains(out, "other workload setting(s)") || review.hasWarnings() {
		t.Errorf("a legacy workload that keeps running is no action, no other setting and no warning:\n%s", out)
	}

	review.Prepare = "prepare-datamarket-agent"
	if out := flat(reviewOutput(review)); !strings.Contains(out, `prepare step "prepare-datamarket-agent"`) || strings.Contains(out, "by hand") {
		t.Errorf("an app with a step is told which one deals with it:\n%s", out)
	}
}

func TestReportChartReview_InformationalSpecDiffsAreNoWarning(t *testing.T) {
	review := chartReview{
		SpecDiffs: []diff.SpecDiff{{Workload: "rocket", Field: "resources.limits.cpu", Rendered: "1", Live: "4", Severity: diff.SpecInfo}},
		Source:    "backup",
	}
	if review.hasWarnings() {
		t.Error("hasWarnings() = true for informational spec diffs only, want false")
	}
}

func TestReportNoChange_SpecDiffsAreNotNoDifferences(t *testing.T) {
	var buf bytes.Buffer
	reportNoChange(log.New(&buf, false), false, 0, chartReview{
		SpecDiffs: []diff.SpecDiff{{Workload: "a", Field: "image", Rendered: "r:1", Live: "r:2", Severity: diff.SpecRisk}},
	})
	if out := buf.String(); strings.Contains(out, "no differences\n") || !strings.Contains(out, "workload settings listed above differ") {
		t.Errorf("output = %q, want it to point at the workload settings, not claim no differences", out)
	}
}

func TestReportChartReview_NamesTheListShadowingAChartDefault(t *testing.T) {
	out := reviewOutput(chartReview{
		LiveOnly: []diff.LiveOnlyVar{{Workload: "rocket", Name: "ROCKET_POD_NAME", Live: "rocket", ShadowedBy: "controllers.rocket.containers.rocket.env"}},
		Source:   "live cluster",
	})
	for _, want := range []string{"ROCKET_POD_NAME", "the chart's default is hidden by the env list at controllers.rocket.containers.rocket.env"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestReportChartReview_SaysWhichProbeVariablesTheChartCarriesNatively(t *testing.T) {
	out := reviewOutput(chartReview{
		Native: []diff.LiveOnlyVar{{Workload: "app", Name: "READINESS_PROBE_PERIOD_SECONDS", Live: "30", NativeField: "readinessProbe.periodSeconds", NativeValue: "30"}},
		LiveOnly: []diff.LiveOnlyVar{
			{Workload: "app", Name: "READINESS_PROBE_FAILURE_THRESHOLD", Live: "3", NativeField: "readinessProbe.failureThreshold", NativeValue: "1"},
			{Workload: "app", Name: "NOT_USED", Live: ""},
		},
		Source: "live cluster",
	})
	for _, want := range []string{
		"1 variable(s) in the live cluster carried natively by the chart",
		"the chart sets readinessProbe.periodSeconds natively, to the same value",
		"2 variable(s) in the live cluster aren't rendered by the chart",
		"the chart sets readinessProbe.failureThreshold natively, to 1 (live 3): a real difference",
		"a placeholder name the descriptor gives to several unrelated parameters",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSizeSection(t *testing.T) {
	fleet := t.TempDir()
	path := tenantfile.Path(fleet, "eosdev", "stratio")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	tenant := "apiVersion: fluxcd.controlplane.io/v1\nkind: ResourceSetInputProvider\nmetadata:\n  name: stratio\nspec:\n  type: Static\n  defaultValues:\n    size: S\n    components: {}\n"
	if err := os.WriteFile(path, []byte(tenant), 0o644); err != nil {
		t.Fatal(err)
	}
	repos := config.RepoPaths{Fleet: fleet}

	for name, tt := range map[string]struct{ live, want string }{
		"live runs another size": {"M", "tenant size S, but live rocket reports CUSTOMER_SIZE=M"},
		"the same size":          {"S", ""},
		"live reports no size":   {"", ""},
	} {
		got := sizeSection(repos, "eosdev", "stratio", "rocket", tt.live)
		if tt.want == "" && got != nil || !strings.Contains(sectionsOutput(got), tt.want) {
			t.Errorf("%s: sections = %v, want %q", name, got, tt.want)
		}
	}
}

func TestReportChartReview_EnvVsLimit(t *testing.T) {
	review := chartReview{
		SpecDiffs: []diff.SpecDiff{{Workload: "dg-hdfs-agent", Container: "agent", Field: diff.EnvVsLimitField + "JAVA_OPTS", Rendered: "2048Mi", Live: "-Xmx3072m", Severity: diff.SpecRisk}},
		Source:    "live cluster",
	}
	out := reviewOutput(review)
	for _, want := range []string{
		"[ACTION] 1 variable(s) ask for more memory than the container's limit",
		"dg-hdfs-agent", "JAVA_OPTS", "-Xmx3072m", "2048Mi",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !review.hasWarnings() {
		t.Error("hasWarnings() = false, want the memory request to stop --yes")
	}
}

// apps diff compares the tenant file as it is: there is no flag to apply (or
// leave out) its patch, so the comparison never needs a mode.
func TestAppsDiff_HasNoPatchModeFlags(t *testing.T) {
	cmd := newAppsDiffCommand()
	for _, name := range []string{"patched", "unpatched"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Errorf("apps diff has --%s: the tenant file's patch is applied by default, with no flag", name)
		}
	}
}

// With the tenant patch applied and nothing left to patch, diff says so
// plainly; "nothing to migrate" is a migrate message.
func TestReportNoChange_NothingLeftAfterThePatch(t *testing.T) {
	var buf bytes.Buffer
	reportNoChange(log.New(&buf, false), false, 0, chartReview{})
	if out := buf.String(); !strings.Contains(out, "no differences") || strings.Contains(out, "nothing to migrate") {
		t.Errorf("output = %q, want a plain \"no differences\"", out)
	}
}

// A variable with no .Values path says, in its WHY column, what the chart's
// line for it is made of; a composed one names the values it is built from.
func TestReportChartReview_NoPathSaysWhatTheChartLineIs(t *testing.T) {
	out := reviewOutput(chartReview{
		Unmapped: []diff.UnmappedDiff{
			{Name: "DISCOVERY_BASE_URI", Rendered: "a", Live: "b", Reason: diff.UnmappedNoPath, Origin: diff.OriginFixed},
			{Name: "VAULT_ROLE", Rendered: "a", Live: "b", Reason: diff.UnmappedNoPath, Origin: diff.OriginRelease},
			{Name: "COMM_DS_URL", Rendered: "a", Live: "b", Reason: diff.UnmappedNoPath, Origin: diff.OriginComposed,
				Sources: []string{"dependencies.postgres.name", "dependencies.postgres.port"}},
			{Name: "OPAQUE", Rendered: "a", Live: "b", Reason: diff.UnmappedNoPath, Origin: diff.OriginComposed},
			{Name: "MYSTERY", Rendered: "a", Live: "b", Reason: diff.UnmappedNoPath},
		},
		Source: "live cluster",
	})
	for _, want := range []string{
		"fixed in the chart: no value sets it",
		"follows the release's name or namespace",
		"built from dependencies.postgres.(name, port)",
		"built by a helper this scan couldn't follow",
		"no chart config file the scan reads defines it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"assembled from other values", "derived from the release", "the chart has no value that sets them"} {
		if strings.Contains(out, not) {
			t.Errorf("the review still has a sub-heading or the old hint, %q:\n%s", not, out)
		}
	}
}

// TestLiveSource_NamesTheSideAfterWhatItIs: the file meld shows for the live
// side says whether it is the live cluster or a backup (--baseline), and
// both sides' labels are the ones internal/ui defines.
func TestLiveSource_NamesTheSideAfterWhatItIs(t *testing.T) {
	if got := liveSource(""); got != "live cluster" {
		t.Errorf("liveSource(\"\") = %q, want %q", got, "live cluster")
	}
	if got := liveSource("/backups/psql/2026-10-01T10-00-00Z"); got != "backup" {
		t.Errorf("liveSource(<backup>) = %q, want %q", got, "backup")
	}
	for got, want := range map[string]string{
		ui.LabelDesired: "desired state", ui.LabelLive: "live cluster", ui.LabelBackup: "backup", ui.LabelLiveNow: "live now",
	} {
		if got != want {
			t.Errorf("label %q, want %q", got, want)
		}
	}
}
