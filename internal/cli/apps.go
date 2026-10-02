package cli

import (
	"fmt"
	"path/filepath"
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
	"github.com/Stratio/flux-stratio/internal/tenantfile"
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
	var patched bool

	cmd := &cobra.Command{
		Use:     "diff <name>",
		Short:   "Show what migrating an app would change, or whether it has drifted",
		Long:    appsDiffLong,
		Example: appsDiffExample,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsDiff(cmd, args[0], view, baseline, driftAgainst, dir, as, patched)
		},
	}
	addAsFlag(cmd, &as)
	cmd.Flags().StringVar(&baseline, "baseline", "",
		"compare against a backup instead of the live cluster: latest, an app's backup directory, or the backups root")
	cmd.Flags().StringVar(&driftAgainst, "drift", "",
		"compare the live cluster now against a backup, with no GitOps rendering (same values as --baseline; not with --baseline, --patched or --view patch)")
	cmd.Flags().StringVar(&view, "view", viewUnified,
		"how to show the result: unified (a diff, default), patch (the patch migrate would write) or meld (open meld, even with no differences)")
	cmd.Flags().BoolVar(&patched, "patched", false,
		"render the desired state WITH the tenant patch applied: what does the migrated app still differ in? (not with --drift or --view patch)")
	cmd.Flags().StringVar(&dir, "dir", "",
		"backups root, where --baseline/--drift latest look (default: a backups/ directory next to the catalog file; must match the --dir of apps backup)")
	return cmd
}

func runAppsDiff(cmd *cobra.Command, name, view, baseline, driftAgainst, dirFlag, as string, patched bool) error {
	switch view {
	case viewUnified, viewPatch, viewMeld:
	default:
		return fmt.Errorf("--view must be one of %s, %s or %s (got %q)", viewUnified, viewPatch, viewMeld, view)
	}
	if baseline != "" && driftAgainst != "" {
		return fmt.Errorf("provide at most one of --baseline or --drift")
	}
	if patched && driftAgainst != "" {
		return fmt.Errorf("--patched has no meaning with --drift: there's no GitOps rendering to apply the patch to")
	}
	if patched && view == viewPatch {
		return fmt.Errorf("--patched has no --view patch: the patch is already in the tenant file; what's left is a comparison")
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
	return runAppsDesiredDiff(cmd, app, s.env, s.client, baseline, view, dirFlag, patched, s.log)
}

// runAppsDesiredDiff is the default `apps diff` comparison: the rendered
// GitOps desired state against the live cluster, or (--baseline) a
// previously captured backup standing in for it.
func runAppsDesiredDiff(cmd *cobra.Command, app config.App, env config.Environment, c client.Client, baseline, view, dirFlag string, patched bool, logger *log.Logger) error {
	resolvedBaseline := ""
	if baseline != "" {
		var err error
		resolvedBaseline, err = resolveBackupArg(baseline, app.ID, dirFlag)
		if err != nil {
			return err
		}
	}

	comparison := desiredComparison(baseline, resolvedBaseline)
	if patched {
		comparison = strings.Replace(comparison, "desired state", "desired state with the tenant patch applied", 1)
	}
	logger.Actionf("diffing %q: %s", app.Name, comparison)
	result, err := appdiff.Diff(cmd.Context(), appdiff.Options{
		Repos: env.RepoPaths(), Cluster: env.Cluster, Tenant: env.Tenant,
		App:             app,
		Runner:          runner.Exec{},
		Client:          c,
		Baseline:        resolvedBaseline,
		WithTenantPatch: patched,
		Log:             logger,
	})
	if err != nil {
		return err
	}

	// meld opens even when there's nothing to change, so both sides can be
	// inspected: with UpToDate they still differ by the tenant file's own
	// patch, which the base is rendered without — the desired side's label
	// says so, or its differences read as contradicting "nothing to migrate".
	openMeld := func() error {
		desiredLabel := "desired state"
		switch {
		case patched:
			desiredLabel = "desired state with tenant patch"
		case result.ExistingPatches > 0:
			desiredLabel = "desired state without tenant patch"
		}
		return ui.Meld(cmd.Context(), runner.Exec{}, desiredLabel, result.Before, liveSource(resolvedBaseline), result.After)
	}

	warnFluxManaged(logger, app, result.FluxManagedBy)
	review := chartReview{
		Unmapped: result.UnmappedDiffs, Missing: result.MissingWorkloads, LiveOnly: result.LiveOnly,
		SpecDiffs: result.SpecDiffs, Carried: result.CarriedResources, Excluded: result.Excluded, Source: liveSource(resolvedBaseline),
	}
	reportChartReview(logger, review)
	reportSizeMismatch(logger, env.RepoPaths(), env.Cluster, env.Tenant, app.Object, result.LiveSize)
	reportDroppedFromExisting(logger, app, result.DroppedFromExisting)
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
		return ui.FileDiff(cmd.OutOrStdout(), result.Before, result.After)
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
		return ui.Meld(cmd.Context(), runner.Exec{}, "backup", result.Before, "live now", result.After)
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

// warnFluxManaged warns when the live object compared against is already
// reconciled by Flux: its state is then the GitOps render's, not the
// legacy installation's, so legacy values Flux reset are invisible to the
// comparison — the patch should come from a backup taken before cutover.
func warnFluxManaged(logger *log.Logger, app config.App, managedBy string) {
	if managedBy == "" {
		return
	}
	logger.Warningf("live %s/%s is already managed by Flux (%s): it may no longer hold the legacy values — "+
		"compare against a pre-cutover backup instead: --baseline latest (see apps backup)",
		app.LiveNamespace(), app.LiveName(), managedBy)
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
	// SpecDiffs are the resources, image and replicas that still differ
	// between the patched rendered workloads and the live ones: only the
	// risky ones are warnings (see hasWarnings).
	SpecDiffs []diff.SpecDiff
	// Carried are the containers whose live resources the patch carries:
	// information, so the patch doesn't change them silently.
	Carried []diff.ResourceEdit
	// Excluded are the differences the catalog's exclude list kept out of
	// the patch: reported, but not a warning (see hasWarnings).
	Excluded []diff.ExcludedDiff
	// Source names the live side: "live cluster" or "backup".
	Source string
}

// hasWarnings reports whether reportChartReview warns about anything for r:
// Excluded and the informational SpecDiffs never count.
func (r chartReview) hasWarnings() bool {
	return len(r.Missing) > 0 || len(r.Unmapped) > 0 || len(r.LiveOnly) > 0 || len(specDiffsOf(r.SpecDiffs, diff.SpecRisk)) > 0
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

// reportSizeMismatch warns when the tenant file's size isn't the one the
// legacy application reports: the GitOps size overlay it is rendered with
// then differs from what ran. Nothing is inferred or rewritten.
func reportSizeMismatch(logger *log.Logger, repos config.RepoPaths, cluster, tenant, app, liveSize string) {
	if liveSize == "" {
		return
	}
	doc, err := tenantfile.Load(tenantfile.Path(repos.Fleet, cluster, tenant))
	if err != nil {
		return // the diff already read the tenant file; nothing useful to add
	}
	if size := doc.DefaultSize(); size != "" && size != liveSize {
		logger.Warningf("tenant size %s but live %s reports CUSTOMER_SIZE=%s: overlay sizing will differ from live", size, app, liveSize)
	}
}

// reportCarried says which containers' live resources the patch carries.
func reportCarried(logger *log.Logger, r chartReview) {
	for _, e := range r.Carried {
		var fields []string
		for _, d := range e.Diffs {
			live := d.Live
			if live == "" {
				live = "unset"
			}
			fields = append(fields, strings.TrimPrefix(d.Field, "resources.")+" "+live+" (GitOps "+orUnset(d.Rendered)+")")
		}
		logger.Actionf("carrying the legacy resources of %s/%s into the patch (%s): %s",
			e.Workload, e.Container, e.Path, strings.Join(fields, ", "))
	}
}

func orUnset(v string) string {
	if v == "" {
		return "unset"
	}
	return v
}

// reportSpecDiffs prints the risky differences as warnings and the others as
// plain information, grouped under their workload.
func reportSpecDiffs(logger *log.Logger, r chartReview) {
	if risks := specDiffsOf(r.SpecDiffs, diff.SpecRisk); len(risks) > 0 {
		logger.Warningf("%d workload setting(s) differ from the %s and can break the migrated workload — not written to the patch:", len(risks), r.Source)
		for _, d := range risks {
			logger.Warningf("  %s", describeSpecDiff(d))
		}
	}
	if infos := specDiffsOf(r.SpecDiffs, diff.SpecInfo); len(infos) > 0 {
		logger.Actionf("%d other workload setting(s) differ from the %s:", len(infos), r.Source)
		for _, d := range infos {
			logger.Actionf("  %s", describeSpecDiff(d))
		}
	}
}

func describeSpecDiff(d diff.SpecDiff) string {
	name := d.Workload
	if d.Container != "" {
		name += "/" + d.Container
	}
	quote := func(v string) string {
		if v == "" {
			return "unset"
		}
		return fmt.Sprintf("%q", v)
	}
	if env, ok := strings.CutPrefix(d.Field, diff.EnvVsLimitField); ok {
		return fmt.Sprintf("%s %s=%s asks for more memory than the container's limit %s", name, env, d.Live, d.Rendered)
	}
	if d.Field == diff.SelectorField {
		return fmt.Sprintf("%s selector: rendered %s, live %s — a selector is immutable, so the legacy workload can't be updated in place: "+
			"it has to be deleted before Flux installs the release", name, quote(d.Rendered), quote(d.Live))
	}
	return fmt.Sprintf("%s %s: rendered %s, live %s", name, d.Field, quote(d.Rendered), quote(d.Live))
}

// reportChartReview warns about everything a chart-mode diff/migrate
// leaves out of its patch, so none of it is lost silently: rendered
// workloads it had nothing to compare against, differences it couldn't
// attribute to one .Values path (listed one per line, with the candidate
// paths), and live variables the chart has no place for. Differences the
// catalog's exclude list kept out on purpose are listed last, as plain
// information.
func reportChartReview(logger *log.Logger, r chartReview) {
	for _, w := range r.Missing {
		logger.Warningf("%s: rendered by the chart but not in the %s — nothing of it is carried into the patch", w, r.Source)
	}
	if len(r.Unmapped) > 0 {
		logger.Warningf("%d difference(s) need manual review — not written to the patch:", len(r.Unmapped))
		for _, u := range r.Unmapped {
			logger.Warningf("  %s", describeUnmapped(u))
		}
	}
	if len(r.LiveOnly) > 0 {
		logger.Warningf("%d variable(s) in the %s aren't rendered by the chart: their values will be lost after migration:", len(r.LiveOnly), r.Source)
		for _, v := range r.LiveOnly {
			name := v.Name
			if v.Workload != "" {
				name = v.Workload + "/" + v.Name
			}
			if v.ShadowedBy != "" {
				logger.Warningf("  %s: live %q — chart default env %s is shadowed by the list at %s", name, v.Live, v.Name, v.ShadowedBy)
				continue
			}
			logger.Warningf("  %s: live %q", name, v.Live)
		}
	}
	reportCarried(logger, r)
	reportSpecDiffs(logger, r)
	for _, e := range r.Excluded {
		name := e.Name
		if e.Workload != "" {
			name = e.Workload + "/" + e.Name
		}
		logger.Actionf("excluded by the catalog: %s live %q, GitOps default %q (%s)", name, e.Live, e.Rendered, e.Path)
	}
}

func describeUnmapped(u diff.UnmappedDiff) string {
	name := u.Name
	if u.Workload != "" {
		name = u.Workload + "/" + u.Name
	}
	line := fmt.Sprintf("%s: rendered %q, live %q — %s", name, u.Rendered, u.Live, u.Reason)
	switch u.Reason {
	case diff.UnmappedAmbiguous:
		line += " between " + strings.Join(u.Candidates, ", ")
	case diff.UnmappedNotReproduced:
		line += " (" + strings.Join(u.Candidates, ", ") + ")"
	case diff.UnmappedConflict:
		line += " for " + strings.Join(u.Candidates, ", ")
		if len(u.Shared) > 0 {
			line += ", which also sets " + strings.Join(u.Shared, ", ")
		}
	}
	return line
}

// liveSource names what a desired-state diff compared against.
func liveSource(resolvedBaseline string) string {
	if resolvedBaseline != "" {
		return "backup"
	}
	return "live cluster"
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
