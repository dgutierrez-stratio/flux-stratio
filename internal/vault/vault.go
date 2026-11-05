// Package vault is the one way flux-stratio reaches the cluster's Vault: the
// way an operator does it by hand, `kubectl exec` into the Vault server pod
// with the root token from keos-core/vault-unseal-keys, running the vault
// CLI that ships in the pod. There is no network path from the operator's
// machine to Vault and no Vault client in this module, so exec is the only
// route that needs nothing beyond the kubeconfig every command already uses.
//
// The token travels as the first line of the command's stdin, never in its
// argv (visible in the pod's process list) and never in a log or an error.
// Only these vault subcommands are issued: secrets list, list, read and
// write. There is deliberately no delete: nothing built on this
// package can remove a Vault entry.
//
// Values read from Vault (Data) pass through memory only, between a Read
// and a Write: Data renders as <redacted> however it is formatted, so it
// can't reach the console or an error by accident. This is the one
// exception to "secrets never leave internal/envvars", which is about what
// flux-stratio outputs, not what it moves.
package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/kubeclient"
)

// Where the Vault server and its root token live in a KEOS cluster.
const (
	// Namespace holds both the Vault server pod and the token Secret.
	Namespace = "keos-core"
	// TokenSecret and TokenKey name the Secret key holding the root token.
	TokenSecret = "vault-unseal-keys"
	TokenKey    = "vault-root"
	// Container is the server pod's container that has the vault CLI.
	Container = "vault"
	// ActiveLabel marks the active server pod of an HA Vault.
	ActiveLabel = "vault-active"
	// UserlandMount is the kv mount the secrets operator keeps the apps'
	// passwords and certificates in.
	UserlandMount = "userland/"
	// addr is the server's listener as seen from inside its own pod.
	addr = "https://127.0.0.1:8200"
)

// serverSelector finds the Vault server pods (not the agent injector, the
// configurer or the operator, which share the namespace).
var serverSelector = client.MatchingLabels{
	"app.kubernetes.io/name":      "vault",
	"app.kubernetes.io/component": "server",
}

// wrapper reads the token from stdin's first line and execs the vault CLI
// with the remaining arguments, so whatever stdin follows the token (a
// Write's JSON) reaches vault untouched.
const wrapper = `read -r VAULT_TOKEN; export VAULT_TOKEN VAULT_ADDR=` + addr + `; exec vault "$@"`

// ErrNotFound is returned when the path holds nothing.
var ErrNotFound = errors.New("not found in Vault")

// redacted is how Data renders, whatever the verb.
const redacted = "<redacted>"

// Data is one Vault entry's fields. It always renders as <redacted>, so a
// value can't be printed, logged or wrapped into an error by accident; read
// a field by indexing the map.
type Data map[string]string

// String implements fmt.Stringer.
func (Data) String() string { return redacted }

// GoString implements fmt.GoStringer.
func (Data) GoString() string { return redacted }

// Format implements fmt.Formatter, covering every verb (%v, %+v, %#v, %s,
// %q, ...).
func (Data) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// LogValue implements slog.LogValuer.
func (Data) LogValue() slog.Value { return slog.StringValue(redacted) }

// MountInfo is a secrets engine mount.
type MountInfo struct {
	// Type is the engine, e.g. "kv".
	Type string
	// Version is the kv engine's version ("1" or "2"), empty for others.
	Version string
}

// Client runs vault CLI commands in the Vault server pod. Build it with
// Connect.
type Client struct {
	exec  kubeclient.Execer
	pod   string
	token string
}

// Connect finds the Vault server pod and reads the root token. c reads the
// Secret and lists the pods; ex runs the commands. Nothing runs in Vault
// yet: the first command shows whether the token is accepted.
func Connect(ctx context.Context, c client.Client, ex kubeclient.Execer) (*Client, error) {
	if c == nil || ex == nil {
		return nil, errors.New("reaching Vault needs a cluster client and a pod exec")
	}
	token, err := rootToken(ctx, c)
	if err != nil {
		return nil, err
	}
	pod, err := serverPod(ctx, c)
	if err != nil {
		return nil, err
	}
	return &Client{exec: ex, pod: pod, token: token}, nil
}

// rootToken reads the root token from TokenSecret/TokenKey.
func rootToken(ctx context.Context, c client.Client) (string, error) {
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: TokenSecret}, &s); err != nil {
		return "", fmt.Errorf("reading the Vault root token from Secret %s/%s: %w", Namespace, TokenSecret, err)
	}
	token := strings.TrimSpace(string(s.Data[TokenKey]))
	if token == "" {
		return "", fmt.Errorf("secret %s/%s has no %q key with the Vault root token", Namespace, TokenSecret, TokenKey)
	}
	if strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("secret %s/%s key %q is not a single-line token", Namespace, TokenSecret, TokenKey)
	}
	return token, nil
}

// serverPod picks the one running Vault server pod, or the active one of
// several.
func serverPod(ctx context.Context, c client.Client) (string, error) {
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(Namespace), serverSelector); err != nil {
		return "", fmt.Errorf("listing the Vault server pods in %s: %w", Namespace, err)
	}
	var running, active []string
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		running = append(running, p.Name)
		if p.Labels[ActiveLabel] == "true" {
			active = append(active, p.Name)
		}
	}
	sort.Strings(running)
	switch {
	case len(running) == 1:
		return running[0], nil
	case len(active) == 1:
		return active[0], nil
	case len(running) == 0:
		return "", fmt.Errorf("no running Vault server pod in %s (labels %v)", Namespace, map[string]string(serverSelector))
	default:
		return "", fmt.Errorf("%d running Vault server pods in %s (%s) and none is labelled %s=true", len(running), Namespace, strings.Join(running, ", "), ActiveLabel)
	}
}

// Pod is the server pod the commands run in, as namespace/name.
func (v *Client) Pod() string { return Namespace + "/" + v.pod }

// run execs `vault args...` with the token, then input, on stdin. A non-zero
// exit is an error carrying vault's own message; one that says the path
// holds nothing wraps ErrNotFound.
func (v *Client) run(ctx context.Context, input []byte, args ...string) (string, error) {
	stdin := bytes.NewBufferString(v.token + "\n")
	stdin.Write(input)
	cmd := append([]string{"sh", "-c", wrapper, "--"}, args...)
	stdout, stderr, err := v.exec.Exec(ctx, Namespace, v.pod, Container, cmd, stdin)
	if err == nil {
		return stdout, nil
	}
	if notFound(stdout, stderr) {
		return "", ErrNotFound
	}
	if msg := firstLines(stderr); msg != "" {
		return "", fmt.Errorf("vault %s in %s: %s: %w", args[0], v.Pod(), msg, err)
	}
	return "", fmt.Errorf("vault %s in %s: %w", args[0], v.Pod(), err)
}

// notFound reports whether a failed command failed because its path is
// empty: `vault read` says "No value found at", `vault list -format=json`
// prints an empty object.
func notFound(stdout, stderr string) bool {
	return strings.Contains(stderr, "No value found at") || strings.Contains(stdout, "No value found at") ||
		strings.TrimSpace(stdout) == "{}"
}

// firstLines is vault's error message without the blank lines it pads it
// with, on one line.
func firstLines(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, " ")
}

// Mount describes the secrets engine mounted at path (e.g. "userland/");
// ErrNotFound when nothing is.
func (v *Client) Mount(ctx context.Context, path string) (MountInfo, error) {
	out, err := v.run(ctx, nil, "secrets", "list", "-format=json")
	if err != nil {
		return MountInfo{}, fmt.Errorf("listing the Vault mounts: %w", err)
	}
	var mounts map[string]struct {
		Type    string            `json:"type"`
		Options map[string]string `json:"options"`
	}
	if err := json.Unmarshal([]byte(out), &mounts); err != nil {
		return MountInfo{}, fmt.Errorf("parsing the Vault mounts: %w", err)
	}
	m, ok := mounts[strings.TrimSuffix(path, "/")+"/"]
	if !ok {
		return MountInfo{}, fmt.Errorf("mount %s: %w", path, ErrNotFound)
	}
	return MountInfo{Type: m.Type, Version: m.Options["version"]}, nil
}

// List returns the entry names under path (a trailing "/" marks a folder);
// ErrNotFound when it holds nothing.
func (v *Client) List(ctx context.Context, path string) ([]string, error) {
	out, err := v.run(ctx, nil, "list", "-format=json", path)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", path, err)
	}
	var keys []string
	if err := json.Unmarshal([]byte(out), &keys); err != nil {
		return nil, fmt.Errorf("parsing the list of %s: %w", path, err)
	}
	return keys, nil
}

// Read returns the fields of the entry at path; ErrNotFound when there is
// none. Every field must be a string, as every password entry is.
func (v *Client) Read(ctx context.Context, path string) (Data, error) {
	out, err := v.run(ctx, nil, "read", "-format=json", path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var secret struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &secret); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	d := make(Data, len(secret.Data))
	for k, val := range secret.Data {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("reading %s: field %q is a %T, not a string", path, k, val)
		}
		d[k] = s
	}
	return d, nil
}

// Write replaces the entry at path with d (JSON on stdin, so no value is
// ever in the command line).
func (v *Client) Write(ctx context.Context, path string, d Data) error {
	if len(d) == 0 {
		return fmt.Errorf("writing %s: no fields", path)
	}
	body, err := json.Marshal(map[string]string(d))
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := v.run(ctx, body, "write", path, "-"); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
