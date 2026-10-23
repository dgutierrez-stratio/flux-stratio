package prepare

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/vault"
	"github.com/Stratio/flux-stratio/internal/vault/vaulttest"
)

const litellmBase = "userland/passwords/genai-litellm.stratio-genai"

// Legacy values, each of the length the chart declares for its new name.
var (
	legacySalt   = strings.Repeat("s", 32)
	legacyKeos   = strings.Repeat("k", 32)
	legacyMaster = "sk-" + strings.Repeat("m", 10)
)

// legacyEntries are the three legacy passwords as CCT left them; the
// salt's carries a field besides pass, which only its backup keeps.
func legacyEntries() map[string]map[string]string {
	return map[string]map[string]string{
		litellmBase + "/db_salt_key":     {"pass": legacySalt, "note": "cct"},
		litellmBase + "/keos_key_secret": {"pass": legacyKeos},
		litellmBase + "/master_api_key":  {"pass": legacyMaster},
		litellmBase + "/keystore":        {"pass": strings.Repeat("x", 14)},
	}
}

// litellmEnv is the fake Vault seeded with entries, a cluster holding it
// and, when withDeployment, the legacy LiteLLM Deployment, and the step's
// options against them with a logger writing to the returned buffer.
func litellmEnv(t *testing.T, entries map[string]map[string]string, withDeployment bool, extra ...client.Object) (*vaulttest.Server, client.Client, Options, *bytes.Buffer) {
	t.Helper()
	srv := vaulttest.New()
	for p, d := range entries {
		srv.Entries[p] = d
	}
	objs := append(vaulttest.Objects(vaulttest.Token, "vault-0"), extra...)
	if withDeployment {
		objs = append(objs, legacyDeployment("stratio-genai", "genai-litellm", "genai-litellm.stratio-genai", 1))
	}
	c := fakeClient(t, objs...)
	var out bytes.Buffer
	opts := Options{
		TenantName: "stratio", LiveName: "genai-litellm", LiveNamespace: "stratio-genai",
		Client: c, Execer: srv.Exec, Log: log.New(&out, true),
	}
	return srv, c, opts, &out
}

// First run on a legacy cluster: each legacy entry is backed up verbatim
// and its value copied to the chart's name, then the legacy Deployment
// goes; the legacy entries themselves are never written, and a re-run
// plans nothing.
func TestStepLitellm_MovesThenDeletes(t *testing.T) {
	srv, c, opts, out := litellmEnv(t, legacyEntries(), true)
	ctx := context.Background()

	ops, err := stepLitellm.Plan(ctx, opts)
	assertPlan(t, ops, err,
		"back up VaultPassword "+litellmBase+"/legacy/master_api_key",
		"back up VaultPassword "+litellmBase+"/legacy/db_salt_key",
		"back up VaultPassword "+litellmBase+"/legacy/keos_key_secret",
		"copy VaultPassword "+litellmBase+"/master-api-key (from master_api_key)",
		"copy VaultPassword "+litellmBase+"/db-salt-key (from db_salt_key)",
		"copy VaultPassword "+litellmBase+"/keos-key-secret (from keos_key_secret)",
		"delete Deployment stratio-genai/genai-litellm",
	)
	if w := srv.Writes(); len(w) != 0 {
		t.Fatalf("Plan wrote %q, want nothing", w)
	}
	// Plan lists the folder and its backups first and reads only entries a
	// move names that exist: the three legacy ones, not keystore.
	if n := countCommands(srv, "read"); n != len(litellmPasswordMoves) {
		t.Errorf("Plan read %d entries, want %d", n, len(litellmPasswordMoves))
	}

	applyAll(t, c, ops)
	for path, want := range map[string]map[string]string{
		litellmBase + "/db-salt-key":            {"pass": legacySalt},
		litellmBase + "/keos-key-secret":        {"pass": legacyKeos},
		litellmBase + "/master-api-key":         {"pass": legacyMaster},
		litellmBase + "/legacy/db_salt_key":     {"pass": legacySalt, "note": "cct"},
		litellmBase + "/legacy/keos_key_secret": {"pass": legacyKeos},
		litellmBase + "/legacy/master_api_key":  {"pass": legacyMaster},
	} {
		if got := srv.Entries[path]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %d field(s), not the expected ones", path, len(got))
		}
	}
	for path, want := range legacyEntries() {
		if !reflect.DeepEqual(srv.Entries[path], want) {
			t.Errorf("legacy entry %s changed", path)
		}
	}
	assertNoLegacyWrites(t, srv)

	ops, err = stepLitellm.Plan(ctx, opts)
	assertPlan(t, ops, err)
	assertNoLeak(t, out.String(), ops, nil)
}

// After the cutover the chart's SecretsBundle has removed the legacy
// entries; the backup shows the release still holds their values, so
// nothing is planned and nothing is reported.
func TestStepLitellm_AfterTheCutover(t *testing.T) {
	entries := map[string]map[string]string{
		litellmBase + "/db-salt-key":            {"pass": legacySalt},
		litellmBase + "/keos-key-secret":        {"pass": legacyKeos},
		litellmBase + "/master-api-key":         {"pass": legacyMaster},
		litellmBase + "/legacy/db_salt_key":     {"pass": legacySalt, "note": "cct"},
		litellmBase + "/legacy/keos_key_secret": {"pass": legacyKeos},
		litellmBase + "/legacy/master_api_key":  {"pass": legacyMaster},
	}
	srv, _, opts, out := litellmEnv(t, entries, false)
	ops, err := stepLitellm.Plan(context.Background(), opts)
	assertPlan(t, ops, err)
	if strings.Contains(out.String(), "⚠️") {
		t.Errorf("warned after a clean cutover:\n%s", out)
	}
	assertNoLegacyWrites(t, srv)
}

// The cases that move nothing, only warn: the legacy entries gone with no
// backup (the chart was applied before the step ever ran, as on eosdev),
// or the release no longer holding the backed-up value.
func TestStepLitellm_WarnsWhenNothingCanBeMoved(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entries map[string]map[string]string
		warning string
	}{
		{
			name: "legacy gone, no backup",
			entries: map[string]map[string]string{
				litellmBase + "/db-salt-key": {"pass": strings.Repeat("n", 32)},
			},
			warning: "the legacy Vault password(s) master_api_key, db_salt_key, keos_key_secret are not in " + litellmBase,
		},
		{
			name: "release replaced the backed-up value",
			entries: map[string]map[string]string{
				litellmBase + "/db-salt-key":            {"pass": strings.Repeat("n", 32)},
				litellmBase + "/keos-key-secret":        {"pass": legacyKeos},
				litellmBase + "/master-api-key":         {"pass": legacyMaster},
				litellmBase + "/legacy/db_salt_key":     {"pass": legacySalt},
				litellmBase + "/legacy/keos_key_secret": {"pass": legacyKeos},
				litellmBase + "/legacy/master_api_key":  {"pass": legacyMaster},
			},
			warning: "db-salt-key in " + litellmBase + " no longer hold(s) the legacy value backed up under legacy/",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, opts, out := litellmEnv(t, tt.entries, true)
			ops, err := stepLitellm.Plan(context.Background(), opts)
			assertPlan(t, ops, err, "delete Deployment stratio-genai/genai-litellm")
			if !strings.Contains(out.String(), tt.warning) {
				t.Errorf("output lacks %q:\n%s", tt.warning, out)
			}
			assertNoLegacyWrites(t, srv)
		})
	}
}

// What would rotate a live secret, or can't be moved as is, stops the step
// in Plan: nothing has been written.
func TestStepLitellm_RefusesBeforeWriting(t *testing.T) {
	for _, tt := range []struct {
		name    string
		change  func(map[string]map[string]string)
		wantErr string
	}{
		{
			name: "release already has its own value",
			change: func(e map[string]map[string]string) {
				e[litellmBase+"/db-salt-key"] = map[string]string{"pass": strings.Repeat("n", 32)}
			},
			wantErr: litellmBase + "/db-salt-key already holds another value than " + litellmBase + "/db_salt_key",
		},
		{
			name: "legacy value of another length",
			change: func(e map[string]map[string]string) {
				e[litellmBase+"/keos_key_secret"] = map[string]string{"pass": "short"}
			},
			wantErr: "holds a 5-character value, but the chart declares keos-key-secret with 32",
		},
		{
			name: "legacy entry without pass",
			change: func(e map[string]map[string]string) {
				e[litellmBase+"/master_api_key"] = map[string]string{"value": legacyMaster}
			},
			wantErr: `has no "pass" field to move to master-api-key`,
		},
		{
			name: "backup differs from the legacy entry",
			change: func(e map[string]map[string]string) {
				e[litellmBase+"/legacy/db_salt_key"] = map[string]string{"pass": legacySalt}
			},
			wantErr: "doesn't match its backup",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entries := legacyEntries()
			tt.change(entries)
			srv, _, opts, out := litellmEnv(t, entries, true)
			ops, err := stepLitellm.Plan(context.Background(), opts)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Plan error = %v, want one containing %q", err, tt.wantErr)
			}
			if len(ops) != 0 || len(srv.Writes()) != 0 {
				t.Errorf("Plan returned %d op(s) and wrote %q, want neither", len(ops), srv.Writes())
			}
			assertNoLeak(t, out.String(), nil, err)
		})
	}
}

// A chart entry already holding the legacy value plus a field the chart
// doesn't declare is rewritten to the bare value: the operator would
// otherwise regenerate it.
func TestStepLitellm_RewritesAnEntryWithExtraFields(t *testing.T) {
	entries := legacyEntries()
	entries[litellmBase+"/db-salt-key"] = map[string]string{"pass": legacySalt, "user": "x"}
	srv, c, opts, _ := litellmEnv(t, entries, false)
	ops, err := stepLitellm.Plan(context.Background(), opts)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	applyAll(t, c, ops)
	if got := srv.Entries[litellmBase+"/db-salt-key"]; !reflect.DeepEqual(got, map[string]string{"pass": legacySalt}) {
		t.Errorf("db-salt-key has %d field(s), want only pass", len(got))
	}
}

// Once the chart has replaced the legacy app — the legacy Deployment gone,
// the release's SecretsBundle there — a re-run plans nothing without
// reaching Vault at all, so it needs no Vault access.
func TestStepLitellm_AfterTheCutoverSkipsVault(t *testing.T) {
	srv, _, opts, _ := litellmEnv(t, nil, false, releaseBundle())
	opts.Execer = nil // reaching Vault would fail
	ops, err := stepLitellm.Plan(context.Background(), opts)
	assertPlan(t, ops, err)
	if len(srv.Exec.Calls) != 0 {
		t.Errorf("ran %d command(s) in Vault after the cutover, want none", len(srv.Exec.Calls))
	}
}

// After the cutover the GitOps Deployment has the legacy one's name and
// the CCT app id label may linger on it; a re-run must not delete it,
// whether the release's bundle is there yet or not.
func TestStepLitellm_NeverTheGitOpsDeployment(t *testing.T) {
	gitops := legacyDeployment("stratio-genai", "genai-litellm", "genai-litellm.stratio-genai", 1, "helm.toolkit.fluxcd.io/name", "litellm")
	for _, tt := range []struct {
		name  string
		extra []client.Object
	}{
		{"with the release's bundle", []client.Object{gitops, releaseBundle()}},
		{"before the bundle", []client.Object{gitops}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, opts, _ := litellmEnv(t, nil, false, tt.extra...)
			ops, err := stepLitellm.Plan(context.Background(), opts)
			assertPlan(t, ops, err)
		})
	}
}

// A run interrupted after its backups, whose legacy originals the bundle
// has since removed, is completed from the backups on the next run.
func TestStepLitellm_CopiesFromTheBackup(t *testing.T) {
	entries := map[string]map[string]string{
		litellmBase + "/legacy/db_salt_key":     {"pass": legacySalt, "note": "cct"},
		litellmBase + "/legacy/keos_key_secret": {"pass": legacyKeos},
		litellmBase + "/legacy/master_api_key":  {"pass": legacyMaster},
		litellmBase + "/master-api-key":         {"pass": legacyMaster},
	}
	srv, c, opts, _ := litellmEnv(t, entries, false)
	ops, err := stepLitellm.Plan(context.Background(), opts)
	assertPlan(t, ops, err,
		"copy VaultPassword "+litellmBase+"/db-salt-key (from legacy/db_salt_key)",
		"copy VaultPassword "+litellmBase+"/keos-key-secret (from legacy/keos_key_secret)",
	)
	applyAll(t, c, ops)
	if got := srv.Entries[litellmBase+"/db-salt-key"]; !reflect.DeepEqual(got, map[string]string{"pass": legacySalt}) {
		t.Errorf("db-salt-key has %d field(s), want the backed-up pass only", len(got))
	}
}

// Each write checks its target again when it runs: a value the secrets
// operator wrote between the plan and the run (the chart applied while the
// confirmation waited) is never overwritten.
func TestStepLitellm_WriteRefusesATargetChangedSincePlan(t *testing.T) {
	srv, c, opts, _ := litellmEnv(t, legacyEntries(), false)
	ctx := context.Background()
	ops, err := stepLitellm.Plan(ctx, opts)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	fresh := map[string]string{"pass": strings.Repeat("n", 32)}
	srv.Entries[litellmBase+"/db-salt-key"] = fresh

	var failed error
	for _, op := range ops {
		if err := op.Apply(ctx, c); err != nil {
			failed = err
			break
		}
	}
	if failed == nil || !strings.Contains(failed.Error(), litellmBase+"/db-salt-key changed since the plan") {
		t.Fatalf("Apply error = %v, want the changed target refused", failed)
	}
	if !reflect.DeepEqual(srv.Entries[litellmBase+"/db-salt-key"], fresh) {
		t.Error("the operator's value was overwritten")
	}
	// The re-plan the error asks for refuses to rotate it.
	if _, err := stepLitellm.Plan(ctx, opts); err == nil || !strings.Contains(err.Error(), "already holds another value") {
		t.Errorf("re-plan error = %v, want the live secret protected", err)
	}
}

// The step moves passwords by plain read and write, which only kv v1
// serves: any other engine stops it before anything is written.
func TestStepLitellm_RequiresKVv1(t *testing.T) {
	srv, _, opts, _ := litellmEnv(t, legacyEntries(), true)
	srv.Mounts["userland/"] = vault.MountInfo{Type: "kv", Version: "2"}
	ops, err := stepLitellm.Plan(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "not kv v1") || len(ops) != 0 {
		t.Errorf("Plan on kv v2 = %v, %v; want an error and no operation", opStrings(ops), err)
	}
}

// Without a live object to name the Vault folder by, the step fails before
// reaching Vault.
func TestStepLitellm_NoLiveObject(t *testing.T) {
	srv, _, opts, _ := litellmEnv(t, legacyEntries(), true)
	opts.LiveNamespace = ""
	if _, err := stepLitellm.Plan(context.Background(), opts); err == nil {
		t.Error("Plan without a live namespace = nil error, want one")
	}
	if len(srv.Exec.Calls) != 0 {
		t.Errorf("ran %d command(s) in Vault, want none", len(srv.Exec.Calls))
	}
	if _, err := InspectPasswords(context.Background(), nil, Options{LiveName: "x"}); err == nil {
		t.Error("InspectPasswords without a live namespace = nil error, want one")
	}
}

// Summary says what doctor reports for each state.
func TestPasswordsState_Summary(t *testing.T) {
	s := PasswordsState{Entries: []string{"master_api_key", "db-salt-key"}, Backups: []string{"db_salt_key"}}
	if got, want := s.Summary(litellmPasswordMoves), "1 legacy password(s) to move, 1 already moved (backup under legacy/), 1 not in Vault (keos_key_secret)"; got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}
}

// Without a pod exec or a reachable Vault the step can't plan.
func TestStepLitellm_NeedsVault(t *testing.T) {
	if !stepLitellm.NeedsVault() {
		t.Error("prepare-litellm doesn't declare NeedsVault")
	}
	_, _, opts, _ := litellmEnv(t, legacyEntries(), true)
	opts.Execer = nil
	if _, err := stepLitellm.Plan(context.Background(), opts); err == nil {
		t.Error("Plan without an Execer = nil error, want one")
	}
	_, _, opts, _ = litellmEnv(t, legacyEntries(), true)
	opts.Execer = &kubeclient.FakeExecer{Response: kubeclient.FakeExecResponse{Stderr: "* permission denied", Err: errExec}}
	if _, err := stepLitellm.Plan(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("Plan with a rejected token error = %v, want permission denied", err)
	}
}

// Only prepare-litellm reaches Vault.
func TestSteps_NeedsVaultOnlyLitellm(t *testing.T) {
	for _, s := range Steps {
		if s.NeedsVault() != (s.Name == "prepare-litellm") {
			t.Errorf("%s: NeedsVault = %v", s.Name, s.NeedsVault())
		}
	}
}

// releaseBundle is the SecretsBundle the litellm chart's release creates.
func releaseBundle() *unstructured.Unstructured {
	return object(gvkSecretsBundle, "stratio-genai", "genai-litellm")
}

// countCommands counts the vault commands of one kind run so far.
func countCommands(srv *vaulttest.Server, kind string) int {
	n := 0
	for _, args := range srv.Commands() {
		if len(args) > 0 && args[0] == kind {
			n++
		}
	}
	return n
}

var errExec = &execError{}

type execError struct{}

func (*execError) Error() string { return "command terminated with exit code 2" }

// assertNoLegacyWrites fails if anything was written to a legacy entry.
func assertNoLegacyWrites(t *testing.T, srv *vaulttest.Server) {
	t.Helper()
	for _, p := range srv.Writes() {
		for _, m := range litellmPasswordMoves {
			if p == litellmBase+"/"+m.From {
				t.Errorf("wrote the legacy entry %s", p)
			}
		}
	}
	for _, args := range srv.Commands() {
		if len(args) > 0 && args[0] == "delete" {
			t.Errorf("ran vault %q", args)
		}
	}
}

// assertNoLeak fails if a legacy value shows in the output, an operation
// or an error.
func assertNoLeak(t *testing.T, output string, ops []Operation, err error) {
	t.Helper()
	texts := append([]string{output}, opStrings(ops)...)
	if err != nil {
		texts = append(texts, err.Error())
	}
	for _, text := range texts {
		for _, v := range []string{legacySalt, legacyKeos, legacyMaster} {
			if strings.Contains(text, v) {
				t.Errorf("a secret value leaked into %q", text)
			}
		}
	}
}
