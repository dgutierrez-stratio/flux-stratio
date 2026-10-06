package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
)

var (
	gvkIngress    = schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"}
	gvkDeployment = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
)

// datarestApp is the app `apps migrate dg-datarest-pgi` resolves on eosdev.
var datarestApp = config.App{
	ID: "dg-datarest-pgi", Name: "DataRest dg-datarest-pgi", Prepare: "prepare-datarest",
	Live: []config.ObjectRef{{GVK: gvkDeployment, Namespace: "stratio-datastores", Name: "dg-datarest-pgi"}},
}

func legacyIngress() *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(gvkIngress)
	u.SetNamespace("stratio-datastores")
	u.SetName("dg-datarest-pgi-admin.eosdev.int")
	u.SetUID(types.UID("uid-ingress"))
	u.SetLabels(map[string]string{"cct.stratio.com/application_id": "dg-datarest-pgi.stratio-datastores"})
	_ = unstructured.SetNestedField(u.Object, "default-ingress-class", "spec", "ingressClassName")
	return u
}

// mutations counts the fake client's writes, so a test can assert none.
type mutations struct{ deletes, patches int }

func (m *mutations) funcs(deleteErr error) interceptor.Funcs {
	return interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			m.deletes++
			if deleteErr != nil {
				return deleteErr
			}
			return c.Delete(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
			m.patches++
			return c.Patch(ctx, obj, p, opts...)
		},
	}
}

// noRead is a stdin that fails the test if anything asks it a question.
type noRead struct{ t *testing.T }

func (r noRead) Read([]byte) (int, error) {
	r.t.Error("stdin was read: a confirmation was asked where none should be")
	return 0, io.EOF
}

type prepareRun struct {
	stdout, stderr bytes.Buffer
	muts           mutations
	c              client.Client
	// backups counts calls to the backupFirst hook; backupErr is what it
	// returns.
	backups   int
	backupErr error
}

// runPrepare runs ensurePrepared for app against objs, answering prompts
// from stdin. execer is nil unless the step under test has a Query.
func runPrepare(t *testing.T, app config.App, stdin io.Reader, dryRun, yes bool, deleteErr error, execer kubeclient.Execer, objs ...client.Object) (*prepareRun, error) {
	t.Helper()
	return runPrepareWithBackup(t, app, stdin, dryRun, yes, deleteErr, nil, execer, objs...)
}

// runPrepareWithBackup is runPrepare with backupFirst returning backupErr.
func runPrepareWithBackup(t *testing.T, app config.App, stdin io.Reader, dryRun, yes bool, deleteErr, backupErr error, execer kubeclient.Execer, objs ...client.Object) (*prepareRun, error) {
	t.Helper()
	scheme, err := kubeclient.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	r := &prepareRun{backupErr: backupErr}
	r.c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithInterceptorFuncs(r.muts.funcs(deleteErr)).Build()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(stdin)
	cmd.SetOut(&r.stdout)
	cmd.SetErr(&r.stderr)
	backupFirst := func() error {
		r.backups++
		return r.backupErr
	}
	err = ensurePrepared(cmd, app, "stratio", r.c, execer, backupFirst, log.New(&r.stderr, true), dryRun, yes)
	return r, err
}

func (r *prepareRun) ingressExists(t *testing.T) bool {
	t.Helper()
	ing := legacyIngress()
	err := r.c.Get(context.Background(), client.ObjectKeyFromObject(ing), ing)
	if err != nil && !kubeclient.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

// TestEnsurePrepared_DryRunShowsThePlanAndChangesNothing: the operator
// sees every operation (stderr), never the objects' manifests — kubectl is
// there for that — and nothing is asked or touched.
func TestEnsurePrepared_DryRunShowsThePlanAndChangesNothing(t *testing.T) {
	r, err := runPrepare(t, datarestApp, noRead{t}, true, false, nil, nil, legacyIngress())
	if err != nil {
		t.Fatalf("ensurePrepared: %v", err)
	}
	if r.muts.deletes+r.muts.patches != 0 || !r.ingressExists(t) || r.backups != 0 {
		t.Errorf("dry run mutated the cluster or took a backup: %+v, backups = %d", r.muts, r.backups)
	}
	const row = "dg-datarest-pgi-admin.eosdev.int delete Ingress stratio-datastores"
	if e := flat(r.stderr.String()); !strings.Contains(e, "[ACTION] prepare step \"prepare-datarest\"") || !strings.Contains(e, row) || !strings.Contains(e, "dry run: would run prepare step") {
		t.Errorf("stderr lacks the plan:\n%s", r.stderr.String())
	}
	if out := r.stdout.String(); out != "" {
		t.Errorf("a manifest or narration reached stdout:\n%s", out)
	}
	if !strings.Contains(flat(r.stderr.String()), "kubectl get <kind> <name> -n <namespace> -o yaml") {
		t.Errorf("no hint on how to inspect an object:\n%s", r.stderr.String())
	}
}

func TestEnsurePrepared_DeclinedChangesNothing(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", ""} {
		r, err := runPrepare(t, datarestApp, strings.NewReader(answer), false, false, nil, nil, legacyIngress())
		if !errors.Is(err, errPrepareDeclined) {
			t.Errorf("answer %q: err = %v, want the step declined (skipped, not a failure)", answer, err)
		}
		if r.muts.deletes != 0 || !r.ingressExists(t) {
			t.Errorf("answer %q: the Ingress was deleted without a yes", answer)
		}
	}
}

func TestEnsurePrepared_ConfirmedRunsExactlyThePlan(t *testing.T) {
	r, err := runPrepare(t, datarestApp, strings.NewReader("y\n"), false, false, nil, nil, legacyIngress())
	if err != nil {
		t.Fatalf("ensurePrepared: %v", err)
	}
	if r.muts.deletes != 1 || r.ingressExists(t) {
		t.Errorf("deletes = %d, Ingress still there = %v; want exactly the one planned delete", r.muts.deletes, r.ingressExists(t))
	}
	if r.backups != 1 {
		t.Errorf("backups = %d, want 1 taken before the delete", r.backups)
	}
	if !strings.Contains(r.stderr.String(), `prepare step "prepare-datarest" complete`) {
		t.Errorf("stderr:\n%s", r.stderr.String())
	}
	// The prompt is narration: with stdout redirected, it must still reach
	// the operator.
	const prompt = `Run these 1 operation(s) of prepare step "prepare-datarest" now? [y/N]`
	if !strings.Contains(r.stderr.String(), prompt) || strings.Contains(r.stdout.String(), "[y/N]") {
		t.Errorf("prompt went to stdout, not stderr:\nstdout: %s\nstderr: %s", r.stdout.String(), r.stderr.String())
	}
}

// TestEnsurePrepared_YesSkipsTheAutomatedPrompt: --yes answers the
// automated step's confirmation, but the plan is still listed first.
func TestEnsurePrepared_YesSkipsTheAutomatedPrompt(t *testing.T) {
	r, err := runPrepare(t, datarestApp, noRead{t}, false, true, nil, nil, legacyIngress())
	if err != nil {
		t.Fatalf("ensurePrepared: %v", err)
	}
	e := flat(r.stderr.String())
	if r.ingressExists(t) || !strings.Contains(e, "dg-datarest-pgi-admin.eosdev.int delete Ingress") || strings.Contains(e, "Answer n") || r.stdout.Len() != 0 {
		t.Errorf("--yes: want the plan listed (no manifests, no question hint) and applied:\n%s", r.stderr.String())
	}
}

// TestEnsurePrepared_FailedBackupChangesNothing: an automated step never
// deletes anything it couldn't back up first.
func TestEnsurePrepared_FailedBackupChangesNothing(t *testing.T) {
	r, err := runPrepareWithBackup(t, datarestApp, noRead{t}, false, true, nil, errors.New("disk full"), nil, legacyIngress())
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("err = %v, want the backup's error", err)
	}
	if r.muts.deletes != 0 || !r.ingressExists(t) {
		t.Errorf("the Ingress was deleted although its backup failed")
	}
}

func TestEnsurePrepared_SatisfiedIsSilent(t *testing.T) {
	r, err := runPrepare(t, datarestApp, noRead{t}, false, false, nil, nil)
	if err != nil {
		t.Fatalf("ensurePrepared: %v", err)
	}
	if r.backups != 0 {
		t.Errorf("an already-satisfied step took a backup")
	}
	if r.stdout.Len() != 0 || strings.Contains(r.stderr.String(), "is required") {
		t.Errorf("an already-satisfied step printed a plan:\nstdout: %s\nstderr: %s", r.stdout.String(), r.stderr.String())
	}
}

// TestEnsurePrepared_FailedOperationStopsTheRest: the DLC plan deletes
// the Ingress, then the Deployment; if the first fails, the second must
// not run and the error names the operation.
func TestEnsurePrepared_FailedOperationStopsTheRest(t *testing.T) {
	app := config.App{
		ID: "dlc-entity", Name: "DLC dlc-entity", Prepare: "prepare-dlc",
		Live: []config.ObjectRef{{GVK: gvkDeployment, Namespace: "stratio-dlc", Name: "dlc-entity"}},
	}
	ing := legacyIngress()
	ing.SetNamespace("stratio-dlc")
	ing.SetName("dlc-entity-dlc-entity.stratio.eosdev.int")
	ing.SetLabels(map[string]string{"cct.stratio.com/application_id": "dlc-entity.stratio-dlc"})
	dep := ing.DeepCopy()
	dep.SetGroupVersionKind(gvkDeployment)
	dep.SetName("dlc-entity")
	dep.SetUID("uid-dep")
	unstructured.RemoveNestedField(dep.Object, "spec")

	r, err := runPrepare(t, app, noRead{t}, false, true, errors.New("admission webhook denied"), nil, ing, dep)
	if err == nil || !strings.Contains(err.Error(), "delete Ingress stratio-dlc/dlc-entity-dlc-entity.stratio.eosdev.int") {
		t.Fatalf("err = %v, want it to name the failed operation", err)
	}
	if r.muts.deletes != 1 {
		t.Errorf("deletes attempted = %d, want 1 (the Deployment delete must not follow a failure)", r.muts.deletes)
	}
}

// genaiMasterPod is the tenant's PgCluster primary, carrying the same
// labels the live eosdev cluster's psql-0 pod does.
func genaiMasterPod() *corev1.Pod {
	pod := &corev1.Pod{}
	pod.Namespace, pod.Name = "stratio-datastores", "psql-0"
	pod.Labels = map[string]string{
		"pgcluster.stratio.com/pgcluster-name": "psql",
		"pgcluster.stratio.com/pgcluster-role": "master",
	}
	return pod
}

// TestEnsurePrepared_GenaiAlwaysAsks: apps migrate shows the SQL and its
// target pod and asks before running anything, then shows the real output
// and asks again; --yes answers neither, a no to either stops the
// migration, and --dry-run resolves the pod and shows the SQL without
// running anything.
func TestEnsurePrepared_GenaiAlwaysAsks(t *testing.T) {
	app := config.App{ID: "genai", Name: "GenAI genai", Prepare: "prepare-genai"}
	newExecer := func() *kubeclient.FakeExecer {
		return &kubeclient.FakeExecer{Response: kubeclient.FakeExecResponse{Stdout: "UPDATE 1\nDELETE 1\n"}}
	}

	execer := newExecer()
	r, err := runPrepare(t, app, noRead{t}, true, false, nil, execer, genaiMasterPod())
	if err != nil || !strings.Contains(r.stderr.String(), `UPDATE "genai-api.stratio-genai".chain`) {
		t.Errorf("dry run: err = %v, want the tenant's SQL shown and no question asked:\n%s", err, r.stderr.String())
	}
	if len(execer.Calls) != 0 {
		t.Errorf("dry run: execer was called: %v", execer.Calls)
	}

	execer = newExecer()
	r, err = runPrepare(t, app, strings.NewReader("n\n"), false, true, nil, execer, genaiMasterPod())
	if !errors.Is(err, errPrepareDeclined) {
		t.Errorf("--yes with a no before running: err = %v, want the step declined (--yes must not answer it)", err)
	}
	if len(execer.Calls) != 0 {
		t.Errorf("declined before running: execer was called: %v", execer.Calls)
	}
	if !strings.Contains(r.stderr.String(), `UPDATE "genai-api.stratio-genai".chain`) {
		t.Errorf("the SQL wasn't shown before the question:\n%s", r.stderr.String())
	}

	execer = newExecer()
	if _, err := runPrepare(t, app, strings.NewReader("y\nn\n"), false, true, nil, execer, genaiMasterPod()); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("no to the output: err = %v, want not confirmed", err)
	}
	if len(execer.Calls) != 1 {
		t.Errorf("execer calls = %d, want 1", len(execer.Calls))
	}

	execer = newExecer()
	r, err = runPrepare(t, app, strings.NewReader("y\ny\n"), false, true, nil, execer, genaiMasterPod())
	if err != nil {
		t.Errorf("confirmed: err = %v", err)
	}
	if !strings.Contains(r.stdout.String(), "UPDATE 1") {
		t.Errorf("stdout lacks the real query output:\n%s", r.stdout.String())
	}
}

func TestEnsurePrepared_UnknownStepFails(t *testing.T) {
	app := datarestApp
	app.Prepare = "prepare-nothing"
	if _, err := runPrepare(t, app, noRead{t}, false, true, nil, nil); err == nil || !strings.Contains(err.Error(), "unknown prepare step") {
		t.Errorf("err = %v, want unknown prepare step", err)
	}
}

// TestSaveThenPrepare_SaveFailureRunsNoPrepare: a prepare step may delete
// the live workload the patch came from, so it never runs unless the
// patch was saved first.
func TestSaveThenPrepare_SaveFailureRunsNoPrepare(t *testing.T) {
	var order []string
	save := func() error { order = append(order, "save"); return errors.New("read-only file system") }
	prepared := func() error { order = append(order, "prepare"); return nil }
	if err := saveThenPrepare(datarestApp, save, prepared); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("err = %v, want the save's error", err)
	}
	if strings.Join(order, ",") != "save" {
		t.Errorf("ran %v, want only the save", order)
	}

	order = nil
	save = func() error { order = append(order, "save"); return nil }
	prepared = func() error { order = append(order, "prepare"); return errors.New("not confirmed") }
	err := saveThenPrepare(datarestApp, save, prepared)
	if strings.Join(order, ",") != "save,prepare" {
		t.Errorf("ran %v, want save then prepare", order)
	}
	if err == nil || !strings.Contains(err.Error(), "now carries") || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("err = %v, want it to say the patch is saved and why prepare stopped", err)
	}
}

// virtualizerFamilyApp is `apps migrate virtualizer` on eosdev once the
// server is already Helm-managed: its live object is the GitOps
// Deployment, and Family is what the seeded catalog selects.
func virtualizerFamilyApp(t *testing.T) config.App {
	t.Helper()
	typ := config.SeedCatalog().Find("virtualizer")
	if typ == nil {
		t.Fatal("the seed catalog has no virtualizer type")
	}
	return config.App{
		ID: "virtualizer", Name: "Virtualizer virtualizer", Prepare: typ.Prepare,
		Family: append([]config.Match{typ.Match}, typ.SiblingMatches()...),
		Live:   []config.ObjectRef{{GVK: gvkDeployment, Namespace: "stratio-apps", Name: "virtualizer"}},
	}
}

func cctDeployment(name, model string, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(gvkDeployment)
	u.SetNamespace("stratio-apps")
	u.SetName(name)
	u.SetUID(types.UID("uid-" + name))
	u.SetAnnotations(map[string]string{
		"cct.stratio.com/application_service": "virtualizer",
		"cct.stratio.com/application_model":   model,
	})
	u.SetLabels(labels)
	return u
}

// TestEnsurePrepared_VirtualizerReplacesTheLegacySiblings: with the server
// already Helm-managed, the step finds the legacy monitor and ui by the
// catalog's sibling selectors — not by the app's own CCT id — and deletes
// their Deployments only: the GitOps Deployment, every ingress and another
// app stay.
func TestEnsurePrepared_VirtualizerReplacesTheLegacySiblings(t *testing.T) {
	ingress := func(name, id string) *unstructured.Unstructured {
		u := legacyIngress()
		u.SetNamespace("stratio-apps")
		u.SetName(name)
		u.SetUID(types.UID("uid-" + name))
		u.SetLabels(map[string]string{"cct.stratio.com/application_id": id})
		return u
	}
	objs := []client.Object{
		cctDeployment("virtualizer", "default", map[string]string{"helm.toolkit.fluxcd.io/name": "virtualizer"}),
		cctDeployment("virtualizer-monitor", "virtualizer-monitor", map[string]string{"cct.stratio.com/application_id": "virtualizer-monitor.stratio-apps"}),
		cctDeployment("virtualizer-ui", "virtualizer-ui", map[string]string{"cct.stratio.com/application_id": "virtualizer-ui.stratio-apps"}),
		ingress("virtualizer-monitor-virtualizer.stratio.eosdev", "virtualizer-monitor.stratio-apps"),
		ingress("virtualizer-ui-virtualizer.stratio.eosdev", "virtualizer-ui.stratio-apps"),
		ingress("discovery-discovery.stratio.eosdev", "discovery.stratio-apps"),
	}

	r, err := runPrepare(t, virtualizerFamilyApp(t), noRead{t}, true, false, nil, nil, objs...)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.muts.deletes != 0 {
		t.Errorf("--dry-run deleted %d object(s)", r.muts.deletes)
	}
	e := flat(r.stderr.String())
	for _, want := range []string{
		"virtualizer-monitor delete Deployment stratio-apps",
		"virtualizer-ui delete Deployment stratio-apps",
	} {
		if !strings.Contains(e, want) {
			t.Errorf("the plan doesn't list %q:\n%s", want, r.stderr.String())
		}
	}
	for _, not := range []string{"Ingress", "virtualizer delete Deployment", "discovery"} {
		if strings.Contains(e, not) {
			t.Errorf("the plan reaches %q:\n%s", not, r.stderr.String())
		}
	}

	r, err = runPrepare(t, virtualizerFamilyApp(t), noRead{t}, false, true, nil, nil, objs...)
	if err != nil {
		t.Fatalf("--yes: %v", err)
	}
	if r.muts.deletes != 2 || r.backups != 1 {
		t.Errorf("deletes = %d, backups = %d; want 2 deletes after exactly one backup", r.muts.deletes, r.backups)
	}
}

// TestEnsurePrepared_AsksWhatNoDoes: every prepare question is preceded by
// what answering no does — skip that step, go on with the migration.
func TestEnsurePrepared_AsksWhatNoDoes(t *testing.T) {
	r, err := runPrepare(t, datarestApp, strings.NewReader("n\n"), false, false, nil, nil, legacyIngress())
	if !errors.Is(err, errPrepareDeclined) {
		t.Fatalf("err = %v, want declined", err)
	}
	e := flat(r.stderr.String())
	note := strings.Index(e, "Answer n at the question to skip only this step: the migration goes on without it")
	prompt := strings.Index(e, "[y/N]")
	if note < 0 || prompt < 0 || note > prompt {
		t.Errorf("the note about n must come before the question:\n%s", r.stderr.String())
	}
	if !strings.Contains(e, "flux stratio apps migrate dg-datarest-pgi") {
		t.Errorf("the note doesn't say how to run the step later:\n%s", r.stderr.String())
	}
}

// TestSkipIfDeclined: a declined step is a warning and a line in the
// summary, never an error; any other failure still stops the app.
func TestSkipIfDeclined(t *testing.T) {
	var out bytes.Buffer
	logger := log.New(&out, false)
	var skipped []config.App
	flags := migrateFlags{onSkippedPrepare: func(a config.App) { skipped = append(skipped, a) }}
	app := config.App{ID: "dg-hdfs-agent", Name: "Governance agent dg-hdfs-agent", Prepare: "prepare-dg-agent"}

	if err := skipIfDeclined(errPrepareDeclined, logger, app, "stratio", flags); err != nil {
		t.Fatalf("a declined step returned %v, want nil: the migration goes on", err)
	}
	if len(skipped) != 1 || skipped[0].ID != "dg-hdfs-agent" {
		t.Errorf("skipped = %v, want the app recorded for the summary", skipped)
	}
	for _, want := range []string{`prepare step "prepare-dg-agent" was NOT run (you declined it)`, "flux stratio apps migrate dg-hdfs-agent"} {
		if !strings.Contains(flat(out.String()), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	boom := errors.New("boom")
	if err := skipIfDeclined(boom, logger, app, "stratio", flags); !errors.Is(err, boom) || len(skipped) != 1 {
		t.Errorf("a real failure: err = %v, skipped = %d; want it passed through and nothing recorded", err, len(skipped))
	}
}

// flat collapses every run of whitespace, so a sentence the layout wrapped
// onto several lines or a table row padded into columns can be searched for.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }
