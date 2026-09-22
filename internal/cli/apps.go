package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/appdiff"
	"github.com/Stratio/flux-stratio/internal/backup"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/ui"
)

func newAppsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Back up, diff and migrate applications",
	}
	cmd.AddCommand(newAppsDiffCommand())
	cmd.AddCommand(newAppsBackupCommand())
	cmd.AddCommand(newAppsMigrateCommand())
	return cmd
}

func newAppsDiffCommand() *cobra.Command {
	var showPatch bool
	var baseline string

	cmd := &cobra.Command{
		Use:   "diff <id>",
		Short: "Compare an app's rendered GitOps desired state against its live legacy state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsDiff(cmd, args[0], showPatch, baseline)
		},
	}
	cmd.Flags().BoolVar(&showPatch, "patch", false, "print the raw patch YAML instead of the default unified diff")
	cmd.Flags().StringVar(&baseline, "baseline", "", "diff against a previously captured backup directory (see apps backup) instead of the live cluster")
	return cmd
}

func runAppsDiff(cmd *cobra.Command, appID string, showPatch bool, baseline string) error {
	logger := rootLogger(cmd)

	cfg, err := config.Load(configFlag)
	if err != nil {
		return err
	}
	app := cfg.Find(appID)
	if app == nil {
		return fmt.Errorf("app %q not found in the config catalog", appID)
	}
	base, cluster, tenant := cfg.Effective(baseFlag, clusterFlag, tenantFlag)

	c, err := kubeclient.New(kubeconfigArgs)
	if err != nil {
		return fmt.Errorf("connecting to the cluster: %w", err)
	}

	logger.Actionf("diffing %q against %s", app.Name, diffTargetLabel(baseline))
	result, err := appdiff.Diff(cmd.Context(), appdiff.Options{
		Base: base, Cluster: cluster, Tenant: tenant,
		App:      *app,
		Runner:   runner.Exec{},
		Client:   c,
		Baseline: baseline,
		Log:      logger,
	})
	if err != nil {
		logger.Failuref("%v", err)
		return err
	}

	if result.Patch == nil {
		logger.Successf("no differences")
		return nil
	}
	logger.Successf("found a difference")

	out := cmd.OutOrStdout()
	if showPatch {
		patchYAML, err := diff.MarshalPatchYAML(*result.Patch)
		if err != nil {
			return err
		}
		return ui.Patch(out, patchYAML)
	}
	return ui.FileDiff(out, result.Before, result.After)
}

func diffTargetLabel(baseline string) string {
	if baseline == "" {
		return "the live cluster"
	}
	return "baseline " + baseline
}

func newAppsBackupCommand() *cobra.Command {
	var all bool
	var dir string

	cmd := &cobra.Command{
		Use:   "backup [id]",
		Short: "Capture an app's live legacy state to disk",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsBackup(cmd, args, all, dir)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "back up every app in the config catalog")
	cmd.Flags().StringVar(&dir, "dir", "", "backups root directory (default: a backups/ directory next to the config file)")
	return cmd
}

func runAppsBackup(cmd *cobra.Command, args []string, all bool, dirFlag string) error {
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

	backupsDir, err := resolveBackupsDir(dirFlag)
	if err != nil {
		return err
	}

	c, err := kubeclient.New(kubeconfigArgs)
	if err != nil {
		return fmt.Errorf("connecting to the cluster: %w", err)
	}

	return backupAll(cmd, apps, backupsDir, base, cluster, tenant, c, logger)
}

// resolveBackupsDir returns dirFlag if set, otherwise a backups/
// directory next to the resolved config file — so backups land beside the
// catalog that describes them by default, with no extra flag needed.
func resolveBackupsDir(dirFlag string) (string, error) {
	if dirFlag != "" {
		return dirFlag, nil
	}
	configPath, err := config.Resolve(configFlag)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(configPath), "backups"), nil
}

func backupAll(cmd *cobra.Command, apps []config.App, backupsDir, base, cluster, tenant string, c client.Client, logger *log.Logger) error {
	var failed []string
	for _, app := range apps {
		logger.Actionf("backing up %q", app.Name)
		result, err := backup.Run(cmd.Context(), backup.Options{
			Base: base, Cluster: cluster, Tenant: tenant,
			App: app, Runner: runner.Exec{}, Client: c, Dir: backupsDir, Log: logger,
		})
		if err != nil {
			logger.Failuref("%v", err)
			failed = append(failed, app.ID)
			continue
		}
		logger.Successf("wrote %s (%s)", result.Dir, strings.Join(result.Files, ", "))
	}
	if len(failed) > 0 {
		return fmt.Errorf("backup failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}
