package cli

import (
	"bytes"
	"strings"
	"testing"

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
		},
		Missing:  []string{"Deployment stratio-genai/genai-ui"},
		LiveOnly: 3,
		Source:   "backup",
	})
	out := buf.String()
	for _, want := range []string{
		"Deployment stratio-genai/genai-ui: rendered by the chart but not in the backup",
		"2 difference(s) need manual review",
		`VAULT_ROLE: rendered "genai_genai-ui", live "legacy" — ambiguous between genaiApi.general.identity.approlename, genaiUi.general.identity.approlename`,
		`rocket/TENANT: rendered "a", live "b" — conflicting live values for rocketCommon.tenant`,
		"3 variable(s) in the backup aren't rendered by the chart",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
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
