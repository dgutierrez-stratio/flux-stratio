package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/appmigrate"
	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/components"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/prepare"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/ui"
)

func newAppsMigrateCommand() *cobra.Command {
	var all bool
	var dryRun bool
	var yes bool
	var continueOnError bool
	var as string
	var baseline string
	var dir string

	cmd := &cobra.Command{
		Use:   "migrate [name]",
		Short: "Diff an app and splice the resulting patch into the tenant file",
		Long:  appsMigrateLong,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsMigrate(cmd, args, all, dryRun, yes, continueOnError, as, baseline, dir)
		},
	}
	addAsFlag(cmd, &as)
	cmd.Flags().StringVar(&baseline, "baseline", "",
		"compute the patch against a backup (see apps backup) instead of the live cluster — for a component Flux "+
			"already reconciled unpatched, whose live state no longer reflects the legacy installation; takes the same "+
			"values as apps diff --baseline (e.g. latest)")
	cmd.Flags().StringVar(&dir, "dir", "", "backups root directory to resolve --baseline latest against (default: a backups/ directory next to the catalog file)")
	cmd.Flags().BoolVar(&all, "all", false, "migrate every live object a catalog type selects and the tenant file declares")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing anything")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt (and never ask which type/entry an ambiguous live object is — fail naming --as instead)")
	cmd.Flags().BoolVar(&continueOnError, "continue-on-error", false, "with --all, keep migrating remaining apps after one fails (default: stop on the first error)")
	return cmd
}

func runAppsMigrate(cmd *cobra.Command, args []string, all, dryRun, yes, continueOnError bool, as, baseline, dirFlag string) error {
	if all == (len(args) > 0) {
		return fmt.Errorf("provide exactly one of an app name or --all")
	}
	if as != "" && all {
		return fmt.Errorf("--as only applies to a single named app")
	}

	s, err := openSession(cmd)
	if err != nil {
		return err
	}
	logger := s.log
	base, cluster, tenant := s.env.Base, s.env.Cluster, s.env.Tenant

	cat, err := catalog.Load(filepath.Join(base, "keos-use-cases"))
	if err != nil {
		return err
	}
	ropts, err := s.resolveOptions(cmd, true, !yes, as)
	if err != nil {
		return err
	}

	var apps []config.App
	var unresolved []error
	if all {
		if apps, unresolved, err = components.ResolveAll(ropts); err != nil {
			return err
		}
		for _, u := range unresolved {
			logger.Failuref("%v", u)
		}
		// An unresolved instance is a failed app like any other: without
		// --continue-on-error, nothing is migrated past it.
		if len(unresolved) > 0 && !continueOnError {
			return fmt.Errorf("%d live instance(s) could not be resolved; answer interactively, migrate them one at a time with --as, or pass --continue-on-error to migrate the rest", len(unresolved))
		}
		apps = appmigrate.OrderApps(ropts.Doc, cat, apps)
	} else {
		app, err := components.Resolve(ropts, args[0])
		if err != nil {
			return err
		}
		apps = []config.App{app}
	}
	c := s.client

	var failed []string
	for _, app := range apps {
		err := migrateOne(cmd, app, base, cluster, tenant, s.env.ChartsBase, cat, c, logger, dryRun, yes, baseline, dirFlag)
		if err == nil {
			continue
		}
		// A single requested app just returns its own error as-is —
		// narrating it here too would only repeat the same text a second
		// time as the final aggregated "Error: ...". With more than one
		// app, the per-app ✗ line and the aggregate list of failed IDs
		// genuinely say different things, so both stay.
		if len(apps) == 1 {
			return err
		}
		logger.Failuref("%q: %v", app.Name, err)
		failed = append(failed, app.ID)
		if !continueOnError {
			break
		}
	}
	if len(unresolved) > 0 {
		failed = append(failed, fmt.Sprintf("%d unresolved instance(s)", len(unresolved)))
	}
	if len(failed) > 0 {
		return fmt.Errorf("migration failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}

func migrateOne(cmd *cobra.Command, app config.App, base, cluster, tenant, chartsBase string, cat *catalog.Catalog, c client.Client, logger *log.Logger, dryRun, yes bool, baseline, dirFlag string) error {
	resolvedBaseline := ""
	if baseline != "" {
		var err error
		if resolvedBaseline, err = resolveBackupArg(baseline, app.ID, dirFlag); err != nil {
			return err
		}
	}

	if app.Prepare != "" {
		if err := ensurePrepared(cmd, app, tenant, c, logger, dryRun, yes); err != nil {
			return err
		}
	}

	opts := appmigrate.Options{
		Base: base, Cluster: cluster, Tenant: tenant, ChartsBase: chartsBase, App: app, Catalog: cat,
		Runner: runner.Exec{}, Client: c, Baseline: resolvedBaseline, Log: logger,
	}

	logger.Actionf("migrating %q: patch from %s", app.Name, desiredComparison(baseline, resolvedBaseline))
	planned, err := appmigrate.Plan(cmd.Context(), opts)
	if err != nil {
		return err
	}
	warnFluxManaged(logger, app, planned.FluxManagedBy)
	if !planned.Migrated {
		reportNoChange(logger, planned.UpToDate, planned.ObsoletePatches)
		return nil
	}

	if err := ui.FileDiff(cmd.OutOrStdout(), planned.Before, planned.After); err != nil {
		return err
	}

	if dryRun {
		logger.Successf("dry run: would migrate %q", app.Name)
		return nil
	}

	if !yes {
		proceed, err := appmigrate.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(), fmt.Sprintf("Apply this patch to %q? [y/N] ", app.Name))
		if err != nil {
			return err
		}
		if !proceed {
			logger.Warningf("skipped %q (not confirmed)", app.Name)
			return nil
		}
	}

	result, err := appmigrate.Apply(cmd.Context(), opts)
	if err != nil {
		return err
	}
	if result.Migrated {
		logger.Successf("migrated %q", app.Name)
	}
	return nil
}

// ensurePrepared checks app's declared prepare precondition and, if it
// doesn't already hold, satisfies it (design decision 5 in the project
// plan). An automated step's plan — every operation, and the live
// manifest of the object it acts on (stdout) — is shown first; --dry-run
// stops there, and otherwise it runs under the same --yes gate as the
// migration itself, applying exactly the operations shown. A
// non-automated step (prepare-genai) prints what the operator must do and
// always asks its own separate confirmation, never skipped by --yes —
// silently proceeding against unrewritten data risks real data corruption.
func ensurePrepared(cmd *cobra.Command, app config.App, tenant string, c client.Client, logger *log.Logger, dryRun, yes bool) error {
	step := prepare.Find(app.Prepare)
	if step == nil {
		return fmt.Errorf("app %q declares unknown prepare step %q", app.ID, app.Prepare)
	}

	if !step.Automated {
		logger.Warningf("prepare step %q is required for %q: %s. Do it by hand:\n%s", step.Name, app.Name, step.Description, step.InstructionsFor(tenant))
		if dryRun {
			logger.Successf("dry run: would ask whether you have completed prepare step %q", step.Name)
			return nil
		}
		proceed, err := appmigrate.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(), fmt.Sprintf("Have you already completed %q? [y/N] ", step.Name))
		if err != nil {
			return err
		}
		if !proceed {
			return fmt.Errorf("prepare step %q not confirmed; %q not migrated", step.Name, app.ID)
		}
		return nil
	}

	popts := prepare.Options{TenantName: tenant, LiveName: app.LiveName(), LiveNamespace: app.LiveNamespace(), Client: c, Log: logger}
	ops, err := step.Plan(cmd.Context(), popts)
	if err != nil {
		return fmt.Errorf("planning prepare step %q: %w", step.Name, err)
	}
	if len(ops) == 0 {
		logger.Debugf("prepare step %q already satisfied", step.Name)
		return nil
	}

	logger.Actionf("prepare step %q is required for %q: %s. It will:", step.Name, app.Name, step.Description)
	for i, op := range ops {
		logger.Actionf("  %d. %s", i+1, op)
	}
	if err := writePrepareManifests(cmd.OutOrStdout(), ops); err != nil {
		return err
	}
	if dryRun {
		logger.Successf("dry run: would run prepare step %q (%d operation(s) above)", step.Name, len(ops))
		return nil
	}
	if !yes {
		proceed, err := appmigrate.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(), fmt.Sprintf("Run these %d operation(s) of prepare step %q now? [y/N] ", len(ops), step.Name))
		if err != nil {
			return err
		}
		if !proceed {
			return fmt.Errorf("prepare step %q not confirmed; %q not migrated", step.Name, app.ID)
		}
	}
	for i, op := range ops {
		logger.Waitingf("%d/%d %s", i+1, len(ops), op)
		if err := op.Apply(cmd.Context(), c); err != nil {
			return fmt.Errorf("running prepare step %q: %w", step.Name, err)
		}
	}
	logger.Successf("prepare step %q complete", step.Name)
	return nil
}

// writePrepareManifests writes each operation's live object as a YAML
// document, headed by a comment naming the operation.
func writePrepareManifests(w io.Writer, ops []prepare.Operation) error {
	for i, op := range ops {
		manifest, err := op.Manifest()
		if err != nil {
			return fmt.Errorf("rendering %s: %w", op, err)
		}
		if _, err := fmt.Fprintf(w, "---\n# %d. %s\n%s", i+1, op, manifest); err != nil {
			return err
		}
	}
	return nil
}
