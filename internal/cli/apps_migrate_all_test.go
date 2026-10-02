package cli

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
)

// TestMigrateApps_DependentsOfAnUnmigratedAppAreSkipped: with
// --continue-on-error, an app whose dependency failed or was declined
// is never migrated, and every app left out is reported.
func TestMigrateApps_DependentsOfAnUnmigratedAppAreSkipped(t *testing.T) {
	apps := []config.App{{ID: "psql", Name: "psql"}, {ID: "pool-psql", Name: "pool-psql"}, {ID: "kafka", Name: "kafka"}}
	deps := map[string][]string{"pool-psql": {"psql"}}

	for name, psqlErr := range map[string]error{
		"failed":   errors.New("render failed"),
		"declined": fmt.Errorf("patch for %q %w", "psql", errNotConfirmed),
	} {
		t.Run(name, func(t *testing.T) {
			var ran []string
			migrate := func(app config.App) error {
				ran = append(ran, app.ID)
				if app.ID == "psql" {
					return psqlErr
				}
				return nil
			}
			var out bytes.Buffer
			failed, err := migrateApps(apps, deps, true, migrate, log.New(&out, false))
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{"psql", "kafka"}; !reflect.DeepEqual(ran, want) {
				t.Errorf("migrated %v, want %v (pool-psql left out)", ran, want)
			}
			if want := []string{"psql", "pool-psql"}; !reflect.DeepEqual(failed, want) {
				t.Errorf("failed = %v, want %v", failed, want)
			}
			if !strings.Contains(out.String(), `"pool-psql": skipped, it depends on "psql"`) {
				t.Errorf("output lacks the skip:\n%s", out.String())
			}
		})
	}
}

// TestMigrateApps_StopsAtFirstFailure: without --continue-on-error
// nothing runs after a failure.
func TestMigrateApps_StopsAtFirstFailure(t *testing.T) {
	apps := []config.App{{ID: "a"}, {ID: "b"}}
	var ran []string
	migrate := func(app config.App) error { ran = append(ran, app.ID); return errors.New("boom") }
	failed, err := migrateApps(apps, nil, false, migrate, log.New(&bytes.Buffer{}, false))
	if err != nil || !reflect.DeepEqual(failed, []string{"a"}) || !reflect.DeepEqual(ran, []string{"a"}) {
		t.Errorf("failed = %v, ran = %v, err = %v; want only a, failed", failed, ran, err)
	}
}

// TestReportSkippedPrepare: --skip-prepare never passes in silence: each app
// gets a warning naming the step and the command that finishes it, and the
// end-of-run summary lists every one.
func TestReportSkippedPrepare(t *testing.T) {
	var out bytes.Buffer
	logger := log.New(&out, false)
	app := config.App{ID: "dlc-entity", Name: "DLC dlc-entity", Prepare: "prepare-dlc"}

	reportSkippedPrepare(logger, app, "stratio")
	reportSkippedPrepares(logger, []config.App{app, {ID: "genai", Prepare: "prepare-genai"}})

	for _, want := range []string{
		`prepare step "prepare-dlc" skipped (--skip-prepare)`,
		"flux stratio apps migrate dlc-entity",
		"2 app(s) still need their prepare step",
		"genai (prepare-genai): flux stratio apps migrate genai",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	reportSkippedPrepares(logger, nil)
	if out.Len() != 0 {
		t.Errorf("nothing skipped, yet it printed: %q", out.String())
	}
}
