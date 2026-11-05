package vault_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/vault"
	"github.com/Stratio/flux-stratio/internal/vault/vaulttest"
)

func fakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := apiruntime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func connect(t *testing.T, srv *vaulttest.Server) *vault.Client {
	t.Helper()
	v, err := vault.Connect(context.Background(), fakeClient(t, vaulttest.Objects(vaulttest.Token, "vault-0")...), srv.Exec)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return v
}

func TestConnect_PicksTheServerPod(t *testing.T) {
	t.Parallel()
	secret := vaulttest.Objects(vaulttest.Token, "unused")[0]
	notRunning := vaulttest.ServerPod("vault-9", true)
	notRunning.Status.Phase = corev1.PodPending
	for _, tt := range []struct {
		name    string
		pods    []client.Object
		want    string
		wantErr string
	}{
		{name: "single", pods: []client.Object{vaulttest.ServerPod("vault-0", false)}, want: "keos-core/vault-0"},
		{name: "HA, the active one", pods: []client.Object{vaulttest.ServerPod("vault-0", false), vaulttest.ServerPod("vault-1", true)}, want: "keos-core/vault-1"},
		{name: "HA, none active", pods: []client.Object{vaulttest.ServerPod("vault-0", false), vaulttest.ServerPod("vault-1", false)}, wantErr: "none is labelled vault-active=true"},
		{name: "none running", pods: []client.Object{notRunning}, wantErr: "no running Vault server pod"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v, err := vault.Connect(context.Background(), fakeClient(t, append([]client.Object{secret}, tt.pods...)...), &kubeclient.FakeExecer{})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Connect error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if v.Pod() != tt.want {
				t.Errorf("Pod() = %q, want %q", v.Pod(), tt.want)
			}
		})
	}
}

func TestConnect_TokenSecret(t *testing.T) {
	t.Parallel()
	pod := vaulttest.ServerPod("vault-0", false)
	for _, tt := range []struct {
		name    string
		objs    []client.Object
		wantErr string
	}{
		{name: "no Secret", objs: []client.Object{pod}, wantErr: "reading the Vault root token from Secret keos-core/vault-unseal-keys"},
		{name: "no key", objs: []client.Object{pod, &corev1.Secret{ObjectMeta: vaulttest.Objects("", "x")[0].(*corev1.Secret).ObjectMeta}}, wantErr: `has no "vault-root" key`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := vault.Connect(context.Background(), fakeClient(t, tt.objs...), &kubeclient.FakeExecer{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Connect error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
	if _, err := vault.Connect(context.Background(), nil, nil); err == nil {
		t.Error("Connect(nil, nil) = nil error, want one")
	}
}

// The token goes only as stdin's first line, never in the command line,
// and every command runs in the vault container of the server pod.
func TestClient_TokenOnlyOnStdin(t *testing.T) {
	t.Parallel()
	srv := vaulttest.New()
	srv.Entries["userland/passwords/a.b/x"] = map[string]string{"pass": "v"}
	v := connect(t, srv)
	ctx := context.Background()

	if _, err := v.Mount(ctx, vault.UserlandMount); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if _, err := v.Read(ctx, "userland/passwords/a.b/x"); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := v.Write(ctx, "userland/passwords/a.b/y", vault.Data{"pass": "w"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, c := range srv.Exec.Calls {
		if c.Namespace != vault.Namespace || c.Pod != "vault-0" || c.Container != vault.Container {
			t.Errorf("ran in %s/%s[%s], want keos-core/vault-0[vault]", c.Namespace, c.Pod, c.Container)
		}
		if strings.Contains(strings.Join(c.Command, " "), vaulttest.Token) {
			t.Errorf("token in the command line: %q", c.Command)
		}
		if !strings.HasPrefix(c.Stdin, vaulttest.Token+"\n") {
			t.Errorf("stdin doesn't start with the token line: %q", c.Stdin)
		}
	}
	if got, want := srv.Exec.Calls[2].Stdin, vaulttest.Token+"\n"+`{"pass":"w"}`; got != want {
		t.Errorf("write stdin = %q, want %q", got, want)
	}
	if got, want := vaulttest.Args(srv.Exec.Calls[2]), []string{"write", "userland/passwords/a.b/y", "-"}; !reflect.DeepEqual(got, want) {
		t.Errorf("write args = %q, want %q", got, want)
	}
}

func TestClient_ReadListMount(t *testing.T) {
	t.Parallel()
	srv := vaulttest.New()
	srv.Entries["userland/passwords/a.b/x"] = map[string]string{"pass": "v", "user": "u"}
	srv.Entries["userland/passwords/a.b/legacy/y"] = map[string]string{"pass": "w"}
	v := connect(t, srv)
	ctx := context.Background()

	d, err := v.Read(ctx, "userland/passwords/a.b/x")
	if err != nil || d["pass"] != "v" || d["user"] != "u" || len(d) != 2 {
		t.Errorf("Read = %d field(s), err %v; want pass and user", len(d), err)
	}
	if _, err := v.Read(ctx, "userland/passwords/a.b/missing"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("Read(missing) error = %v, want ErrNotFound", err)
	}
	keys, err := v.List(ctx, "userland/passwords/a.b")
	if err != nil || !reflect.DeepEqual(keys, []string{"legacy/", "x"}) {
		t.Errorf("List = %q, %v; want [legacy/ x]", keys, err)
	}
	if _, err := v.List(ctx, "userland/passwords/none"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("List(empty) error = %v, want ErrNotFound", err)
	}
	m, err := v.Mount(ctx, "userland")
	if err != nil || m != (vault.MountInfo{Type: "kv", Version: "1"}) {
		t.Errorf("Mount = %+v, %v; want kv v1", m, err)
	}
	if _, err := v.Mount(ctx, "nope/"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("Mount(nope) error = %v, want ErrNotFound", err)
	}
}

// A rejected token is an error carrying vault's own message, and nothing
// it reports holds the token.
func TestClient_BadToken(t *testing.T) {
	t.Parallel()
	srv := vaulttest.New()
	srv.Token = "another"
	_, err := connect(t, srv).List(context.Background(), "userland/passwords")
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("List error = %v, want permission denied", err)
	}
	if strings.Contains(err.Error(), vaulttest.Token) {
		t.Errorf("error holds the token: %v", err)
	}
}

func TestClient_WriteNothing(t *testing.T) {
	t.Parallel()
	srv := vaulttest.New()
	if err := connect(t, srv).Write(context.Background(), "userland/x", nil); err == nil {
		t.Error("Write(nil) = nil error, want one")
	}
	if len(srv.Exec.Calls) != 0 {
		t.Errorf("Write(nil) ran %d command(s), want none", len(srv.Exec.Calls))
	}
}

// Data never renders its values, whatever prints it.
func TestData_Redacted(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t-value"
	d := vault.Data{"pass": secret}
	var sb strings.Builder
	slog.New(slog.NewTextHandler(&sb, nil)).Info("x", "data", d)
	for _, out := range []string{
		fmt.Sprint(d), fmt.Sprintf("%v %+v %#v %s %q", d, d, d, d, d), d.String(), d.GoString(),
		fmt.Errorf("wrapped: %v", d).Error(), sb.String(),
	} {
		if strings.Contains(out, secret) {
			t.Errorf("value leaked: %q", out)
		}
	}
}
