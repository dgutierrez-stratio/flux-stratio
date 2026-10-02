package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/appmigrate"
	"github.com/Stratio/flux-stratio/internal/backup"
	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/components"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/discovery"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/prepare"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
	"github.com/Stratio/flux-stratio/internal/ui"
)

// migrateFlags are apps migrate's flags that shape how each app is
// migrated.
type migrateFlags struct {
	dryRun, yes, acceptWarnings, skipPrepare bool
	baseline, dir                            string
	// onSkippedPrepare, if set, hears of each app whose prepare step
	// --skip-prepare left out, for the summary at the end of the run.
	onSkippedPrepare func(config.App)
}

// errNotConfirmed is a migration the operator declined at a prompt: not a
// failure of the tool, but not a migrated app either — `--all` leaves its
// dependents out, and the command's exit status says it wasn't migrated.
var errNotConfirmed = errors.New("not confirmed")

func newAppsMigrateCommand() *cobra.Command {
	var all bool
	var continueOnError bool
	var as string
	var flags migrateFlags

	cmd := &cobra.Command{
		Use:   "migrate [name]",
		Short: "Diff an app and splice the resulting patch into the tenant file",
		Long:  appsMigrateLong,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsMigrate(cmd, args, all, continueOnError, as, flags)
		},
	}
	addAsFlag(cmd, &as)
	cmd.Flags().StringVar(&flags.baseline, "baseline", "",
		"compute the patch against a backup (see apps backup) instead of the live cluster — for a component Flux "+
			"already reconciled unpatched, whose live state no longer reflects the legacy installation; takes the same "+
			"values as apps diff --baseline (e.g. latest)")
	cmd.Flags().StringVar(&flags.dir, "dir", "", "backups root directory to resolve --baseline latest against (default: a backups/ directory next to the catalog file)")
	cmd.Flags().BoolVar(&all, "all", false, "migrate every live object a catalog type selects and the tenant file declares")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "show what would change without writing anything")
	cmd.Flags().BoolVarP(&flags.yes, "yes", "y", false, "skip the confirmation prompt (and never ask which type/entry an ambiguous live object is — fail naming --as instead); an app with warnings still stops unless --accept-warnings is also given")
	cmd.Flags().BoolVar(&flags.acceptWarnings, "accept-warnings", false, "with --yes, migrate an app even when its diff has warnings (values that will be lost, differences needing review, unresolved dependencies)")
	cmd.Flags().BoolVar(&flags.skipPrepare, "skip-prepare", false, "patch the tenant file but leave out every app's prepare step (nothing is run, shown or asked, and no backup is taken for it); "+
		"each skipped step is warned about and listed at the end — the app isn't ready for Flux to reconcile until apps migrate <app> runs it, which finds the patch up to date and goes straight to the step")
	cmd.Flags().BoolVar(&continueOnError, "continue-on-error", false, "with --all, keep migrating remaining apps after one fails (default: stop on the first error)")
	return cmd
}

func runAppsMigrate(cmd *cobra.Command, args []string, all, continueOnError bool, as string, flags migrateFlags) error {
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
	repos, cluster, tenant := s.env.RepoPaths(), s.env.Cluster, s.env.Tenant

	cat, err := catalog.Load(repos.UseCases)
	if err != nil {
		return err
	}
	ropts, err := s.resolveOptions(cmd, true, !flags.yes, as)
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
	deps := appmigrate.Dependencies(ropts.Doc, cat, apps)
	var skippedPrepare []config.App
	flags.onSkippedPrepare = func(app config.App) { skippedPrepare = append(skippedPrepare, app) }
	migrate := func(app config.App) error {
		return migrateOne(cmd, app, repos, cluster, tenant, cat, c, s.execer, s.index, logger, flags)
	}
	failed, err := migrateApps(apps, deps, continueOnError, migrate, logger)
	reportSkippedPrepares(logger, skippedPrepare)
	if err != nil {
		return err
	}
	if len(unresolved) > 0 {
		failed = append(failed, fmt.Sprintf("%d unresolved instance(s)", len(unresolved)))
	}
	if len(failed) > 0 {
		return fmt.Errorf("not migrated: %s", strings.Join(failed, ", "))
	}
	return nil
}

// migrateApps migrates apps in order, returning the IDs of those not
// migrated. A single app's error is returned as is. With several, each
// failure is narrated, and the loop stops at the first one unless
// continueOnError; an app depending (deps, by ID) on one that failed or
// was declined is left out too, since Flux would hold it back behind
// its unmigrated dependency anyway.
func migrateApps(apps []config.App, deps map[string][]string, continueOnError bool, migrate func(config.App) error, logger *log.Logger) ([]string, error) {
	var failed []string
	notMigrated := map[string]bool{}
	for _, app := range apps {
		if dep := firstNotMigrated(deps[app.ID], notMigrated); dep != "" {
			logger.Failuref("%q: skipped, it depends on %q, which wasn't migrated", app.Name, dep)
			notMigrated[app.ID] = true
			failed = append(failed, app.ID)
			continue
		}
		err := migrate(app)
		if err == nil {
			continue
		}
		notMigrated[app.ID] = true
		// A single requested app just returns its own error as-is —
		// narrating it here too would only repeat the same text a second
		// time as the final aggregated "Error: ...". With more than one
		// app, the per-app ✗ line and the aggregate list of failed IDs
		// genuinely say different things, so both stay.
		if len(apps) == 1 {
			return nil, err
		}
		logger.Failuref("%q: %v", app.Name, err)
		failed = append(failed, app.ID)
		if !continueOnError {
			break
		}
	}
	return failed, nil
}

// firstNotMigrated returns the first of deps in notMigrated, or "".
func firstNotMigrated(deps []string, notMigrated map[string]bool) string {
	for _, d := range deps {
		if notMigrated[d] {
			return d
		}
	}
	return ""
}

func migrateOne(cmd *cobra.Command, app config.App, repos config.RepoPaths, cluster, tenant string, cat *catalog.Catalog, c client.Client, execer kubeclient.Execer, idx *discovery.Index, logger *log.Logger, flags migrateFlags) error {
	dryRun, yes, baseline, dirFlag := flags.dryRun, flags.yes, flags.baseline, flags.dir
	resolvedBaseline := ""
	if baseline != "" {
		var err error
		if resolvedBaseline, err = resolveBackupArg(baseline, app.ID, dirFlag); err != nil {
			return err
		}
	}

	opts := appmigrate.Options{
		Repos: repos, Cluster: cluster, Tenant: tenant, App: app, Catalog: cat,
		Runner: runner.Exec{}, Client: c, Baseline: resolvedBaseline, Log: logger,
	}

	logger.Actionf("migrating %q: patch from %s", app.Name, desiredComparison(baseline, resolvedBaseline))
	planned, err := appmigrate.Plan(cmd.Context(), opts)
	if err != nil {
		return err
	}
	warnFluxManaged(logger, app, planned.FluxManagedBy)
	review := chartReview{
		Unmapped: planned.UnmappedDiffs, Missing: planned.MissingWorkloads, LiveOnly: planned.LiveOnly,
		Excluded: planned.Excluded, Source: liveSource(resolvedBaseline),
	}
	reportChartReview(logger, review)
	reportUnresolvedDeps(logger, app, planned.UnresolvedDeps)
	reportLegacyAgentPatches(logger, app, planned.LegacyAgentPatches)
	// The patch is planned, and saved, before app's prepare step runs: a
	// prepare step may delete the very live workload the patch is read
	// from (prepare-dlc), so a patch saved only afterwards would be lost
	// with it if the save failed. Saved first, a prepare step that fails
	// or is declined leaves a tenant file already carrying the patch, and
	// re-running apps migrate finds it up to date and goes straight to the
	// prepare step. backupFirst captures the live state before an
	// automated prepare step changes any of it.
	backupFirst := func() error {
		return backupBeforePrepare(cmd, app, repos, c, idx, logger, dirFlag)
	}
	prepared := func() error {
		if app.Prepare == "" {
			return nil
		}
		if flags.skipPrepare {
			reportSkippedPrepare(logger, app, tenant)
			if flags.onSkippedPrepare != nil {
				flags.onSkippedPrepare(app)
			}
			return nil
		}
		return ensurePrepared(cmd, app, tenant, c, execer, backupFirst, logger, dryRun, yes)
	}
	reportDroppedFromExisting(logger, app, planned.DroppedFromExisting)
	// An obsolete patch (reportNoChange warns of it below) and a patch that
	// would replace one carrying more than the new patch does are warnings
	// like any other: --yes must not wave them through unreviewed.
	warned := planned.FluxManagedBy != "" || review.hasWarnings() || len(planned.UnresolvedDeps) > 0 ||
		planned.ObsoletePatches > 0 || len(planned.DroppedFromExisting) > 0 || planned.LegacyAgentPatches > 0
	if !planned.Migrated {
		reportNoChange(logger, planned.UpToDate, planned.ObsoletePatches, review)
		if app.Prepare == "" {
			return nil
		}
		// No patch to show, but the prepare step still changes the
		// cluster: the warnings above stop it just the same. Skipped, it
		// changes nothing, so there is nothing for them to stop.
		if !flags.skipPrepare {
			if err := confirmWarnings(cmd, app, warned, flags, "Run its prepare step"); err != nil {
				return err
			}
		}
		return prepared()
	}

	if err := confirmWarnings(cmd, app, warned, flags, "Show the patch"); err != nil {
		return err
	}

	if err := ui.FileDiff(cmd.OutOrStdout(), planned.Before, planned.After); err != nil {
		return err
	}

	if dryRun {
		if err := prepared(); err != nil {
			return err
		}
		logger.Successf("dry run: would migrate %q", app.Name)
		return nil
	}

	if !yes {
		proceed, err := appmigrate.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(), fmt.Sprintf("Apply this patch to %q? [y/N] ", app.Name))
		if err != nil {
			return err
		}
		if !proceed {
			return fmt.Errorf("patch for %q %w", app.Name, errNotConfirmed)
		}
	}

	if err := saveThenPrepare(app, planned.Save, prepared); err != nil {
		return err
	}
	logger.Successf("migrated %q", app.Name)
	return nil
}

// saveThenPrepare writes app's planned patch, then runs its prepare step:
// never the other way round, since a prepare step may delete the live
// workload the patch was computed from, and a save failing after that
// would lose the patch with it.
func saveThenPrepare(app config.App, save, prepared func() error) error {
	if err := save(); err != nil {
		return err
	}
	if err := prepared(); err != nil {
		return fmt.Errorf("the tenant file now carries %q's patch, but its prepare step didn't complete — don't commit it until `apps migrate %s` runs again and finishes it (if the step already removed the live workload, the diff has nothing live to read: run `apps migrate %s --baseline latest`, which reads the backup taken just before the step): %w", app.Name, app.ID, app.ID, err)
	}
	return nil
}

// backupBeforePrepare backs up app's live state, exactly as `apps backup`
// would, into the backups root dirFlag names (default: next to the
// catalog), before an automated prepare step deletes or changes any of
// it: without one, a deleted legacy workload's values could only come
// back from wherever the operator thought to save them.
func backupBeforePrepare(cmd *cobra.Command, app config.App, repos config.RepoPaths, c client.Client, idx *discovery.Index, logger *log.Logger, dirFlag string) error {
	backupsDir, err := resolveBackupsDir(dirFlag)
	if err != nil {
		return err
	}
	result, err := backup.Run(cmd.Context(), backup.Options{
		Repos: repos, App: app, Runner: runner.Exec{}, Client: c, Index: idx, Dir: backupsDir, Log: logger,
	})
	if err != nil {
		return fmt.Errorf("backing up %q before its prepare step: %w", app.Name, err)
	}
	if len(result.Files) == 0 {
		return fmt.Errorf("backing up %q before its prepare step captured nothing; back it up by hand (apps backup) first", app.Name)
	}
	logger.Successf("backed up %q to %s before its prepare step", app.Name, result.Dir)
	return nil
}

// confirmWarnings stops on the warnings migrate printed before going
// on (next names what comes next, for the question): they'd otherwise
// scroll away behind a long diff, and a migration they rule out never
// gets as far as its apply prompt. Nothing to confirm without warnings,
// and --dry-run never asks. --yes doesn't answer it either: an
// unattended run stops on an app with warnings unless --accept-warnings
// says to carry on regardless.
func confirmWarnings(cmd *cobra.Command, app config.App, warned bool, flags migrateFlags, next string) error {
	if !warned || flags.dryRun {
		return nil
	}
	if flags.yes {
		if flags.acceptWarnings {
			return nil
		}
		return fmt.Errorf("%q has warnings (above) that need review: run it without --yes to review them, or pass --accept-warnings", app.Name)
	}
	proceed, err := appmigrate.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(), fmt.Sprintf("Review the warnings above. %s for %q? [y/N] ", next, app.Name))
	if err != nil {
		return err
	}
	if !proceed {
		return fmt.Errorf("warnings for %q %w", app.Name, errNotConfirmed)
	}
	return nil
}

// reportDroppedFromExisting warns that replacing app's existing patch with
// the computed one loses what only the existing one sets.
func reportDroppedFromExisting(logger *log.Logger, app config.App, dropped []string) {
	if len(dropped) == 0 {
		return
	}
	logger.Warningf("%q: the patch replaces the tenant file's existing one for this object, and drops %d path(s) only the existing patch sets "+
		"(a hand edit, or a value this diff cannot see): %s", app.Name, len(dropped), strings.Join(dropped, ", "))
}

// reportSkippedPrepare warns that --skip-prepare left app's prepare step
// out, and what that leaves undone.
func reportSkippedPrepare(logger *log.Logger, app config.App, tenant string) {
	step := prepare.Find(app.Prepare)
	if step == nil {
		logger.Warningf("%q: its prepare step %q was skipped (--skip-prepare)", app.Name, app.Prepare)
		return
	}
	logger.Warningf("%q: prepare step %q skipped (--skip-prepare): %s. The patch is in the tenant file, but Flux shouldn't reconcile the app "+
		"until the step is done: run `flux stratio apps migrate %s` without --skip-prepare (it finds the patch up to date and goes straight to the step).",
		app.Name, step.Name, step.Description, app.ID)
	if !step.Automated && step.Query == nil {
		logger.Warningf("%q: done by hand, the step is:\n%s", app.Name, step.InstructionsFor(tenant))
	}
}

// reportSkippedPrepares lists, at the end of a run, every app whose prepare
// step --skip-prepare left out, so they can't scroll away unnoticed.
func reportSkippedPrepares(logger *log.Logger, apps []config.App) {
	if len(apps) == 0 {
		return
	}
	lines := make([]string, len(apps))
	for i, a := range apps {
		lines[i] = fmt.Sprintf("  %s (%s): flux stratio apps migrate %s", a.ID, a.Prepare, a.ID)
	}
	logger.Warningf("%d app(s) still need their prepare step before Flux reconciles them:\n%s", len(apps), strings.Join(lines, "\n"))
}

// reportUnresolvedDeps warns about each of app's tenant-file dependencies
// that names an entry the tenant file doesn't declare. The patch is still
// written (the missing entry may be added in the same change), but Flux
// holds the app's Kustomization back on the missing apps-<name> until
// the name is fixed.
func reportUnresolvedDeps(logger *log.Logger, app config.App, deps []tenantfile.UnresolvedDependency) {
	for _, d := range deps {
		declared := "none declared"
		if len(d.Declared) > 0 {
			declared = "declared: " + strings.Join(d.Declared, ", ")
		}
		logger.Warningf("%s depends on %s %q, which the tenant file doesn't declare (%s) — Flux will hold %s back on apps-%s until it's fixed",
			app.Object, d.Key, d.Name, declared, app.Kustomization, d.Name)
	}
}

// reportLegacyAgentPatches warns of gosec-agent patches the legacy client
// wrote to the parent entry's patches, where nothing reads them.
func reportLegacyAgentPatches(logger *log.Logger, app config.App, n int) {
	if n > 0 {
		logger.Warningf("%d patch(es) for %s sit in the parent entry's patches: the legacy client put them there and they are ignored — "+
			"the agent reads config.agent.patches; remove them", n, app.Object)
	}
}

// ensurePrepared checks app's declared prepare precondition and, if it
// doesn't already hold, satisfies it (design decision 5 in the project
// plan). An automated step's plan — every operation, and the live
// manifest of the object it acts on (stdout) — is shown first; --dry-run
// stops there, and otherwise it runs under the same --yes gate as the
// migration itself, applying exactly the operations shown. A Query step
// (prepare-genai) shows its SQL and target pod and asks before running,
// then shows the real captured output and asks again — both questions
// never skipped by --yes — no Kubernetes API can verify a
// data rewrite happened correctly, so apps migrate never claims to on its
// own. A non-automated, non-Query step prints what the operator must do
// by hand and always asks the same way.
func ensurePrepared(cmd *cobra.Command, app config.App, tenant string, c client.Client, execer kubeclient.Execer, backupFirst func() error, logger *log.Logger, dryRun, yes bool) error {
	step := prepare.Find(app.Prepare)
	if step == nil {
		return fmt.Errorf("app %q declares unknown prepare step %q", app.ID, app.Prepare)
	}

	if step.Query != nil {
		return ensurePreparedQuery(cmd, *step, app, tenant, c, execer, logger, dryRun)
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
	if backupFirst == nil {
		return fmt.Errorf("prepare step %q changes live objects, and no backup of %q could be taken first", step.Name, app.ID)
	}
	if err := backupFirst(); err != nil {
		return err
	}
	for i, op := range ops {
		logger.Waitingf("%d/%d %s", i+1, len(ops), op)
		if err := op.Apply(cmd.Context(), c); err != nil {
			return fmt.Errorf("running prepare step %q: %w", step.Name, err)
		}
	}
	logger.Successf("prepare step %q complete", step.Name)
	logger.Warningf("what prepare step %q deleted or scaled down stays that way until the tenant file's change is committed, pushed and reconciled by Flux", step.Name)
	return nil
}

// ensurePreparedQuery shows step's Query — the SQL, with the tenant
// substituted, and the pod it will run against — and asks before running
// it, then shows the real captured output and asks again whether it looks
// right. Neither question is skipped by --yes: the query rewrites live
// application data, which no Kubernetes API can verify or undo. The Python
// client's own version of this same check exited 0 unconditionally,
// marking the step migrated on faith whether or not the SQL was ever run.
func ensurePreparedQuery(cmd *cobra.Command, step prepare.Step, app config.App, tenant string, c client.Client, execer kubeclient.Execer, logger *log.Logger, dryRun bool) error {
	popts := prepare.Options{TenantName: tenant, LiveName: app.LiveName(), LiveNamespace: app.LiveNamespace(), Client: c, Execer: execer, Log: logger}

	pod, _, _, err := step.RunQuery(cmd.Context(), popts, tenant, true)
	if err != nil {
		return fmt.Errorf("resolving prepare step %q's target pod: %w", step.Name, err)
	}
	logger.Warningf("prepare step %q is required for %q: %s. It will run against pod %s/%s:\n%s",
		step.Name, app.Name, step.Description, pod.Namespace, pod.Name, strings.ReplaceAll(step.Query.SQL, "<tenant>", tenant))
	if dryRun {
		logger.Successf("dry run: would run prepare step %q against pod %s/%s", step.Name, pod.Namespace, pod.Name)
		return nil
	}
	proceed, err := appmigrate.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(),
		fmt.Sprintf("Run prepare step %q against pod %s/%s now? [y/N] ", step.Name, pod.Namespace, pod.Name))
	if err != nil {
		return err
	}
	if !proceed {
		return fmt.Errorf("prepare step %q not confirmed; %q not migrated", step.Name, app.ID)
	}

	logger.Actionf("running prepare step %q", step.Name)
	pod, stdout, stderr, err := step.RunQuery(cmd.Context(), popts, tenant, false)
	if err != nil {
		if stderr != "" {
			logger.Warningf("prepare step %q's stderr:\n%s", step.Name, stderr)
		}
		return fmt.Errorf("running prepare step %q: %w", step.Name, err)
	}
	if _, err := fmt.Fprint(cmd.OutOrStdout(), stdout); err != nil {
		return err
	}
	if stderr != "" {
		logger.Warningf("prepare step %q's stderr:\n%s", step.Name, stderr)
	}

	proceed, err = appmigrate.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(),
		fmt.Sprintf("prepare step %q ran against pod %s/%s; does the output above look correct? [y/N] ", step.Name, pod.Namespace, pod.Name))
	if err != nil {
		return err
	}
	if !proceed {
		return fmt.Errorf("prepare step %q not confirmed; %q not migrated", step.Name, app.ID)
	}
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
