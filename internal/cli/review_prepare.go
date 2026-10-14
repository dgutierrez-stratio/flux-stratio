package cli

import (
	"fmt"
	"strings"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/prepare"
)

// What a prepare step has to say goes through the same layout as an app's
// review (renderReview): a badge, a title, a hint on what it means, and a
// table or verbatim lines. A step is always an action for the operator — it
// changes the live cluster, or has to be done by hand, or was left undone —
// so these sections are all [ACTION]; the one-line progress of a step that
// runs (backed up, 1/2 delete …, complete) stays narrated like any command's.

// showSections prints sections the way an app's review is printed.
func showSections(logger *log.Logger, sections ...reviewSection) {
	logger.Block(renderReview(sections, styleFor(logger)))
}

// prepareTitle names the step and what it does.
func prepareTitle(step *prepare.Step) string {
	return fmt.Sprintf("prepare step %q: %s", step.Name, step.Description)
}

// declineHint is what answering no to a step's question does: the same
// sentence wherever a step asks.
func declineHint(app config.App) string {
	return fmt.Sprintf("Answer n at the question to skip only this step: the migration goes on without it, and you can do it by hand or run `flux stratio apps migrate %s` again later.", app.ID)
}

// inspectHint tells how to look at an object before a step acts on it.
const inspectHint = "To inspect an object first: kubectl get <kind> <name> -n <namespace> -o yaml."

// inspectVaultHint is inspectHint for a Vault entry, whose NAMESPACE is its
// Vault folder.
const inspectVaultHint = "A " + prepare.KindVaultPassword + " is a Vault entry in the NAMESPACE folder: `vault list <namespace>` in the Vault pod (flux stratio doctor checks it is reachable); the step never shows its value."

// prepareOpsSection lists the operations an automated step would perform.
// dryRun and yes say what the operator can expect next: nothing asked or
// changed, a run without a question, or the question.
func prepareOpsSection(step *prepare.Step, app config.App, ops []prepare.Operation, dryRun, yes bool) reviewSection {
	var hint string
	switch {
	case dryRun:
		hint = "Dry run: nothing is asked or changed. Without --dry-run it changes the live cluster after a backup of the app."
	case yes:
		hint = "Runs now (--yes), after a backup of the app, and changes the live cluster."
	default:
		hint = "Changes the live cluster, after a backup of the app. " + declineHint(app)
	}
	rows := make([][]string, len(ops))
	inspect := inspectHint
	for i, op := range ops {
		rows[i] = []string{op.Object.GetName(), op.Action, op.Object.GetKind(), op.Object.GetNamespace()}
		if op.Object.GetKind() == prepare.KindVaultPassword {
			inspect = inspectHint + " " + inspectVaultHint
		}
	}
	return reviewSection{
		Level: levelAction, Title: prepareTitle(step), Hint: hint + " " + inspect,
		Header: []string{"OBJECT", "ACTION", "KIND", "NAMESPACE"}, Rows: rows,
	}
}

// prepareManualSection is a step the operator does by hand, with its
// instructions as written.
func prepareManualSection(step *prepare.Step, app config.App, tenant string, dryRun bool) reviewSection {
	hint := "Done by hand: apps migrate asks whether you already did it. " + declineHint(app)
	if dryRun {
		hint = "Done by hand: without --dry-run, apps migrate asks whether you already did it."
	}
	return reviewSection{
		Level: levelAction, Title: prepareTitle(step), Hint: hint,
		Lines: strings.Split(strings.TrimRight(step.InstructionsFor(tenant), "\n"), "\n"),
	}
}

// prepareQuerySection is a step that runs a statement in a live pod, shown
// verbatim: it rewrites data, so it is never cut to fit a column.
func prepareQuerySection(step *prepare.Step, app config.App, tenant, pod string, dryRun bool) reviewSection {
	hint := fmt.Sprintf("Runs this against pod %s. It rewrites live data, so it asks before running and again after showing the real output; --yes answers neither. %s", pod, declineHint(app))
	if dryRun {
		hint = fmt.Sprintf("Dry run: nothing is asked or run. Without --dry-run it runs this against pod %s, asking before and after (--yes answers neither).", pod)
	}
	return reviewSection{
		Level: levelAction, Title: prepareTitle(step), Hint: hint,
		Lines: strings.Split(strings.TrimRight(strings.ReplaceAll(step.Query.SQL, "<tenant>", tenant), "\n"), "\n"),
	}
}

// preparedSection says what is left to do once a step has changed the
// cluster.
func preparedSection(step *prepare.Step) reviewSection {
	return reviewSection{
		Level: levelAction, Title: fmt.Sprintf("prepare step %q changed the live cluster", step.Name),
		Hint: "What it deleted or scaled down stays that way until the tenant file's change is committed, pushed and reconciled by Flux.",
	}
}

// skippedPrepareSection says a step was not run — the operator declined it,
// or --skip-prepare left it out — that the migration went on, and what is
// left undone. why is "declined" or "--skip-prepare".
func skippedPrepareSection(app config.App, tenant, why string) reviewSection {
	reason := why
	if why == "declined" {
		reason = "you declined it"
	}
	step := prepare.Find(app.Prepare)
	if step == nil {
		return reviewSection{Level: levelAction, Title: fmt.Sprintf("prepare step %q was NOT run (%s)", app.Prepare, reason)}
	}
	sec := reviewSection{
		Level: levelAction, Title: fmt.Sprintf("prepare step %q was NOT run (%s)", step.Name, reason),
		Hint: fmt.Sprintf("%s. The migration went on without it, and the patch is in the tenant file, but Flux shouldn't reconcile the app until the step is done: "+
			"do it by hand, or run `flux stratio apps migrate %s` again (it finds the patch up to date and goes straight to the step).", capitalize(step.Description), app.ID),
	}
	if !step.Automated && step.Query == nil {
		sec.Lines = strings.Split(strings.TrimRight(step.InstructionsFor(tenant), "\n"), "\n")
	}
	return sec
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// skippedPreparesSection lists, at the end of a run, every app whose step
// was left out, so none scrolls away unnoticed.
func skippedPreparesSection(apps []config.App) []reviewSection {
	if len(apps) == 0 {
		return nil
	}
	rows := make([][]string, len(apps))
	for i, a := range apps {
		rows[i] = []string{a.ID, a.Prepare, "flux stratio apps migrate " + a.ID}
	}
	return []reviewSection{{
		Level: levelAction, Title: fmt.Sprintf("%d app(s) still need their prepare step before Flux reconciles them", len(apps)),
		Header: []string{"APP", "STEP", "RUN"}, Rows: rows,
	}}
}
