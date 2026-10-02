package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

func TestReportChartReview(t *testing.T) {
	var buf bytes.Buffer
	reportChartReview(log.New(&buf, false), chartReview{
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
			{Workload: "rocket", Name: "LEGACY_FLAG", Live: "true"},
			{Name: "EXTRA_JARS", Live: "a.jar,b.jar"},
		},
		Source: "backup",
	})
	out := buf.String()
	for _, want := range []string{
		"Deployment stratio-genai/genai-ui: rendered by the chart but not in the backup",
		"4 difference(s) need manual review",
		`KERBEROS_REALM_NAME: rendered "eosdev.int", live "EOSDEV.INT" — conflicting live values for cluster.domain, which also sets PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN=eosdev.int`,
		`HARDCODED: rendered "1", live "2" — set inline in the container env`,
		`VAULT_ROLE: rendered "genai_genai-ui", live "legacy" — ambiguous between genaiApi.general.identity.approlename, genaiUi.general.identity.approlename`,
		`rocket/TENANT: rendered "a", live "b" — conflicting live values for rocketCommon.tenant`,
		"2 variable(s) in the backup aren't rendered by the chart",
		`rocket/LEGACY_FLAG: live "true"`,
		`EXTRA_JARS: live "a.jar,b.jar"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
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

	var buf bytes.Buffer
	reportChartReview(log.New(&buf, false), review)
	out := buf.String()
	for _, want := range []string{
		`excluded by the catalog: genai-api/VAULT_ROLE live "legacy-role", GitOps default "genai_genai-api" (genaiApi.general.identity.approlename)`,
		`excluded by the catalog: ROLE live "b", GitOps default "a" (x.role)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "⚠") {
		t.Errorf("excluded differences were printed as warnings:\n%s", out)
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

func TestReportLegacyAgentPatches(t *testing.T) {
	app := config.App{Object: "opensearch1-gosec-agent"}
	var buf bytes.Buffer
	reportLegacyAgentPatches(log.New(&buf, false), app, 0)
	if buf.Len() != 0 {
		t.Errorf("no legacy patch: output = %q, want nothing", buf.String())
	}
	reportLegacyAgentPatches(log.New(&buf, false), app, 1)
	for _, want := range []string{"opensearch1-gosec-agent", "config.agent.patches", "remove"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q: %s", want, buf.String())
		}
	}
}

func TestReportUnresolvedDeps(t *testing.T) {
	var buf bytes.Buffer
	app := config.App{Object: "rocket", Kustomization: "apps-rocket"}
	reportUnresolvedDeps(log.New(&buf, false), app, []tenantfile.UnresolvedDependency{
		{Key: "dgAgent", Name: "dg-agent", Declared: []string{"dg-hdfs-agent"}},
		{Key: "virtualizer", Name: "virtualizer"},
	})
	out := buf.String()
	for _, want := range []string{
		`rocket depends on dgAgent "dg-agent", which the tenant file doesn't declare (declared: dg-hdfs-agent) — Flux will hold apps-rocket back on apps-dg-agent until it's fixed`,
		`rocket depends on virtualizer "virtualizer", which the tenant file doesn't declare (none declared)`,
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
