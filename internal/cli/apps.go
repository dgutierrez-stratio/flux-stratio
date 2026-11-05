package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/appdiff"
	"github.com/Stratio/flux-stratio/internal/backup"
	"github.com/Stratio/flux-stratio/internal/components"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/discovery"
	"github.com/Stratio/flux-stratio/internal/drift"
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

func newAppsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "apps",
		Short:   "Back up, compare and migrate apps",
		Long:    appsLong,
		Example: appsDiffExample + "\n" + appsMigrateExample,
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
	var as string

	cmd := &cobra.Command{
		Use:     "diff <name>",
		Short:   "Show what migrating an app would change, or whether it has drifted",
		Long:    appsDiffLong,
		Example: appsDiffExample,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsDiff(cmd, args[0], view, baseline, driftAgainst, dir, as)
		},
	}
	addAsFlag(cmd, &as)
	cmd.Flags().StringVar(&baseline, "baseline", "",
		"compare against a backup instead of the live cluster: latest, an app's backup directory, or the backups root")
	cmd.Flags().StringVar(&driftAgainst, "drift", "",
		"compare the live cluster now against a backup, with no GitOps rendering (same values as --baseline; not with --baseline or --view patch)")
	cmd.Flags().StringVar(&view, "view", viewUnified,
		"how to show the result: unified (a diff, default), patch (the patch migrate would write) or meld (open meld, even with no differences)")
	cmd.Flags().StringVar(&dir, "dir", "",
		"backups root, where --baseline/--drift latest look (default: a backups/ directory next to the catalog file; must match the --dir of apps backup)")
	return cmd
}

func runAppsDiff(cmd *cobra.Command, name, view, baseline, driftAgainst, dirFlag, as string) error {
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

	s, err := openSession(cmd)
	if err != nil {
		return err
	}
	// --drift reads only the live cluster and a backup, never the tenant
	// file; the desired-state comparisons render from it.
	ropts, err := s.resolveOptions(cmd, driftAgainst == "", true, as)
	if err != nil {
		return err
	}
	app, err := components.Resolve(ropts, name)
	if err != nil {
		return err
	}
	s.log.Debugf("%q resolved to %s (entry %q, kustomization %q, live %s/%s)",
		name, app.Type, app.Entry, app.Kustomization, app.LiveNamespace(), app.LiveName())

	if driftAgainst != "" {
		return runAppsDriftDiff(cmd, app, s, driftAgainst, view, dirFlag)
	}
	return runAppsDesiredDiff(cmd, app, s.env, s.client, baseline, view, dirFlag, s.log)
}

// runAppsDesiredDiff is the default `apps diff` comparison: the rendered
// GitOps desired state against the live cluster, or (--baseline) a
// previously captured backup standing in for it.
func runAppsDesiredDiff(cmd *cobra.Command, app config.App, env config.Environment, c client.Client, baseline, view, dirFlag string, logger *log.Logger) error {
	resolvedBaseline := ""
	if baseline != "" {
		var err error
		resolvedBaseline, err = resolveBackupArg(baseline, app.ID, dirFlag)
		if err != nil {
			return err
		}
	}

	comparison := desiredComparison(baseline, resolvedBaseline)
	logger.Actionf("diffing %q: %s", app.Name, comparison)
	result, err := appdiff.Diff(cmd.Context(), appdiff.Options{
		Repos: env.RepoPaths(), Cluster: env.Cluster, Tenant: env.Tenant,
		App:      app,
		Runner:   runner.Exec{},
		Client:   c,
		Baseline: resolvedBaseline,
		Log:      logger,
	})
	if err != nil {
		return err
	}

	// meld opens even when there's nothing to change, so both sides can be
	// inspected.
	openMeld := func() error {
		return ui.OpenMeld(cmd.Context(), runner.Exec{}, ui.LabelDesired, result.Before, liveSource(resolvedBaseline), result.After, result.Items)
	}

	review := chartReview{
		Unmapped: result.UnmappedDiffs, Missing: result.MissingWorkloads, LiveOnly: result.LiveOnly, Native: result.Native,
		SpecDiffs: result.SpecDiffs, Carried: result.CarriedResources, Services: result.Services, Excluded: result.Excluded, Source: liveSource(resolvedBaseline),
		Prepare: app.Prepare,
	}
	review.Extra = slices.Concat(
		fluxManagedSection(app, result.FluxManagedBy),
		sizeSection(env.RepoPaths(), env.Cluster, env.Tenant, app.Object, result.LiveSize),
		droppedSection(result.DroppedFromExisting),
	)
	reportChartReview(logger, review)
	if result.Patch == nil || result.UpToDate {
		reportNoChange(logger, result.UpToDate, result.ObsoletePatches, review)
		if view == viewMeld {
			return openMeld()
		}
		return nil
	}
	logger.Successf("found a difference: %s", comparison)

	switch view {
	case viewPatch:
		patchYAML, err := diff.MarshalPatchYAML(*result.Patch)
		if err != nil {
			return err
		}
		return ui.Patch(cmd.OutOrStdout(), patchYAML)
	case viewMeld:
		return openMeld()
	default:
		return ui.FileDiffWith(cmd.OutOrStdout(), result.Before, result.After,
			diffOptions(cmd, desiredLabel, liveSource(resolvedBaseline), sectionsFor(app.ChartPath)))
	}
}

// runAppsDriftDiff is `apps diff --drift`: the live cluster right now,
// compared directly against a stored backup — no GitOps rendering, no
// tenant file, no render at all. See internal/drift's package doc for why
// this is a different question than the default comparison.
func runAppsDriftDiff(cmd *cobra.Command, app config.App, s *session, driftAgainst, view, dirFlag string) error {
	logger := s.log
	resolved, err := resolveBackupArg(driftAgainst, app.ID, dirFlag)
	if err != nil {
		return err
	}

	against := backupLabel(driftAgainst, resolved)
	logger.Actionf("checking %q for drift: live now vs. %s", app.Name, against)
	result, err := drift.Run(cmd.Context(), drift.Options{
		App: app, Repos: s.env.RepoPaths(), Runner: runner.Exec{}, Client: s.client, Index: s.index,
		Against: resolved, Log: logger,
	})
	if err != nil {
		return err
	}

	if result.Before == result.After {
		logger.Successf("no drift: live now matches %s", against)
	} else {
		logger.Successf("found drift: live now differs from %s", against)
	}

	// meld opens even without drift, so both sides can be inspected; an
	// empty unified diff has nothing to show.
	if view == viewMeld {
		return ui.OpenMeld(cmd.Context(), runner.Exec{}, ui.LabelBackup, result.Before, ui.LabelLiveNow, result.After, result.Items)
	}
	return ui.FileDiffWith(cmd.OutOrStdout(), result.Before, result.After, diffOptions(cmd, ui.LabelBackup, ui.LabelLiveNow, ui.SectionsNone))
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

// reportNoChange narrates a diff/migrate that found nothing to write:
// either live already matches the base (warning when an existing patch
// would now move it away), or the tenant file already carries exactly the
// needed patch — unless review holds differences no patch could carry,
// which reportChartReview already listed.
func reportNoChange(logger *log.Logger, upToDate bool, obsoletePatches int, review chartReview) {
	switch {
	case upToDate:
		logger.Successf("nothing to migrate: the tenant file's existing patch already covers every difference")
	case len(review.Unmapped) > 0:
		logger.Warningf("nothing can be patched automatically: the %d difference(s) listed above need manual review", len(review.Unmapped))
	case obsoletePatches > 0:
		logger.Warningf("no differences against the unpatched base, but the tenant file carries %d patch(es) for this object "+
			"that Flux would apply on top — review or remove them", obsoletePatches)
	case len(review.SpecDiffs) > 0:
		logger.Successf("no environment variable differences; the workload settings listed above differ")
	default:
		logger.Successf("no differences")
	}
}

// chartReview is what a chart-mode diff couldn't carry into its patch.
type chartReview struct {
	// Unmapped are the differences no patch value could be computed for.
	Unmapped []diff.UnmappedDiff
	// Missing names the rendered workloads with no live side to compare.
	Missing []string
	// LiveOnly are the live variables the chart doesn't render.
	LiveOnly []diff.LiveOnlyVar
	// Native are the live variables the chart carries natively with the same
	// value (a probe variable and the container probe): information, not a
	// warning (see hasWarnings).
	Native []diff.LiveOnlyVar
	// SpecDiffs are the resources, image and replicas that still differ
	// between the patched rendered workloads and the live ones: only the
	// risky ones are warnings (see hasWarnings).
	SpecDiffs []diff.SpecDiff
	// Carried are the containers whose live resources the patch carries:
	// information, so the patch doesn't change them silently.
	Carried []diff.ResourceEdit
	// Services are the live Services the cutover would leave selecting
	// nothing: one exposed and not carried into the patch, or a ClusterIP
	// one the chart serves none of the ports of, is a warning (see
	// hasWarnings); a carried one or a replaced leftover is not.
	Services []diff.ServiceFinding
	// Excluded are the differences the catalog's exclude list kept out of
	// the patch: reported, but not a warning (see hasWarnings).
	Excluded []diff.ExcludedDiff
	// Source names the live side: "live cluster" or "backup".
	Source string
	// Prepare names the app's prepare step ("" for none): what a selector
	// the legacy workload can't be updated across is told to do depends on it.
	Prepare string
	// Extra are findings that aren't about the comparison itself (Flux
	// already manages the live object, the tenant size, the dependencies the
	// patch needs ...), laid out with the rest.
	Extra []reviewSection
}

// hasWarnings reports whether reportChartReview warns about anything for r:
// Excluded and the informational SpecDiffs never count.
func (r chartReview) hasWarnings() bool {
	return len(r.Missing) > 0 || len(r.Unmapped) > 0 || len(r.LiveOnly) > 0 || len(specDiffsOf(r.SpecDiffs, diff.SpecRisk)) > 0 ||
		slices.ContainsFunc(r.Services, serviceLost)
}

// serviceLost reports whether the cutover leaves f's Service unreachable
// with nothing taking its place: exposed and not carried into the patch,
// or a ClusterIP one no rendered Service replaces.
func serviceLost(f diff.ServiceFinding) bool {
	if f.Exposed() {
		return f.Path == ""
	}
	return f.Replacement == ""
}

// specDiffsOf are the diffs of one severity, in order.
func specDiffsOf(diffs []diff.SpecDiff, severity diff.SpecSeverity) []diff.SpecDiff {
	var out []diff.SpecDiff
	for _, d := range diffs {
		if d.Severity == severity {
			out = append(out, d)
		}
	}
	return out
}

// liveSource names what a desired-state diff compared against.
func liveSource(resolvedBaseline string) string {
	if resolvedBaseline != "" {
		return ui.LabelBackup
	}
	return ui.LabelLive
}

// desiredComparison names what a desired-state diff (apps diff without
// --drift, and apps migrate's patch computation) compares, for the log:
// the rendered GitOps desired state against the live cluster, or against
// a backup when --baseline was given (arg is its raw flag value, resolved
// the backup directory it resolved to).
func desiredComparison(arg, resolved string) string {
	if resolved == "" {
		return "desired state vs. live cluster"
	}
	return "desired state vs. " + backupLabel(arg, resolved)
}

// backupLabel names a --baseline/--drift backup for the log: "your last
// backup (<dir>)" when it was auto-located with "latest", "backup <dir>"
// when the operator named it.
func backupLabel(arg, resolved string) string {
	if arg == autoLocateBackup {
		return fmt.Sprintf("your last backup (%s)", resolved)
	}
	return "backup " + resolved
}

func newAppsBackupCommand() *cobra.Command {
	var catalog bool
	var all bool
	var dir string
	var as string

	cmd := &cobra.Command{
		Use:     "backup [name]",
		Short:   "Save apps' legacy state to disk (before Flux reconciles them)",
		Long:    appsBackupLong,
		Example: appsBackupExample,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsBackup(cmd, args, catalog, all, dir, as)
		},
	}
	addAsFlag(cmd, &as)
	cmd.Flags().BoolVar(&catalog, "catalog", false, "back up every live object that a catalog type selects")
	cmd.Flags().BoolVar(&all, "all", false, "back up --catalog plus every other live object found in the cluster")
	cmd.Flags().StringVar(&dir, "dir", "", "backups root to write into (default: a backups/ directory next to the catalog file)")
	return cmd
}

func runAppsBackup(cmd *cobra.Command, args []string, catalog, all bool, dirFlag, as string) error {
	selected := 0
	for _, v := range []bool{len(args) > 0, catalog, all} {
		if v {
			selected++
		}
	}
	if selected != 1 {
		return fmt.Errorf("provide exactly one of an app name, --catalog or --all")
	}
	if as != "" && len(args) == 0 {
		return fmt.Errorf("--as only applies to a single named app")
	}

	backupsDir, err := resolveBackupsDir(dirFlag)
	if err != nil {
		return err
	}
	s, err := openSession(cmd)
	if err != nil {
		return err
	}
	// A backup captures live state only: no tenant file involved.
	ropts, err := s.resolveOptions(cmd, false, true, as)
	if err != nil {
		return err
	}

	var apps []config.App
	var unresolved []error
	switch {
	case len(args) > 0:
		app, err := components.Resolve(ropts, args[0])
		if err != nil {
			return err
		}
		apps = []config.App{app}
	case catalog:
		if apps, unresolved, err = components.ResolveAll(ropts); err != nil {
			return err
		}
	default: // all
		catalogApps, u, err := components.ResolveAll(ropts)
		if err != nil {
			return err
		}
		unresolved = u
		apps = backup.DiscoveredApps(catalogApps, s.index)
	}
	// Like any other per-app backup failure, an unresolved instance never
	// stops the rest from being captured.
	for _, u := range unresolved {
		s.log.Failuref("%v", u)
	}

	err = backupAll(cmd, apps, backupsDir, s.env.RepoPaths(), s.client, s.index, s.log)
	if err == nil && len(unresolved) > 0 {
		err = fmt.Errorf("backup skipped %d unresolved instance(s)", len(unresolved))
	}
	return err
}

// resolveBackupsDir returns dirFlag if set, otherwise a backups/
// directory next to the resolved catalog file — so backups land beside the
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

func backupAll(cmd *cobra.Command, apps []config.App, backupsDir string, repos config.RepoPaths, c client.Client, idx *discovery.Index, logger *log.Logger) error {
	var failed []string
	for _, app := range apps {
		logger.Actionf("backing up %q", app.Name)
		result, err := backup.Run(cmd.Context(), backup.Options{
			Repos: repos, App: app, Runner: runner.Exec{}, Client: c, Index: idx, Dir: backupsDir, Log: logger,
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
