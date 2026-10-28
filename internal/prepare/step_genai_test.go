package prepare

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Stratio/flux-stratio/internal/kubeclient"
)

// pgPod is a pod carrying the tenant's shared PgCluster's own labels — the
// same ones the live eosdev cluster's psql-0/1/2 pods carry.
func pgPod(name, role string) *corev1.Pod {
	pod := &corev1.Pod{}
	pod.Namespace = "stratio-datastores"
	pod.Name = name
	pod.Labels = map[string]string{
		"pgcluster.stratio.com/pgcluster-name": "psql",
		"pgcluster.stratio.com/pgcluster-role": role,
	}
	return pod
}

func TestGenaiRunQuery_FindsThePrimaryAndSubstitutesTenant(t *testing.T) {
	c := fakeClient(t, pgPod("psql-0", "master"), pgPod("psql-1", "replica"), pgPod("psql-2", "replica"))
	execer := &kubeclient.FakeExecer{Response: kubeclient.FakeExecResponse{Stdout: "UPDATE 1\nDELETE 1\n"}}

	pod, stdout, _, err := stepGenAI.RunQuery(context.Background(), Options{Client: c, Execer: execer}, "stratio", false)
	if err != nil {
		t.Fatalf("RunQuery: %v", err)
	}
	if pod.Name != "psql-0" {
		t.Errorf("resolved pod = %q, want psql-0 (the master)", pod.Name)
	}
	if stdout != "UPDATE 1\nDELETE 1\n" {
		t.Errorf("stdout = %q, want the execer's response passed through", stdout)
	}
	if len(execer.Calls) != 1 {
		t.Fatalf("execer calls = %d, want 1", len(execer.Calls))
	}
	call := execer.Calls[0]
	if call.Namespace != "stratio-datastores" || call.Pod != "psql-0" || call.Container != "postgresql" {
		t.Errorf("exec target = %s/%s (%s), want stratio-datastores/psql-0 (postgresql)", call.Namespace, call.Pod, call.Container)
	}
	// The genai schemas live in the "genai" PgDatabase, not the cluster's
	// own "psql" database — verified live: connecting to "psql" fails
	// with "relation ... does not exist" (the schema simply isn't there).
	if want := "genai"; len(call.Command) == 0 || call.Command[len(call.Command)-1] != want {
		t.Errorf("Command = %v, want it to target the %q database", call.Command, want)
	}
	// Without ON_ERROR_STOP psql exits 0 after a failed statement, and
	// without --single-transaction a failed DELETE leaves the UPDATE in.
	cmd := strings.Join(call.Command, " ")
	for _, want := range []string{"-v ON_ERROR_STOP=1", "--single-transaction"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("Command = %v, want it to carry %q", call.Command, want)
		}
	}
	for _, want := range []string{`UPDATE "genai-api.stratio-genai".chain`, `to_regclass('"genai-gateway.stratio-genai".endpoint')`} {
		if !strings.Contains(call.Stdin, want) {
			t.Errorf("SQL sent to stdin lacks %q:\n%s", want, call.Stdin)
		}
	}
	if strings.Contains(call.Stdin, "<tenant>") {
		t.Errorf("SQL still carries the <tenant> placeholder:\n%s", call.Stdin)
	}
}

func TestGenaiRunQuery_DryRunResolvesThePodWithoutExecuting(t *testing.T) {
	c := fakeClient(t, pgPod("psql-0", "master"))
	execer := &kubeclient.FakeExecer{}

	pod, stdout, stderr, err := stepGenAI.RunQuery(context.Background(), Options{Client: c, Execer: execer}, "stratio", true)
	if err != nil {
		t.Fatalf("RunQuery dry-run: %v", err)
	}
	if pod == nil || pod.Name != "psql-0" {
		t.Errorf("dry-run should still resolve the target pod, got %v", pod)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("dry-run must not execute anything, got stdout=%q stderr=%q", stdout, stderr)
	}
	if len(execer.Calls) != 0 {
		t.Errorf("dry-run called the execer: %v", execer.Calls)
	}
}

func TestGenaiRunQuery_NoPrimaryFound(t *testing.T) {
	c := fakeClient(t, pgPod("psql-1", "replica"))
	_, _, _, err := stepGenAI.RunQuery(context.Background(), Options{Client: c, Execer: &kubeclient.FakeExecer{}}, "stratio", false)
	if err == nil {
		t.Error("no pod carrying the master label: got nil error")
	}
}

func TestGenaiRunQuery_MoreThanOnePrimaryFound(t *testing.T) {
	c := fakeClient(t, pgPod("psql-0", "master"), pgPod("psql-3", "master"))
	_, _, _, err := stepGenAI.RunQuery(context.Background(), Options{Client: c, Execer: &kubeclient.FakeExecer{}}, "stratio", false)
	if err == nil {
		t.Error("two pods carrying the master label: got nil error")
	}
}

func TestGenaiRunQuery_ExecErrorIsWrapped(t *testing.T) {
	c := fakeClient(t, pgPod("psql-0", "master"))
	execer := &kubeclient.FakeExecer{Response: kubeclient.FakeExecResponse{
		Stderr: "syntax error",
		Err:    errors.New("command terminated with exit code 1"),
	}}

	_, _, stderr, err := stepGenAI.RunQuery(context.Background(), Options{Client: c, Execer: execer}, "stratio", false)
	if err == nil || !strings.Contains(err.Error(), "prepare-genai") {
		t.Errorf("err = %v, want it to name the step", err)
	}
	if stderr != "syntax error" {
		t.Errorf("stderr = %q, want it surfaced even on a failed run", stderr)
	}
}

// TestGenAISQL_UpdateOnlyRewritesRowsHoldingTheOldName: the UPDATE carries
// the legacy script's WHERE, so a re-run (or a database already migrated)
// rewrites no row.
func TestGenAISQL_UpdateOnlyRewritesRowsHoldingTheOldName(t *testing.T) {
	update := genaiSQL[:strings.Index(genaiSQL, "DO $$")]
	for _, col := range []string{"chain_params", "worker_config", "invoke_schema"} {
		if !strings.Contains(update, col+"::text LIKE '%dg-businessglossary-api%'") {
			t.Errorf("UPDATE has no WHERE guard on %s:\n%s", col, update)
		}
	}
}

// genaiReader answers the step's read-only SELECTs and COPYs: how many rows
// each table has left, and whether the endpoint table exists.
func genaiReader(chainRows, endpointRows int, endpointExists bool) *kubeclient.FakeExecer {
	return &kubeclient.FakeExecer{Handle: func(call kubeclient.FakeExecCall) kubeclient.FakeExecResponse {
		isEndpoint := strings.Contains(call.Stdin, "endpoint")
		switch {
		case strings.Contains(call.Stdin, "to_regclass"):
			if endpointExists {
				return kubeclient.FakeExecResponse{Stdout: "t\n"}
			}
			return kubeclient.FakeExecResponse{Stdout: "f\n"}
		case strings.Contains(call.Stdin, "SELECT count(*)"):
			n := chainRows
			if isEndpoint {
				n = endpointRows
			}
			return kubeclient.FakeExecResponse{Stdout: strconv.Itoa(n) + "\n"}
		case strings.Contains(call.Stdin, "COPY ("):
			return kubeclient.FakeExecResponse{Stdout: "row-1\t{}\n"}
		}
		return kubeclient.FakeExecResponse{Err: errors.New("unexpected statement: " + call.Stdin)}
	}}
}

func TestGenaiPending_CountsTheRowsStillToRewrite(t *testing.T) {
	c := fakeClient(t, pgPod("psql-0", "master"))
	for _, tt := range []struct {
		name                  string
		chain, endpoint, want int
		endpointExists        bool
	}{
		{"rewrite pending", 7, 0, 7, false},
		{"endpoint row pending too", 7, 1, 8, true},
		{"all done", 0, 0, 0, false},
		{"only the endpoint row left", 0, 1, 1, true},
		// The endpoint table is gone (genai-gateway superseded by litellm):
		// whatever a count would say, it counts for nothing.
		{"endpoint table absent", 0, 5, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			execer := genaiReader(tt.chain, tt.endpoint, tt.endpointExists)
			pod, _, _, err := stepGenAI.RunQuery(context.Background(), Options{Client: c, Execer: execer}, "stratio", true)
			if err != nil {
				t.Fatal(err)
			}
			got, err := stepGenAI.Pending(context.Background(), Options{Client: c, Execer: execer}, "stratio", pod)
			if err != nil || got != tt.want {
				t.Errorf("Pending = %d, %v, want %d", got, err, tt.want)
			}
			for _, call := range execer.Calls {
				if strings.Contains(call.Stdin, "UPDATE") || strings.Contains(call.Stdin, "DELETE") {
					t.Errorf("Pending sent a statement that changes data: %q", call.Stdin)
				}
				if strings.Contains(strings.Join(call.Command, " "), "--single-transaction") {
					t.Errorf("Pending ran the write command: %v", call.Command)
				}
			}
		})
	}
}

func TestGenaiPending_ReadErrorFails(t *testing.T) {
	c := fakeClient(t, pgPod("psql-0", "master"))
	execer := &kubeclient.FakeExecer{Response: kubeclient.FakeExecResponse{Err: errors.New("connection refused")}}
	pod, _, _, _ := stepGenAI.RunQuery(context.Background(), Options{Client: c, Execer: execer}, "stratio", true)
	if _, err := stepGenAI.Pending(context.Background(), Options{Client: c, Execer: execer}, "stratio", pod); err == nil {
		t.Error("a failing read: got nil error, want it surfaced rather than read as nothing pending")
	}
	execer = &kubeclient.FakeExecer{Response: kubeclient.FakeExecResponse{Stdout: "garbage"}}
	if _, err := stepGenAI.Pending(context.Background(), Options{Client: c, Execer: execer}, "stratio", pod); err == nil {
		t.Error("an unreadable count: got nil error")
	}
}

func TestGenaiDumpRows_CopiesTheRowsTheSQLChanges(t *testing.T) {
	c := fakeClient(t, pgPod("psql-0", "master"))
	execer := genaiReader(7, 1, true)
	pod, _, _, _ := stepGenAI.RunQuery(context.Background(), Options{Client: c, Execer: execer}, "stratio", true)

	copies, err := stepGenAI.DumpRows(context.Background(), Options{Client: c, Execer: execer}, "stratio", pod)
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) != 2 || copies[0].File != "genai-stratio-chain.sql" || copies[1].File != "genai-stratio-endpoint.sql" {
		t.Fatalf("copies = %+v, want the chain and endpoint files, in that order", copies)
	}
	chain := copies[0]
	if chain.Rows != 7 {
		t.Errorf("chain copy rows = %d, want 7", chain.Rows)
	}
	for _, want := range []string{`COPY "genai-api.stratio-genai".chain FROM stdin;`, "row-1\t{}\n", "\\.\n", "dg-businessglossary-api"} {
		if !strings.Contains(chain.Content, want) {
			t.Errorf("chain copy lacks %q:\n%s", want, chain.Content)
		}
	}
	if strings.Contains(chain.Content, "<tenant>") {
		t.Errorf("chain copy still carries the <tenant> placeholder:\n%s", chain.Content)
	}
}

func TestGenaiDumpRows_NothingToChangeCopiesNothing(t *testing.T) {
	c := fakeClient(t, pgPod("psql-0", "master"))
	execer := genaiReader(0, 0, false)
	pod, _, _, _ := stepGenAI.RunQuery(context.Background(), Options{Client: c, Execer: execer}, "stratio", true)
	copies, err := stepGenAI.DumpRows(context.Background(), Options{Client: c, Execer: execer}, "stratio", pod)
	if err != nil || len(copies) != 0 {
		t.Errorf("copies = %+v, %v, want none", copies, err)
	}
}

// genaiDeployment is a Deployment of the genai CCT service, a CCT app of its
// own (as genai-ui is).
func genaiDeployment(name string, extraLabels ...string) *unstructured.Unstructured {
	u := legacyDeployment("stratio-genai", name, name+".stratio-genai", 1, extraLabels...)
	u.SetAnnotations(map[string]string{"cct.stratio.com/application_service": "genai"})
	return u
}

func isGenaiFamily(obj *unstructured.Unstructured) bool {
	return obj.GetAnnotations()["cct.stratio.com/application_service"] == "genai"
}

// TestGenaiPlan_TheFamilysLegacyDeploymentsOnly: genai-api and its siblings
// (each a CCT app of its own, so the app id lookup alone would find one),
// never a Flux-managed one, another app's, or an Ingress or Service.
func TestGenaiPlan_TheFamilysLegacyDeploymentsOnly(t *testing.T) {
	other := legacyDeployment("stratio-genai", "genai-litellm", "genai-litellm.stratio-genai", 1)
	other.SetAnnotations(map[string]string{"cct.stratio.com/application_service": "litellm"})
	c := fakeClient(t,
		genaiDeployment("genai-api"),
		genaiDeployment("genai-ui"),
		genaiDeployment("genai-developer-proxy", "helm.toolkit.fluxcd.io/name", "genai"),
		other,
		legacyDeployment("stratio-apps", "genai-api", "genai-api.stratio-apps", 1),
		object(gvkIngress, "stratio-genai", "genai.eosdev.int", cctAppIDLabel, "genai-api.stratio-genai"),
	)
	opts := Options{TenantName: "stratio", LiveName: "genai-api", LiveNamespace: "stratio-genai", IsLegacy: isGenaiFamily, Client: c}

	ops, err := stepGenAI.Plan(context.Background(), opts)
	assertPlan(t, ops, err,
		"delete Deployment stratio-genai/genai-api",
		"delete Deployment stratio-genai/genai-ui")

	applyAll(t, c, ops)
	ops, err = stepGenAI.Plan(context.Background(), opts)
	assertPlan(t, ops, err)
}

func TestGenaiStep_ShapeAndReplacesWorkloads(t *testing.T) {
	if stepGenAI.Query == nil || stepGenAI.Plan == nil || stepGenAI.Automated {
		t.Errorf("prepare-genai: Query=%v Plan=%v Automated=%v, want a Query step that also carries a Plan", stepGenAI.Query != nil, stepGenAI.Plan != nil, stepGenAI.Automated)
	}
	if !stepGenAI.ReplacesWorkloads {
		t.Error("prepare-genai deletes the legacy Deployments: ReplacesWorkloads must be true")
	}
}
