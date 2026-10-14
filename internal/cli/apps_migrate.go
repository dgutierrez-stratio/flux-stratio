package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// errPrepareDeclined is a prepare step the operator answered no to: not a
// failure and not an interruption — the migration goes on without the step,
// which is left for them to do by hand or by running apps migrate again. It
// never leaves migrateOne: the prepared closure turns it into a warning and
// the end-of-run summary.
var errPrepareDeclined = errors.New("prepare step declined")

func newAppsMigrateCommand() *cobra.Command {
	var all bool
	var continueOnError bool
	var as string
	var flags migrateFlags

	cmd := &cobra.Command{
		Use:     "migrate [name]",
		Short:   "Write an app's patch into the tenant file (and run its prepare step)",
		Long:    appsMigrateLong,
		Example: appsMigrateExample,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppsMigrate(cmd, args, all, continueOnError, as, flags)
		},
	}
	addAsFlag(cmd, &as)
	cmd.Flags().StringVar(&flags.baseline, "baseline", "",
		"take the legacy values from a backup instead of the live cluster, for an app Flux already reset: latest, a backup directory or the backups root")
	cmd.Flags().StringVar(&flags.dir, "dir", "", "backups root, where --baseline latest looks (default: a backups/ directory next to the catalog file)")
	cmd.Flags().BoolVar(&all, "all", false, "migrate every classified app whose component the tenant file declares, in dependency order")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "show the tenant-file change and each prepare step's operations, but write and run nothing")
	cmd.Flags().BoolVarP(&flags.yes, "yes", "y", false, "do not ask to confirm (and fail instead of asking which type/entry); an app with warnings still stops unless --accept-warnings")
	cmd.Flags().BoolVar(&flags.acceptWarnings, "accept-warnings", false, "with --yes, migrate an app even when it has warnings (lost values, differences to review, risky resources/image/replicas/selector)")
	cmd.Flags().BoolVar(&flags.skipPrepare, "skip-prepare", false, "write the patch but run no prepare step (they are listed at the end; run apps migrate <app> later for just the step)")
	cmd.Flags().BoolVar(&continueOnError, "continue-on-error", false, "with --all, carry on after an app fails (default: stop at the first failure)")
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
	review := chartReview{
		Unmapped: planned.UnmappedDiffs, Missing: planned.MissingWorkloads, LiveOnly: planned.LiveOnly, Native: planned.Native,
		SpecDiffs: planned.SpecDiffs, Carried: planned.CarriedResources, Excluded: planned.Excluded, Source: liveSource(resolvedBaseline),
		Prepare: app.Prepare,
	}
	review.Extra = slices.Concat(
		fluxManagedSection(app, planned.FluxManagedBy),
		sizeSection(repos, cluster, tenant, app.Object, planned.LiveSize),
		unresolvedDepsSection(app, planned.UnresolvedDeps),
		legacyAgentSection(app, planned.LegacyAgentPatches),
		droppedSection(planned.DroppedFromExisting),
	)
	reportChartReview(logger, review)
	// The patch is planned, and saved, before app's prepare step runs: a
	// prepare step may delete the very live workload the patch is read
	// from (prepare-dlc), so a patch saved only afterwards would be lost
	// with it if the save failed. Saved first, a prepare step that fails
	// or is declined leaves a tenant file already carrying the patch, and
	// re-running apps migrate finds it up to date and goes straight to the
	// prepare step. backupFirst captures the live state before an
	// automated prepare step changes any of it.
	backupFirst := func() (string, error) {
		return backupBeforePrepare(cmd, app, repos, c, idx, logger, dirFlag)
	}
	prepared := func() error {
		if app.Prepare == "" {
			return nil
		}
		if flags.skipPrepare {
			skipPrepare(logger, app, tenant, flags, "--skip-prepare")
			return nil
		}
		return skipIfDeclined(ensurePrepared(cmd, app, tenant, c, execer, backupFirst, logger, dryRun, yes), logger, app, tenant, flags)
	}
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
				if errors.Is(err, errNotConfirmed) {
					// The only thing left to do here is the prepare step: no
					// is declining it, and the migration goes on without it.
					skipPrepare(logger, app, tenant, flags, "declined")
					return nil
				}
				return err
			}
		}
		return prepared()
	}

	if err := confirmWarnings(cmd, app, warned, flags, "Show the patch"); err != nil {
		return err
	}

	if err := ui.FileDiffWith(cmd.OutOrStdout(), planned.Before, planned.After, diffOptions(cmd, tenantFileCurrent, tenantFileWithNew, ui.SectionsYAML)); err != nil {
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
// back from wherever the operator thought to save them. It returns the
// directory the backup went to, so a step can add what the workload backup
// doesn't hold (prepare-genai's data rows) next to it.
func backupBeforePrepare(cmd *cobra.Command, app config.App, repos config.RepoPaths, c client.Client, idx *discovery.Index, logger *log.Logger, dirFlag string) (string, error) {
	backupsDir, err := resolveBackupsDir(dirFlag)
	if err != nil {
		return "", err
	}
	result, err := backup.Run(cmd.Context(), backup.Options{
		Repos: repos, App: app, Runner: runner.Exec{}, Client: c, Index: idx, Dir: backupsDir, Log: logger,
	})
	if err != nil {
		return "", fmt.Errorf("backing up %q before its prepare step: %w", app.Name, err)
	}
	if len(result.Files) == 0 {
		return "", fmt.Errorf("backing up %q before its prepare step captured nothing; back it up by hand (apps backup) first", app.Name)
	}
	logger.Successf("backed up %q to %s before its prepare step", app.Name, result.Dir)
	return result.Dir, nil
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

// skipIfDeclined turns a prepare step the operator answered no to into a
// skipped one — a warning and an entry in the end-of-run summary, no error —
// and passes any other result through.
func skipIfDeclined(err error, logger *log.Logger, app config.App, tenant string, flags migrateFlags) error {
	if errors.Is(err, errPrepareDeclined) {
		skipPrepare(logger, app, tenant, flags, "declined")
		return nil
	}
	return err
}

// skipPrepare leaves app's prepare step out — --skip-prepare, or the
// operator answering no to it (why) — tells them what that leaves undone,
// and records it for the summary at the end of the run. The migration goes
// on either way.
func skipPrepare(logger *log.Logger, app config.App, tenant string, flags migrateFlags, why string) {
	reportSkippedPrepare(logger, app, tenant, why)
	if flags.onSkippedPrepare != nil {
		flags.onSkippedPrepare(app)
	}
}

// reportSkippedPrepare says app's prepare step was NOT run (why: the
// --skip-prepare flag, or "declined" at its prompt), that the migration went
// on without it, and what that leaves undone.
func reportSkippedPrepare(logger *log.Logger, app config.App, tenant, why string) {
	showSections(logger, skippedPrepareSection(app, tenant, why))
}

// reportSkippedPrepares lists, at the end of a run, every app whose prepare
// step was left out (--skip-prepare, or declined), so they can't scroll away
// unnoticed.
func reportSkippedPrepares(logger *log.Logger, apps []config.App) {
	showSections(logger, skippedPreparesSection(apps)...)
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
// by hand and always asks the same way. Answering no to any of those
// questions is errPrepareDeclined: the step is skipped, never the whole
// migration (the one exception is the Query step's second question, asked
// after the SQL already ran, whose no is a real failure).
func ensurePrepared(cmd *cobra.Command, app config.App, tenant string, c client.Client, execer kubeclient.Execer, backupFirst func() (string, error), logger *log.Logger, dryRun, yes bool) error {
	step := prepare.Find(app.Prepare)
	if step == nil {
		return fmt.Errorf("app %q declares unknown prepare step %q", app.ID, app.Prepare)
	}

	if step.Query != nil {
		return ensurePreparedQuery(cmd, *step, app, tenant, c, execer, backupFirst, logger, dryRun, yes)
	}

	if !step.Automated {
		showSections(logger, prepareManualSection(step, app, tenant, dryRun))
		if dryRun {
			logger.Successf("dry run: would ask whether you have completed prepare step %q", step.Name)
			return nil
		}
		proceed, err := appmigrate.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(), fmt.Sprintf("Have you already completed %q? [y/N] ", step.Name))
		if err != nil {
			return err
		}
		if !proceed {
			return errPrepareDeclined
		}
		return nil
	}

	popts := prepareOptions(app, tenant, c, execer, logger)
	ops, err := step.Plan(cmd.Context(), popts)
	if err != nil {
		return fmt.Errorf("planning prepare step %q: %w", step.Name, err)
	}
	if len(ops) == 0 {
		logger.Successf("prepare step %q already satisfied", step.Name)
		return nil
	}

	showSections(logger, prepareOpsSection(step, app, ops, dryRun, yes))
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
			return errPrepareDeclined
		}
	}
	if backupFirst == nil {
		return fmt.Errorf("prepare step %q changes live objects, and no backup of %q could be taken first", step.Name, app.ID)
	}
	if _, err := backupFirst(); err != nil {
		return err
	}
	for i, op := range ops {
		logger.Waitingf("%d/%d %s", i+1, len(ops), op)
		if err := op.Apply(cmd.Context(), c); err != nil {
			return fmt.Errorf("running prepare step %q: %w", step.Name, err)
		}
	}
	logger.Successf("prepare step %q complete", step.Name)
	showSections(logger, preparedSection(step))
	return nil
}

// prepareOptions is what a prepare step is given about app: its live
// object, and the selector for every legacy workload of its family.
func prepareOptions(app config.App, tenant string, c client.Client, execer kubeclient.Execer, logger *log.Logger) prepare.Options {
	return prepare.Options{
		TenantName: tenant, LiveName: app.LiveName(), LiveNamespace: app.LiveNamespace(),
		IsLegacy: func(obj *unstructured.Unstructured) bool { return components.MatchesAny(app.Family, obj) },
		Client:   c, Execer: execer, Log: logger,
	}
}

// ensurePreparedQuery runs a Query step (prepare-genai). It counts, with
// read-only SELECTs (also under --dry-run), the rows its SQL would still
// change, and plans the Deployment deletes it carries (step.Plan); with
// neither left, the step is satisfied and asks nothing. Otherwise it shows
// the SQL — verbatim, with its target pod — and the operations, and asks
// before running anything; --dry-run stops there.
//
// A run takes the app's backup and copies the rows the SQL changes (both
// before the SQL, and a copy that fails or comes back empty stops before any
// statement), runs the SQL, shows the real captured output and asks again
// whether it looks right. Neither question is skipped by --yes: the query
// rewrites live application data, which no Kubernetes API can verify or
// undo. The Python client's own version of this same check exited 0
// unconditionally, marking the step migrated on faith whether or not the
// SQL was ever run. Last come the step's deletes, asked like any
// automated step's (--yes answers it). They come after the SQL: the SQL is
// one transaction, so a failure leaves the cluster as it was, while a
// deleted Deployment is the less reversible part.
func ensurePreparedQuery(cmd *cobra.Command, step prepare.Step, app config.App, tenant string, c client.Client, execer kubeclient.Execer, backupFirst func() (string, error), logger *log.Logger, dryRun, yes bool) error {
	popts := prepareOptions(app, tenant, c, execer, logger)

	pod, _, _, err := step.RunQuery(cmd.Context(), popts, tenant, true)
	if err != nil {
		return fmt.Errorf("resolving prepare step %q's target pod: %w", step.Name, err)
	}
	pending, err := step.Pending(cmd.Context(), popts, tenant, pod)
	if err != nil {
		return err
	}
	var ops []prepare.Operation
	if step.Plan != nil {
		if ops, err = step.Plan(cmd.Context(), popts); err != nil {
			return fmt.Errorf("planning prepare step %q: %w", step.Name, err)
		}
	}
	if pending == 0 && len(ops) == 0 {
		logger.Successf("prepare step %q already satisfied", step.Name)
		return nil
	}

	podName := pod.Namespace + "/" + pod.Name
	var sections []reviewSection
	if pending > 0 {
		sections = append(sections, prepareQuerySection(&step, app, tenant, podName, pending, dryRun))
	}
	if len(ops) > 0 {
		sections = append(sections, prepareOpsSection(&step, app, ops, dryRun, yes))
	}
	showSections(logger, sections...)
	if dryRun {
		logger.Successf("dry run: would run prepare step %q (%d row(s) to rewrite in pod %s, %d operation(s) above)", step.Name, pending, podName, len(ops))
		return nil
	}

	backedUp := false
	takeBackup := func() (string, error) {
		if backupFirst == nil {
			return "", fmt.Errorf("prepare step %q changes live objects, and no backup of %q could be taken first", step.Name, app.ID)
		}
		dir, err := backupFirst()
		if err == nil {
			backedUp = true
		}
		return dir, err
	}

	if pending > 0 {
		proceed, err := appmigrate.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(),
			fmt.Sprintf("Run prepare step %q against pod %s now? [y/N] ", step.Name, podName))
		if err != nil {
			return err
		}
		if !proceed {
			return errPrepareDeclined
		}
		dir, err := takeBackup()
		if err != nil {
			return err
		}
		if err := backupRows(cmd.Context(), logger, step, popts, tenant, pod, dir); err != nil {
			return err
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
	}

	if len(ops) == 0 {
		return nil
	}
	if !yes {
		proceed, err := appmigrate.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(), fmt.Sprintf("Run these %d operation(s) of prepare step %q now? [y/N] ", len(ops), step.Name))
		if err != nil {
			return err
		}
		if !proceed {
			return errPrepareDeclined
		}
	}
	if !backedUp {
		if _, err := takeBackup(); err != nil {
			return err
		}
	}
	for i, op := range ops {
		logger.Waitingf("%d/%d %s", i+1, len(ops), op)
		if err := op.Apply(cmd.Context(), c); err != nil {
			return fmt.Errorf("running prepare step %q: %w", step.Name, err)
		}
	}
	logger.Successf("prepare step %q complete", step.Name)
	showSections(logger, preparedSection(&step))
	return nil
}

// backupRows saves the rows a Query step's SQL is about to change under
// <dir>/prepare/, next to the workload backup (which holds none of them).
func backupRows(ctx context.Context, logger *log.Logger, step prepare.Step, popts prepare.Options, tenant string, pod *corev1.Pod, dir string) error {
	copies, err := step.DumpRows(ctx, popts, tenant, pod)
	if err != nil {
		return fmt.Errorf("backing up the rows prepare step %q changes: %w", step.Name, err)
	}
	if len(copies) == 0 {
		return nil
	}
	target := filepath.Join(dir, "prepare")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("backing up the rows prepare step %q changes: %w", step.Name, err)
	}
	for _, d := range copies {
		path := filepath.Join(target, d.File)
		if err := os.WriteFile(path, []byte(d.Content), 0o600); err != nil {
			return fmt.Errorf("backing up the rows prepare step %q changes: %w", step.Name, err)
		}
		logger.Successf("backed up %d row(s) prepare step %q changes to %s", d.Rows, step.Name, path)
	}
	return nil
}
