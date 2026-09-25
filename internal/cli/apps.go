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
	"github.com/Stratio/flux-stratio/internal/discovery"
	"github.com/Stratio/flux-stratio/internal/drift"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/ui"
)

// autoLocateBackup is a reserved --baseline/--drift value meaning
// "auto-locate the app's most recent capture" rather than naming a
// literal directory called that. It's an ordinary flag value, not a
// pflag NoOptDefVal — cobra/pflag only honors NoOptDefVal for
// "--flag=value" syntax, silently misparsing the far more common
// "--flag value" (space-separated) form as two separate arguments
// instead, which is worse than not having a bare-flag shortcut at all.
const autoLocateBackup = "latest"

// The three values --view accepts — one flag replacing the old
// independent --patch/--meld bools, so there's exactly one way to pick a
// view instead of two flags whose combination needed a runtime check.
const (
	viewUnified = "unified"
	viewPatch   = "patch"
	viewMeld    = "meld"
)

const appsDiffLong = `Compares one app's state two different ways, depending on the question:

Pre-migration — "what would migrating this app change?": the rendered
GitOps desired state against the live cluster (the default), or against
a --baseline backup standing in for live — useful right before a
disruptive step (e.g. a prepare step) changes live state and you want to
diff against what it looked like a moment ago, not whatever it looks
like when you happen to run the diff.

Post-migration — "has this app drifted since I backed it up?": --drift
compares the live cluster right now directly against a stored backup,
with no GitOps rendering at all. Once Flux is reconciling an app, the
pre-migration question above stops being the useful one to ask; --drift
is the one to reach for instead. --baseline and --drift are mutually
exclusive — pick one question at a time.

--baseline and --drift both take the same kind of value — a backup
directory, as written by apps backup — and resolve it the same three ways:

  --baseline latest                     the app's most recent apps backup capture
  --baseline backups/psql               that app's own backup directory: picks the latest timestamp under it
  --baseline backups                    the overall backups/ root: finds <root>/psql/ and picks the latest there
  --baseline backups/psql/2026-01-02T15-04-05Z   an exact capture, used as-is

("latest" is a reserved keyword, not a directory name — pass a real path
for anything else. NoOptDefVal/"bare --baseline with nothing after it" is
deliberately not supported: pflag would silently swallow the next token —
even --help — as --baseline's value instead of parsing it as its own
flag, which is far more confusing than requiring an explicit value.)

Examples:

  flux stratio apps diff psql                            # desired state vs. live cluster
  flux stratio apps diff psql --baseline latest           # desired state vs. your last backup
  flux stratio apps diff psql --drift latest              # live now vs. your last backup
  flux stratio apps diff psql --view patch                # print the raw patch YAML instead
  flux stratio apps diff psql --drift latest --view meld  # open the drift check in meld`

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
	var baseline string
	var driftAgainst string
	var view string
	var dir string

	cmd := &cobra.Command{
		Use:   "diff <id>",
		Short: "Compare an app's desired/live state pre-migration, or check its post-migration drift",
		Long:  appsDiffLong,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsDiff(cmd, args[0], view, baseline, driftAgainst, dir)
		},
	}
	cmd.Flags().StringVar(&baseline, "baseline", "",
		"pre-migration: diff the rendered desired state against a previously captured backup (see apps backup) "+
			"instead of the live cluster — see the command's --help for the full baseline-vs-drift explanation")
	cmd.Flags().StringVar(&driftAgainst, "drift", "",
		"post-migration: compare the live cluster right now directly against a previously captured backup, with no "+
			"GitOps rendering involved — has this app drifted since it was backed up? Mutually exclusive with --baseline "+
			"and with --view patch (see the command's --help)")
	cmd.Flags().StringVar(&view, "view", viewUnified,
		"how to display the comparison: unified (default, a terminal diff), patch (the raw patch YAML apps migrate "+
			"would write; invalid with --drift), meld (open in meld instead — see flux stratio doctor)")
	cmd.Flags().StringVar(&dir, "dir", "",
		"backups root directory to resolve --baseline/--drift latest against (default: a backups/ directory next to "+
			"the config file) — must match whatever --dir apps backup used, if any, or latest can't find it")
	return cmd
}

func runAppsDiff(cmd *cobra.Command, appID, view, baseline, driftAgainst, dirFlag string) error {
	switch view {
	case viewUnified, viewPatch, viewMeld:
	default:
		return fmt.Errorf("--view must be one of %s, %s or %s (got %q)", viewUnified, viewPatch, viewMeld, view)
	}
	if baseline != "" && driftAgainst != "" {
		return fmt.Errorf("provide at most one of --baseline or --drift")
	}
	if view == viewPatch && driftAgainst != "" {
		return fmt.Errorf("--view patch has no meaning with --drift: there's no GitOps patch, only what changed live")
	}

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

	if driftAgainst != "" {
		return runAppsDriftDiff(cmd, *app, base, cfg.ChartsBase, c, driftAgainst, view, dirFlag, logger)
	}
	return runAppsDesiredDiff(cmd, *app, base, cluster, tenant, cfg.ChartsBase, c, baseline, view, dirFlag, logger)
}

// runAppsDesiredDiff is the default `apps diff` comparison: the rendered
// GitOps desired state against the live cluster, or (--baseline) a
// previously captured backup standing in for it.
func runAppsDesiredDiff(cmd *cobra.Command, app config.App, base, cluster, tenant, chartsBase string, c client.Client, baseline, view, dirFlag string, logger *log.Logger) error {
	resolvedBaseline := ""
	if baseline != "" {
		var err error
		resolvedBaseline, err = resolveBackupArg(baseline, app.ID, dirFlag)
		if err != nil {
			return err
		}
	}

	logger.Actionf("diffing %q against %s", app.Name, diffTargetLabel(resolvedBaseline))
	result, err := appdiff.Diff(cmd.Context(), appdiff.Options{
		Base: base, Cluster: cluster, Tenant: tenant, ChartsBase: chartsBase,
		App:      app,
		Runner:   runner.Exec{},
		Client:   c,
		Baseline: resolvedBaseline,
		Log:      logger,
	})
	if err != nil {
		return err
	}

	if result.Patch == nil {
		logger.Successf("no differences")
		return nil
	}
	logger.Successf("found a difference")

	switch view {
	case viewPatch:
		patchYAML, err := diff.MarshalPatchYAML(*result.Patch)
		if err != nil {
			return err
		}
		return ui.Patch(cmd.OutOrStdout(), patchYAML)
	case viewMeld:
		liveLabel := "live"
		if resolvedBaseline != "" {
			liveLabel = "backup"
		}
		return ui.Meld(cmd.Context(), runner.Exec{}, "rendered", result.Before, liveLabel, result.After)
	default:
		return ui.FileDiff(cmd.OutOrStdout(), result.Before, result.After)
	}
}

// runAppsDriftDiff is `apps diff --drift`: the live cluster right now,
// compared directly against a stored backup — no GitOps rendering, no
// tenant file, no render at all. See internal/drift's package doc for why
// this is a different question than the default comparison.
func runAppsDriftDiff(cmd *cobra.Command, app config.App, base, chartsBase string, c client.Client, driftAgainst, view, dirFlag string, logger *log.Logger) error {
	resolved, err := resolveBackupArg(driftAgainst, app.ID, dirFlag)
	if err != nil {
		return err
	}

	logger.Actionf("scanning the live cluster")
	idx, err := discovery.Scan(cmd.Context(), c, logger)
	if err != nil {
		return fmt.Errorf("scanning the live cluster: %w", err)
	}
	logger.Successf("scan complete")

	logger.Actionf("checking %q for drift since %s", app.Name, resolved)
	result, err := drift.Run(cmd.Context(), drift.Options{
		App: app, Base: base, ChartsBase: chartsBase, Runner: runner.Exec{}, Client: c, Index: idx,
		Against: resolved, Log: logger,
	})
	if err != nil {
		return err
	}

	if result.Before == result.After {
		logger.Successf("no drift since %s", resolved)
		return nil
	}
	logger.Successf("found drift since %s", resolved)

	if view == viewMeld {
		return ui.Meld(cmd.Context(), runner.Exec{}, "backup", result.Before, "live", result.After)
	}
	return ui.FileDiff(cmd.OutOrStdout(), result.Before, result.After)
}

// resolveBackupArg turns a --baseline/--drift value into an actual
// timestamped backup directory: autoLocateBackup resolves under dirFlag
// if set, else the default backups directory next to the config file —
// the same resolution apps backup --dir itself uses, so latest can find
// a backup captured with a matching --dir; anything else is resolved via
// backup.ResolveBaseline, which accepts a specific backup dir, an app's
// own backup root, or the overall backups root just as well.
func resolveBackupArg(value, appID, dirFlag string) (string, error) {
	root := value
	if value == autoLocateBackup {
		var err error
		root, err = resolveBackupsDir(dirFlag)
		if err != nil {
			return "", err
		}
	}
	return backup.ResolveBaseline(root, appID)
}

func diffTargetLabel(baseline string) string {
	if baseline == "" {
		return "the live cluster"
	}
	return "baseline " + baseline
}

func newAppsBackupCommand() *cobra.Command {
	var catalog bool
	var all bool
	var dir string

	cmd := &cobra.Command{
		Use:   "backup [id]",
		Short: "Capture an app's live legacy state to disk",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsBackup(cmd, args, catalog, all, dir)
		},
	}
	cmd.Flags().BoolVar(&catalog, "catalog", false, "back up every app in the config catalog")
	cmd.Flags().BoolVar(&all, "all", false, "back up every live object the cluster scan finds, not just the config catalog")
	cmd.Flags().StringVar(&dir, "dir", "", "backups root directory (default: a backups/ directory next to the config file)")
	return cmd
}

func runAppsBackup(cmd *cobra.Command, args []string, catalog, all bool, dirFlag string) error {
	selected := 0
	for _, v := range []bool{len(args) > 0, catalog, all} {
		if v {
			selected++
		}
	}
	if selected != 1 {
		return fmt.Errorf("provide exactly one of an app id, --catalog or --all")
	}

	logger := rootLogger(cmd)
	cfg, err := config.Load(configFlag)
	if err != nil {
		return err
	}

	base, _, _ := cfg.Effective(baseFlag, clusterFlag, tenantFlag)

	backupsDir, err := resolveBackupsDir(dirFlag)
	if err != nil {
		return err
	}

	c, err := kubeclient.New(kubeconfigArgs)
	if err != nil {
		return fmt.Errorf("connecting to the cluster: %w", err)
	}

	logger.Actionf("scanning the live cluster")
	idx, err := discovery.Scan(cmd.Context(), c, logger)
	if err != nil {
		return fmt.Errorf("scanning the live cluster: %w", err)
	}
	logger.Successf("scan complete")

	var apps []config.App
	switch {
	case len(args) > 0:
		app := cfg.Find(args[0])
		if app == nil {
			return fmt.Errorf("app %q not found in the config catalog", args[0])
		}
		apps = []config.App{*app}
	case catalog:
		apps = cfg.Apps
	default: // all
		apps = backup.DiscoveredApps(cfg, idx)
	}

	return backupAll(cmd, apps, backupsDir, base, cfg.ChartsBase, c, idx, logger)
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

func backupAll(cmd *cobra.Command, apps []config.App, backupsDir, base, chartsBase string, c client.Client, idx *discovery.Index, logger *log.Logger) error {
	var failed []string
	for _, app := range apps {
		logger.Actionf("backing up %q", app.Name)
		result, err := backup.Run(cmd.Context(), backup.Options{
			Base: base, ChartsBase: chartsBase, App: app, Runner: runner.Exec{}, Client: c, Index: idx, Dir: backupsDir, Log: logger,
		})
		if err != nil {
			// A single requested app just returns its own error as-is —
			// narrating it here too would only repeat the same text a
			// second time as the final aggregated "Error: ...". With
			// more than one app, the per-app ✗ line and the aggregate
			// list of failed IDs genuinely say different things, so both
			// stay.
			if len(apps) == 1 {
				return err
			}
			logger.Failuref("%v", err)
			failed = append(failed, app.ID)
			continue
		}
		if len(result.Files) == 0 {
			continue
		}
		logger.Successf("wrote %s (%s)", result.Dir, strings.Join(result.Files, ", "))
	}
	if len(failed) > 0 {
		return fmt.Errorf("backup failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}
