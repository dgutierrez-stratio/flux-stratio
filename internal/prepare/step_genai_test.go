package prepare

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

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
