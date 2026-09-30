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

The patch is always the whole one: the desired side is rendered without
the tenant file's existing patch for the object's kind (the one apps
migrate would replace), so --view patch shows exactly what apps migrate
would write. When the tenant file already carries exactly that patch,
there's nothing to migrate — though --view meld still shows the
differences that patch covers, labelling the desired side "desired state
without tenant patch".

When the live object is already reconciled by Flux (it carries Flux's
kustomize.toolkit.fluxcd.io/name or helm.toolkit.fluxcd.io/name label),
a warning says so: Flux has reset it to the GitOps defaults, so legacy
values it overwrote no longer show up live. Compare against the backup
you took before cutover instead, with --baseline latest.

<name> is a live object's name (psql, psql-agent, kafka1) or its derived
GitOps object name (psql-gosec-agent); the component type is detected
from the live object's labels/annotations against the catalog. Use
--as <type>[/<entry>] to answer any "which one?" question up front.

Examples:

  flux stratio apps diff psql                            # desired state vs. live cluster
  flux stratio apps diff psql --baseline latest           # desired state vs. your last backup
  flux stratio apps diff psql --drift latest              # live now vs. your last backup
  flux stratio apps diff psql --view patch                # print the raw patch YAML instead
  flux stratio apps diff psql --drift latest --view meld  # open the drift check in meld
  flux stratio apps diff pool-psql --baseline latest --view patch
                                                         # Flux already reset live: patch from the backup
  flux stratio apps diff kafka1 --as kafka/kafka         # live kafka1 migrates into tenant entry "kafka"`

const appsBackupLong = `Captures live legacy state to disk, under <dir>/<app-id>/<UTC-timestamp>/:
cr.yaml for a CR-backed component; deployment.yaml + env-vars.env for a
chart-backed one, plus one env-vars.<kind>.<name>.env per live workload so
sibling workloads' same-named variables stay apart (or helmrelease.yaml +
values.yaml when only a HelmRelease is live). It reads only the live
cluster — never the tenant file — so it works before a component is
declared there at all.

Take the backup BEFORE pushing a component to the tenant file. Once Flux
reconciles a component unpatched, it resets the live object to the GitOps
defaults, and the legacy values it overwrote survive only in a backup —
which is what apps diff/migrate --baseline compute the patch from.

  <name>     one component, by live name (psql, psql-agent) or GitOps
             object name (psql-gosec-agent)
  --catalog  every live object a catalog type selects
  --all      that, plus every other live object the cluster scan finds

Examples:

  flux stratio apps backup --catalog      # before touching anything
  flux stratio apps backup pool-psql      # a fresh reference point for one component`

const appsMigrateLong = `Computes a component's patch — the legacy values the GitOps render
doesn't already produce — and splices it into its entry in the tenant
file (components.<key>[name=<entry>].patches, or config.agent.patches for
a gosec agent), preserving every comment. Runs the component's prepare
step first, if its catalog type declares one.

How the patch is computed:

  - The desired side is rendered from the tenant file WITHOUT the
    existing patch for the object's kind (the one this command replaces,
    by target.kind), so the result is always the whole patch — never a
    leftover delta that would drop what the existing patch already set.
  - The legacy side is the live cluster, or a backup with --baseline.
  - If the tenant file already carries exactly that patch, there's
    nothing to do: running migrate twice converges.

Migrate before Flux reconciles the component. If you push a component to
the tenant file unpatched first, Flux resets the live object to the
GitOps defaults, and a patch computed from live can no longer see the
legacy values it overwrote (apps diff/migrate warn when the live object
is already Flux-managed). Recover by computing the patch from the backup
you took before cutover:

  flux stratio apps migrate pool-psql --baseline latest --dry-run
  flux stratio apps migrate pool-psql --baseline latest

then commit and push the tenant file: Flux restores the legacy values.

Do you need --baseline? Usually not:

  - Without it (the default), the legacy side is the live cluster. That is
    right as long as the component's legacy workloads still run untouched —
    i.e. before Flux has reconciled it. A chart's sibling workloads
    (genai-api and genai-ui, say) are each fetched live and compared with
    their own rendered workload.
  - Use --baseline only when live no longer holds the legacy values: you get
    the "already managed by Flux" warning, or you know Flux reset the
    component before you migrated it.
  - Either way, take an apps backup first: it costs nothing, it's the only
    record of the legacy values once Flux reconciles the component, and it
    captures every workload of a multi-workload chart (the catalog type's
    chart.siblings), each in its own env file.

Resolving which component to migrate:

  <name> is a live object's name or its derived GitOps object name; the
  type is detected against the catalog's selectors. If the derived
  tenant entry isn't declared (say live kafka1, tenant entry kafka), or a
  name matches more than one instance, you're asked — or answer up front
  with --as <type>[/<entry>]. Nothing you answer is stored.
  --all migrates every classified instance whose component the tenant
  file declares, in dependency order; types it doesn't declare (absent or
  commented out) are skipped with a warning. With --yes, a question that
  needs an answer isn't asked: that instance fails, naming --as.

Examples:

  flux stratio apps migrate psql --dry-run               # preview the tenant-file edit
  flux stratio apps migrate psql                         # apply it (asks to confirm)
  flux stratio apps migrate kafka1 --as kafka/kafka      # pin the tenant entry
  flux stratio apps migrate pool-psql --baseline latest  # Flux already reset live: use the backup
  flux stratio apps migrate --all --dry-run              # survey everything first
  flux stratio apps migrate --all --continue-on-error    # then migrate what resolves`

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
	var as string

	cmd := &cobra.Command{
		Use:   "diff <name>",
		Short: "Compare an app's desired/live state pre-migration, or check its post-migration drift",
		Long:  appsDiffLong,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsDiff(cmd, args[0], view, baseline, driftAgainst, dir, as)
		},
	}
	addAsFlag(cmd, &as)
	cmd.Flags().StringVar(&baseline, "baseline", "",
		"pre-migration: diff the rendered desired state against a previously captured backup (see apps backup) "+
			"instead of the live cluster — see the command's --help for the full baseline-vs-drift explanation")
	cmd.Flags().StringVar(&driftAgainst, "drift", "",
		"post-migration: compare the live cluster right now directly against a previously captured backup, with no "+
			"GitOps rendering involved — has this app drifted since it was backed up? Mutually exclusive with --baseline "+
			"and with --view patch (see the command's --help)")
	cmd.Flags().StringVar(&view, "view", viewUnified,
		"how to display the comparison: unified (default, a terminal diff), patch (the raw patch YAML apps migrate "+
			"would write; invalid with --drift), meld (open in meld instead, even when there are no differences — see flux stratio doctor)")
	cmd.Flags().StringVar(&dir, "dir", "",
		"backups root directory to resolve --baseline/--drift latest against (default: a backups/ directory next to "+
			"the config file) — must match whatever --dir apps backup used, if any, or latest can't find it")
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
	// inspected: with UpToDate they still differ by the tenant file's own
	// patch, which the base is rendered without — the desired side's label
	// says so, or its differences read as contradicting "nothing to migrate".
	openMeld := func() error {
		desiredLabel := "desired state"
		if result.ExistingPatches > 0 {
			desiredLabel = "desired state without tenant patch"
		}
		return ui.Meld(cmd.Context(), runner.Exec{}, desiredLabel, result.Before, liveSource(resolvedBaseline), result.After)
	}

	warnFluxManaged(logger, app, result.FluxManagedBy)
	review := chartReview{
		Unmapped: result.UnmappedDiffs, Missing: result.MissingWorkloads, LiveOnly: result.LiveOnlyCount,
		Source: liveSource(resolvedBaseline),
	}
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
	// LiveOnly counts live variables the chart doesn't render.
	LiveOnly int
	// Source names the live side: "live cluster" or "backup".
	Source string
}

// hasWarnings reports whether reportChartReview prints anything for r.
func (r chartReview) hasWarnings() bool {
	return len(r.Missing) > 0 || len(r.Unmapped) > 0 || r.LiveOnly > 0
}

// reportChartReview warns about everything a chart-mode diff/migrate
// leaves out of its patch, so none of it is lost silently: rendered
// workloads it had nothing to compare against, differences it couldn't
// attribute to one .Values path (listed one per line, with the candidate
// paths), and live variables the chart has no place for.
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
	if r.LiveOnly > 0 {
		logger.Warningf("%d variable(s) in the %s aren't rendered by the chart: their values will be lost after migration", r.LiveOnly, r.Source)
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
	case diff.UnmappedConflict:
		line += " for " + strings.Join(u.Candidates, ", ")
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
		Use:   "backup [name]",
		Short: "Capture an app's live legacy state to disk",
		Long:  appsBackupLong,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsBackup(cmd, args, catalog, all, dir, as)
		},
	}
	addAsFlag(cmd, &as)
	cmd.Flags().BoolVar(&catalog, "catalog", false, "back up every live object a catalog type selects")
	cmd.Flags().BoolVar(&all, "all", false, "back up every live object the cluster scan finds, not just the config catalog")
	cmd.Flags().StringVar(&dir, "dir", "", "backups root directory (default: a backups/ directory next to the config file)")
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
