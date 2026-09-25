package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/appmigrate"
	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/prepare"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
	"github.com/Stratio/flux-stratio/internal/ui"
)

func newAppsMigrateCommand() *cobra.Command {
	var all bool
	var dryRun bool
	var yes bool
	var continueOnError bool

	cmd := &cobra.Command{
		Use:   "migrate [id]",
		Short: "Diff an app and splice the resulting patch into the tenant file",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsMigrate(cmd, args, all, dryRun, yes, continueOnError)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "migrate every app in the config catalog")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing anything")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&continueOnError, "continue-on-error", false, "with --all, keep migrating remaining apps after one fails (default: stop on the first error)")
	return cmd
}

func runAppsMigrate(cmd *cobra.Command, args []string, all, dryRun, yes, continueOnError bool) error {
	if all == (len(args) > 0) {
		return fmt.Errorf("provide exactly one of an app id or --all")
	}

	logger := rootLogger(cmd)
	cfg, err := config.Load(configFlag)
	if err != nil {
		return err
	}

	var apps []config.App
	if all {
		apps = cfg.Apps
	} else {
		app := cfg.Find(args[0])
		if app == nil {
			return fmt.Errorf("app %q not found in the config catalog", args[0])
		}
		apps = []config.App{*app}
	}

	base, cluster, tenant := cfg.Effective(baseFlag, clusterFlag, tenantFlag)

	cat, err := catalog.Load(filepath.Join(base, "keos-use-cases"))
	if err != nil {
		return err
	}

	c, err := kubeclient.New(kubeconfigArgs)
	if err != nil {
		return fmt.Errorf("connecting to the cluster: %w", err)
	}

	if all {
		doc, err := tenantfile.Load(tenantfile.Path(base, cluster, tenant))
		if err != nil {
			return err
		}
		apps = appmigrate.OrderApps(doc, cat, apps)
	}

	var failed []string
	for _, app := range apps {
		err := migrateOne(cmd, app, base, cluster, tenant, cfg.ChartsBase, cat, c, logger, dryRun, yes)
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
	if len(failed) > 0 {
		return fmt.Errorf("migration failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}

func migrateOne(cmd *cobra.Command, app config.App, base, cluster, tenant, chartsBase string, cat *catalog.Catalog, c client.Client, logger *log.Logger, dryRun, yes bool) error {
	if app.Prepare != "" {
		if err := ensurePrepared(cmd, app, tenant, c, logger, dryRun, yes); err != nil {
			return err
		}
	}

	opts := appmigrate.Options{
		Base: base, Cluster: cluster, Tenant: tenant, ChartsBase: chartsBase, App: app, Catalog: cat,
		Runner: runner.Exec{}, Client: c, Log: logger,
	}

	logger.Actionf("migrating %q", app.Name)
	planned, err := appmigrate.Plan(cmd.Context(), opts)
	if err != nil {
		return err
	}
	if !planned.Migrated {
		logger.Successf("no differences")
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
		proceed, err := appmigrate.Confirm(cmd.InOrStdin(), fmt.Sprintf("Apply this patch to %q? [y/N] ", app.Name))
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
// plan): an automated step runs under the same --dry-run/--yes gate as
// the migration itself; a non-automated step (prepare-genai) prints what
// the operator must do and always asks its own separate confirmation,
// never skipped by --yes — silently proceeding against unrewritten data
// risks real data corruption.
func ensurePrepared(cmd *cobra.Command, app config.App, tenant string, c client.Client, logger *log.Logger, dryRun, yes bool) error {
	step := prepare.Find(app.Prepare)
	if step == nil {
		return fmt.Errorf("app %q declares unknown prepare step %q", app.ID, app.Prepare)
	}
	popts := prepare.Options{TenantName: tenant, Client: c, Log: logger}

	satisfied, err := step.Satisfied(cmd.Context(), popts)
	if err != nil {
		return fmt.Errorf("checking prepare step %q: %w", step.Name, err)
	}
	if satisfied {
		logger.Debugf("prepare step %q already satisfied", step.Name)
		return nil
	}

	if !step.Automated {
		if err := step.Run(cmd.Context(), popts); err != nil {
			return err
		}
		proceed, err := appmigrate.Confirm(cmd.InOrStdin(), fmt.Sprintf("Have you already completed %q? [y/N] ", step.Name))
		if err != nil {
			return err
		}
		if !proceed {
			return fmt.Errorf("prepare step %q not confirmed; %q not migrated", step.Name, app.ID)
		}
		return nil
	}

	logger.Actionf("prepare step %q is required for %q: %s", step.Name, app.Name, step.Description)
	if dryRun {
		logger.Successf("dry run: would run prepare step %q", step.Name)
		return nil
	}
	if !yes {
		proceed, err := appmigrate.Confirm(cmd.InOrStdin(), fmt.Sprintf("Run prepare step %q for %q now? [y/N] ", step.Name, app.Name))
		if err != nil {
			return err
		}
		if !proceed {
			return fmt.Errorf("prepare step %q not confirmed; %q not migrated", step.Name, app.ID)
		}
	}
	if err := step.Run(cmd.Context(), popts); err != nil {
		return fmt.Errorf("running prepare step %q: %w", step.Name, err)
	}
	logger.Successf("prepare step %q complete", step.Name)
	return nil
}
