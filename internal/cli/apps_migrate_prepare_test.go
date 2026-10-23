package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	"github.com/Stratio/flux-stratio/internal/vault/vaulttest"
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
	// backupDir is where the last backupFirst call said it backed up to.
	backupDir string
}

// runPrepare runs ensurePrepared for app against objs, answering prompts
// from stdin. execer is nil unless the step under test execs in a pod (a
// Query, or Vault).
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
	backupFirst := func() (string, error) {
		r.backups++
		if r.backupErr != nil {
			return "", r.backupErr
		}
		r.backupDir = t.TempDir()
		return r.backupDir, nil
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

// TestEnsurePrepared_DatarestDeletesTheIngressAndTheDeployment: the legacy
// Deployment goes with the Ingress, after the backup, and the plan names both
// (a Service of the same app stays).
func TestEnsurePrepared_DatarestDeletesTheIngressAndTheDeployment(t *testing.T) {
	dep := &unstructured.Unstructured{Object: map[string]any{}}
	dep.SetGroupVersionKind(gvkDeployment)
	dep.SetNamespace("stratio-datastores")
	dep.SetName("dg-datarest-pgi")
	dep.SetUID("uid-dep")
	dep.SetLabels(map[string]string{"cct.stratio.com/application_id": "dg-datarest-pgi.stratio-datastores"})

	r, err := runPrepare(t, datarestApp, noRead{t}, true, false, nil, nil, legacyIngress(), dep)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	e := flat(r.stderr.String())
	for _, row := range []string{"dg-datarest-pgi-admin.eosdev.int delete Ingress stratio-datastores", "dg-datarest-pgi delete Deployment stratio-datastores"} {
		if !strings.Contains(e, row) {
			t.Errorf("the plan lacks %q:\n%s", row, r.stderr.String())
		}
	}

	r, err = runPrepare(t, datarestApp, noRead{t}, false, true, nil, nil, legacyIngress(), dep)
	if err != nil {
		t.Fatalf("real run: %v", err)
	}
	if r.muts.deletes != 2 || r.backups != 1 || r.ingressExists(t) {
		t.Errorf("deletes = %d, backups = %d, ingress left = %v; want both objects deleted after one backup", r.muts.deletes, r.backups, r.ingressExists(t))
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

// genaiDB is a fake PgCluster primary: how many rows the chain table (and the
// endpoint one, when it exists) still has to rewrite, and what the SQL and
// the row copy answer. Every call is told apart by what it carries.
type genaiDB struct {
	chainRows, endpointRows int
	endpointExists          bool
	copyOut                 string
	// failCopy makes the row copy fail.
	failCopy bool
	*kubeclient.FakeExecer
}

func newGenaiDB(chainRows int) *genaiDB {
	db := &genaiDB{chainRows: chainRows, copyOut: "chain-1\t{}\n"}
	db.FakeExecer = &kubeclient.FakeExecer{Handle: func(call kubeclient.FakeExecCall) kubeclient.FakeExecResponse {
		switch {
		case strings.Contains(strings.Join(call.Command, " "), "--single-transaction"):
			return kubeclient.FakeExecResponse{Stdout: "UPDATE 1\nDELETE 1\n"}
		case strings.Contains(call.Stdin, "to_regclass"):
			if db.endpointExists {
				return kubeclient.FakeExecResponse{Stdout: "t\n"}
			}
			return kubeclient.FakeExecResponse{Stdout: "f\n"}
		case strings.Contains(call.Stdin, "SELECT count(*)"):
			n := db.chainRows
			if strings.Contains(call.Stdin, "endpoint") {
				n = db.endpointRows
			}
			return kubeclient.FakeExecResponse{Stdout: fmt.Sprintf("%d\n", n)}
		case strings.Contains(call.Stdin, "COPY ("):
			if db.failCopy {
				return kubeclient.FakeExecResponse{Err: errors.New("copy failed")}
			}
			return kubeclient.FakeExecResponse{Stdout: db.copyOut}
		}
		return kubeclient.FakeExecResponse{Stdout: "UPDATE 1\nDELETE 1\n"}
	}}
	return db
}

// writes are the calls that ran the step's SQL (not a read-only SELECT/COPY).
func (db *genaiDB) writes() int {
	n := 0
	for _, c := range db.Calls {
		if strings.Contains(strings.Join(c.Command, " "), "--single-transaction") {
			n++
		}
	}
	return n
}

// genaiApp is `apps migrate genai`'s app: the seeded catalog's family (genai-api
// and its genai-ui and genai-developer-proxy siblings) around the live
// genai-api.
var genaiApp = func() config.App {
	typ := config.SeedCatalog().Find("genai")
	return config.App{
		ID: "genai", Name: "GenAI genai", Prepare: typ.Prepare,
		Family: append([]config.Match{typ.Match}, typ.SiblingMatches()...),
		Live:   []config.ObjectRef{{GVK: gvkDeployment, Namespace: "stratio-genai", Name: "genai-api"}},
	}
}()

// TestEnsurePrepared_GenaiAlwaysAsks: apps migrate shows the SQL and its
// target pod and asks before running anything, then shows the real output
// and asks again; --yes answers neither, a no to either stops the
// migration, and --dry-run resolves the pod, counts the rows with a
// read-only SELECT and shows the SQL without running anything.
func TestEnsurePrepared_GenaiAlwaysAsks(t *testing.T) {
	app := genaiApp

	db := newGenaiDB(7)
	r, err := runPrepare(t, app, noRead{t}, true, false, nil, db, genaiMasterPod())
	if err != nil || !strings.Contains(r.stderr.String(), `UPDATE "genai-api.stratio-genai".chain`) {
		t.Errorf("dry run: err = %v, want the tenant's SQL shown and no question asked:\n%s", err, r.stderr.String())
	}
	if db.writes() != 0 || r.backups != 0 {
		t.Errorf("dry run: SQL runs = %d, backups = %d, want none", db.writes(), r.backups)
	}
	for _, c := range db.Calls {
		if strings.Contains(c.Stdin, "UPDATE") || strings.Contains(c.Stdin, "DELETE") {
			t.Errorf("dry run sent a statement that changes data: %q", c.Stdin)
		}
	}

	db = newGenaiDB(7)
	r, err = runPrepare(t, app, strings.NewReader("n\n"), false, true, nil, db, genaiMasterPod())
	if !errors.Is(err, errPrepareDeclined) {
		t.Errorf("--yes with a no before running: err = %v, want the step declined (--yes must not answer it)", err)
	}
	if db.writes() != 0 || r.backups != 0 {
		t.Errorf("declined before running: SQL runs = %d, backups = %d, want none", db.writes(), r.backups)
	}
	if !strings.Contains(r.stderr.String(), `UPDATE "genai-api.stratio-genai".chain`) {
		t.Errorf("the SQL wasn't shown before the question:\n%s", r.stderr.String())
	}

	db = newGenaiDB(7)
	if _, err := runPrepare(t, app, strings.NewReader("y\nn\n"), false, true, nil, db, genaiMasterPod()); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("no to the output: err = %v, want not confirmed", err)
	}
	if db.writes() != 1 {
		t.Errorf("SQL runs = %d, want 1", db.writes())
	}

	db = newGenaiDB(7)
	r, err = runPrepare(t, app, strings.NewReader("y\ny\n"), false, true, nil, db, genaiMasterPod())
	if err != nil {
		t.Errorf("confirmed: err = %v", err)
	}
	if !strings.Contains(r.stdout.String(), "UPDATE 1") {
		t.Errorf("stdout lacks the real query output:\n%s", r.stdout.String())
	}
}

// TestEnsurePrepared_GenaiBacksUpBeforeTheSQL is the regression for the
// missing backup: the workload backup is taken, and the rows the SQL
// rewrites are copied next to it, before the statement runs.
func TestEnsurePrepared_GenaiBacksUpBeforeTheSQL(t *testing.T) {
	db := newGenaiDB(7)
	db.endpointExists, db.endpointRows = true, 1
	order := []string{}
	inner := db.Handle
	db.Handle = func(c kubeclient.FakeExecCall) kubeclient.FakeExecResponse {
		if strings.Contains(strings.Join(c.Command, " "), "--single-transaction") {
			order = append(order, "sql")
		}
		return inner(c)
	}
	r, err := runPrepare(t, genaiApp, strings.NewReader("y\ny\n"), false, true, nil, db, genaiMasterPod())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if r.backups != 1 {
		t.Errorf("backups = %d, want 1 (workloads, before the SQL)", r.backups)
	}
	for _, f := range []string{"genai-stratio-chain.sql", "genai-stratio-endpoint.sql"} {
		b, err := os.ReadFile(filepath.Join(r.backupDir, "prepare", f))
		if err != nil {
			t.Errorf("data backup %s: %v", f, err)
			continue
		}
		if !strings.Contains(string(b), "COPY ") || !strings.Contains(string(b), "FROM stdin;") {
			t.Errorf("%s doesn't load the rows back:\n%s", f, b)
		}
	}
	if got := strings.Join(order, ","); got != "sql" {
		t.Errorf("SQL runs = %q, want one", got)
	}
}

// TestEnsurePrepared_GenaiFailedDataBackupRunsNoSQL: with no copy of the
// rows, no statement runs.
func TestEnsurePrepared_GenaiFailedDataBackupRunsNoSQL(t *testing.T) {
	db := newGenaiDB(7)
	db.failCopy = true
	_, err := runPrepare(t, genaiApp, strings.NewReader("y\ny\n"), false, true, nil, db, genaiMasterPod())
	if err == nil || !strings.Contains(err.Error(), "backing up the rows") {
		t.Errorf("err = %v, want the data backup failure", err)
	}
	if db.writes() != 0 {
		t.Errorf("SQL ran %d time(s) with no data backup", db.writes())
	}

	db = newGenaiDB(7)
	db.copyOut = ""
	if _, err := runPrepare(t, genaiApp, strings.NewReader("y\ny\n"), false, true, nil, db, genaiMasterPod()); err == nil || db.writes() != 0 {
		t.Errorf("an empty copy: err = %v, SQL runs = %d, want an error and none", err, db.writes())
	}
}

func TestEnsurePrepared_GenaiFailedWorkloadBackupRunsNoSQL(t *testing.T) {
	db := newGenaiDB(7)
	_, err := runPrepareWithBackup(t, genaiApp, strings.NewReader("y\ny\n"), false, true, nil, errors.New("no backup"), db, genaiMasterPod())
	if err == nil || db.writes() != 0 {
		t.Errorf("err = %v, SQL runs = %d, want the error and no SQL", err, db.writes())
	}
}

// TestEnsurePrepared_GenaiSatisfiedAsksNothing: no row left to rewrite and
// no legacy Deployment left: the second run changes nothing, asks nothing and
// says so at the normal log level.
func TestEnsurePrepared_GenaiSatisfiedAsksNothing(t *testing.T) {
	db := newGenaiDB(0)
	r, err := runPrepare(t, genaiApp, noRead{t}, false, false, nil, db, genaiMasterPod())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(r.stderr.String(), "already satisfied") {
		t.Errorf("no 'already satisfied' line:\n%s", r.stderr.String())
	}
	if db.writes() != 0 || r.backups != 0 || r.muts.deletes != 0 {
		t.Errorf("changed something: SQL %d, backups %d, deletes %d", db.writes(), r.backups, r.muts.deletes)
	}
}

// genaiLegacy is a legacy genai Deployment (a CCT app of its own).
func genaiLegacy(name string) *unstructured.Unstructured {
	u := cctDeployment(name, name, map[string]string{"cct.stratio.com/application_id": name + ".stratio-genai"})
	u.SetAnnotations(map[string]string{
		"cct.stratio.com/application_service": "genai",
		"cct.stratio.com/application_model":   name,
	})
	u.SetNamespace("stratio-genai")
	return u
}

// TestEnsurePrepared_GenaiDeletesTheLegacyDeploymentsAfterTheSQL: rows done,
// Deployments left: only the family's legacy Deployments are planned, --yes
// answers their question, and the run says it is deleting them.
func TestEnsurePrepared_GenaiDeletesTheLegacyDeploymentsAfterTheSQL(t *testing.T) {
	api, ui, other := genaiLegacy("genai-api"), genaiLegacy("genai-ui"), genaiLegacy("genai-other")
	db := newGenaiDB(0)
	r, err := runPrepare(t, genaiApp, noRead{t}, false, true, nil, db, genaiMasterPod(), api, ui, other)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if r.muts.deletes != 2 {
		t.Errorf("deletes = %d, want 2 (genai-api, genai-ui; never genai-other)", r.muts.deletes)
	}
	if db.writes() != 0 {
		t.Errorf("the SQL ran with no row left to rewrite")
	}
	if r.backups != 1 {
		t.Errorf("backups = %d, want 1 before the deletes", r.backups)
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

// litellmApp is `apps migrate litellm` on a legacy cluster.
var litellmApp = config.App{
	ID: "litellm", Name: "LiteLLM genai-litellm", Prepare: "prepare-litellm",
	Live: []config.ObjectRef{{GVK: gvkDeployment, Namespace: "stratio-genai", Name: "genai-litellm"}},
}

// TestEnsurePrepared_LitellmMovesTheVaultPasswords: an automated step gets
// the pod exec, so prepare-litellm plans its Vault moves next to the
// Deployment delete; the dry run lists them and writes nothing, --yes
// runs them, and no value is ever printed.
func TestEnsurePrepared_LitellmMovesTheVaultPasswords(t *testing.T) {
	const base = "userland/passwords/genai-litellm.stratio-genai"
	salt := strings.Repeat("s", 32)
	newServer := func() *vaulttest.Server {
		srv := vaulttest.New()
		srv.Entries[base+"/db_salt_key"] = map[string]string{"pass": salt}
		srv.Entries[base+"/keos_key_secret"] = map[string]string{"pass": strings.Repeat("k", 32)}
		srv.Entries[base+"/master_api_key"] = map[string]string{"pass": "sk-" + strings.Repeat("m", 10)}
		return srv
	}
	deployment := func() *unstructured.Unstructured {
		u := cctDeployment("genai-litellm", "genai-litellm", map[string]string{"cct.stratio.com/application_id": "genai-litellm.stratio-genai"})
		u.SetNamespace("stratio-genai")
		return u
	}
	objs := func() []client.Object {
		return append(vaulttest.Objects(vaulttest.Token, "vault-0"), deployment())
	}

	srv := newServer()
	r, err := runPrepare(t, litellmApp, noRead{t}, true, false, nil, srv.Exec, objs()...)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	e := flat(r.stderr.String())
	for _, want := range []string{
		"db-salt-key (from db_salt_key) copy VaultPassword userland/passwords/",
		"legacy/db_salt_key back up VaultPassword userland/passwords/",
		"genai-litellm delete Deployment stratio-genai",
		"is a Vault entry in the NAMESPACE folder",
	} {
		if !strings.Contains(e, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, r.stderr.String())
		}
	}
	if w := srv.Writes(); len(w) != 0 || r.muts.deletes != 0 {
		t.Errorf("dry run wrote %q and deleted %d object(s), want nothing", w, r.muts.deletes)
	}

	srv = newServer()
	r, err = runPrepare(t, litellmApp, noRead{t}, false, true, nil, srv.Exec, objs()...)
	if err != nil {
		t.Fatalf("--yes run: %v", err)
	}
	if got := srv.Entries[base+"/db-salt-key"]["pass"]; got != salt {
		t.Error("db-salt-key doesn't hold the legacy salt after the run")
	}
	if len(srv.Writes()) != 6 || r.muts.deletes != 1 || r.backups != 1 {
		t.Errorf("run wrote %d entr(y/ies), deleted %d object(s), took %d backup(s); want 6, 1, 1", len(srv.Writes()), r.muts.deletes, r.backups)
	}
	if strings.Contains(r.stderr.String()+r.stdout.String(), salt) {
		t.Error("the legacy salt was printed")
	}
}
