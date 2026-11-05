//go:build e2e

package prepare

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/vault"
)

// TestE2E_PasswordMovesSurviveTheSecretsBundle runs the litellm password
// moves against a real cluster, in a sandbox Vault folder no app uses, and
// then lets the secrets operator reconcile a SecretsBundle declaring the
// moved names, as the chart's does:
//
//	FLUX_STRATIO_E2E_CONTEXT=kubernetes-admin@eosdev \
//	  go test -tags e2e -run TestE2E_PasswordMoves -v ./internal/prepare/
//
// It writes to Vault and creates (then deletes) a SecretsBundle: run it only
// with the cluster owner's go-ahead. It refuses to start if the sandbox
// folder holds anything. The package vault has no delete, so the legacy/
// backups it leaves are listed at the end for removal by hand.
func TestE2E_PasswordMovesSurviveTheSecretsBundle(t *testing.T) {
	kubeContext := os.Getenv("FLUX_STRATIO_E2E_CONTEXT")
	if kubeContext == "" {
		t.Skip("FLUX_STRATIO_E2E_CONTEXT is not set")
	}
	const (
		namespace = "stratio-genai"
		bundle    = "flux-stratio-e2e"
	)
	base := vault.UserlandMount + "passwords/" + bundle + "." + namespace
	opts := Options{LiveName: bundle, LiveNamespace: namespace}
	ctx := context.Background()
	logger := log.New(os.Stderr, true)

	flags := genericclioptions.NewConfigFlags(true)
	flags.Context = &kubeContext
	c, err := kubeclient.New(flags)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := kubeclient.NewExecer(flags)
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.Connect(ctx, c, ex)
	if err != nil {
		t.Fatal(err)
	}
	if keys, err := v.List(ctx, base); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("sandbox %s is not empty (%v, %v): clean it first", base, keys, err)
	}
	t.Cleanup(func() {
		t.Logf("remove the backups by hand, in the Vault pod: vault delete %s/%s/<name> for master_api_key, db_salt_key, keos_key_secret", base, legacyBackupDir)
	})

	// 1. Seed the legacy entries, as CCT left them.
	legacy := map[string]string{}
	for _, m := range litellmPasswordMoves {
		legacy[m.From] = randomString(t, m.Length)
		if err := v.Write(ctx, base+"/"+m.From, vault.Data{passwordField: legacy[m.From]}); err != nil {
			t.Fatal(err)
		}
	}

	// 2. Plan and apply the moves; a re-plan finds nothing to do.
	ops, err := planPasswordMoves(ctx, v, opts, litellmPasswordMoves, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2*len(litellmPasswordMoves) {
		t.Fatalf("planned %q, want a backup and a copy per move", opStrings(ops))
	}
	for _, op := range ops {
		if err := op.Apply(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(path, want string) {
		t.Helper()
		d, err := v.Read(ctx, path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return
		}
		if d[passwordField] != want {
			t.Errorf("%s doesn't hold the legacy value", path)
		}
	}
	for _, m := range litellmPasswordMoves {
		expect(base+"/"+m.To, legacy[m.From])
		expect(base+"/"+legacyBackupDir+"/"+m.From, legacy[m.From])
		expect(base+"/"+m.From, legacy[m.From])
	}
	if ops, err := planPasswordMoves(ctx, v, opts, litellmPasswordMoves, logger); err != nil || len(ops) != 0 {
		t.Fatalf("re-plan = %q, %v; want nothing", opStrings(ops), err)
	}

	// 3. The chart's SecretsBundle keeps the moved values, removes the
	// legacy entries and leaves legacy/ alone.
	sb := secretsBundle(bundle, namespace)
	if err := c.Create(ctx, sb); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Delete(context.Background(), sb); client.IgnoreNotFound(err) != nil {
			t.Errorf("deleting SecretsBundle %s/%s: %v", namespace, bundle, err)
		}
	})
	if err := waitReady(ctx, c, sb); err != nil {
		t.Fatal(err)
	}
	for _, m := range litellmPasswordMoves {
		expect(base+"/"+m.To, legacy[m.From])
		expect(base+"/"+legacyBackupDir+"/"+m.From, legacy[m.From])
		if _, err := v.Read(ctx, base+"/"+m.From); !errors.Is(err, vault.ErrNotFound) {
			t.Errorf("%s/%s still there after the bundle reconciled (%v)", base, m.From, err)
		}
	}
}

// TestE2E_InspectPasswordsIsReadOnly makes, against a real app's passwords
// folder, the reads prepare-litellm's plan and doctor's vault check start
// with, and only those: it lists names, reads no value, writes nothing.
//
//	FLUX_STRATIO_E2E_CONTEXT=kubernetes-admin@eosdev FLUX_STRATIO_E2E_APP=stratio-genai/genai-litellm \
//	  go test -tags e2e -run TestE2E_InspectPasswords -v ./internal/prepare/
func TestE2E_InspectPasswordsIsReadOnly(t *testing.T) {
	kubeContext, app := os.Getenv("FLUX_STRATIO_E2E_CONTEXT"), os.Getenv("FLUX_STRATIO_E2E_APP")
	namespace, name, ok := strings.Cut(app, "/")
	if kubeContext == "" || !ok {
		t.Skip("FLUX_STRATIO_E2E_CONTEXT or FLUX_STRATIO_E2E_APP (<namespace>/<live name>) is not set")
	}
	ctx := context.Background()
	flags := genericclioptions.NewConfigFlags(true)
	flags.Context = &kubeContext
	c, err := kubeclient.New(flags)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := kubeclient.NewExecer(flags)
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.Connect(ctx, c, ex)
	if err != nil {
		t.Fatal(err)
	}
	state, err := InspectPasswords(ctx, v, Options{LiveName: name, LiveNamespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s on %s: %s kv v%s; entries %v; legacy/ %v; %s", state.Path, v.Pod(), vault.UserlandMount, state.Mount.Version,
		state.Entries, state.Backups, state.Summary(litellmPasswordMoves))
}

// secretsBundle declares litellmPasswordMoves' names as the litellm chart
// does.
func secretsBundle(name, namespace string) *unstructured.Unstructured {
	var passwords []any
	for _, m := range litellmPasswordMoves {
		passwords = append(passwords, map[string]any{"name": m.To, "keyName": passwordField, "length": int64(m.Length), "type": "generic"})
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "secrets.stratio.com/v2",
		"kind":       "SecretsBundle",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec":       map[string]any{"mount": "userland", "passwords": passwords},
	}}
	return u
}

func waitReady(ctx context.Context, c client.Client, sb *unstructured.Unstructured) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		got := sb.DeepCopy()
		if err := c.Get(ctx, client.ObjectKeyFromObject(sb), got); err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		return phase == "Ready", nil
	})
}

// randomString is n random alphanumerics.
func randomString(t *testing.T, n int) string {
	t.Helper()
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			t.Fatal(err)
		}
		b[i] = alphabet[k.Int64()]
	}
	return string(b)
}
