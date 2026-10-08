// Package vaulttest is an in-memory Vault reached the way vault.Client
// reaches the real one — a kubeclient.FakeExecer that answers the vault CLI
// commands vault.Client runs — plus the cluster objects vault.Connect looks
// up, so tests of anything built on package vault run with no cluster.
package vaulttest

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/vault"
)

// Token is the root token Objects stores and Server accepts by default.
const Token = "s.test-root-token"

// errExit is what a real exec returns for the CLI's non-zero exit.
var errExit = errors.New("command terminated with exit code 2")

// Server is the fake Vault. Entries maps a path ("userland/passwords/a.b/c")
// to its fields; Mounts maps a mount path ("userland/") to its engine.
type Server struct {
	Token   string
	Mounts  map[string]vault.MountInfo
	Entries map[string]map[string]string
	// Exec is the Execer to hand vault.Connect; its Calls record every
	// command as run, stdin (token first) included.
	Exec *kubeclient.FakeExecer
}

// New returns a Server accepting Token with a kv v1 "userland/" mount and
// no entries.
func New() *Server {
	s := &Server{
		Token:   Token,
		Mounts:  map[string]vault.MountInfo{"userland/": {Type: "kv", Version: "1"}},
		Entries: map[string]map[string]string{},
	}
	s.Exec = &kubeclient.FakeExecer{Handle: s.handle}
	return s
}

// Objects are the cluster objects vault.Connect needs: the token Secret
// holding token, and one running server pod named pod.
func Objects(token, pod string) []client.Object {
	return []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: vault.Namespace, Name: vault.TokenSecret},
			Data:       map[string][]byte{vault.TokenKey: []byte(token + "\n")},
		},
		ServerPod(pod, false),
	}
}

// ServerPod is a running Vault server pod, labelled active or not.
func ServerPod(name string, active bool) *corev1.Pod {
	labels := map[string]string{"app.kubernetes.io/name": "vault", "app.kubernetes.io/component": "server"}
	if active {
		labels[vault.ActiveLabel] = "true"
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: vault.Namespace, Name: name, Labels: labels},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// Commands are the vault argument lists run so far, in order.
func (s *Server) Commands() [][]string {
	out := make([][]string, len(s.Exec.Calls))
	for i, c := range s.Exec.Calls {
		out[i] = Args(c)
	}
	return out
}

// Writes are the paths written so far, in order.
func (s *Server) Writes() []string {
	var out []string
	for _, args := range s.Commands() {
		if len(args) > 1 && args[0] == "write" {
			out = append(out, args[1])
		}
	}
	return out
}

// Args is the vault argument list of one call: what follows the
// `sh -c <wrapper> --` prefix.
func Args(c kubeclient.FakeExecCall) []string {
	if len(c.Command) < 4 {
		return nil
	}
	return c.Command[4:]
}

func (s *Server) handle(c kubeclient.FakeExecCall) kubeclient.FakeExecResponse {
	token, rest, _ := strings.Cut(c.Stdin, "\n")
	if token != s.Token {
		return fail("Error making API request.\n\nCode: 403. Errors:\n\n* permission denied")
	}
	args := Args(c)
	switch {
	case len(args) >= 2 && args[0] == "secrets" && args[1] == "list":
		mounts := map[string]any{}
		for p, m := range s.Mounts {
			mounts[p] = map[string]any{"type": m.Type, "options": map[string]string{"version": m.Version}}
		}
		return okJSON(mounts)
	case len(args) == 3 && args[0] == "list":
		return s.list(args[2])
	case len(args) == 3 && args[0] == "read":
		d, found := s.Entries[args[2]]
		if !found {
			return fail("No value found at " + args[2])
		}
		return okJSON(map[string]any{"request_id": "x", "data": d})
	case len(args) == 3 && args[0] == "write" && args[2] == "-":
		var d map[string]string
		if err := json.Unmarshal([]byte(rest), &d); err != nil {
			return fail("Failed to parse JSON input: " + err.Error())
		}
		s.Entries[args[1]] = d
		return ok("Success! Data written to: " + args[1])
	}
	return fail("unexpected vault command " + strings.Join(args, " "))
}

func (s *Server) list(path string) kubeclient.FakeExecResponse {
	prefix := strings.TrimSuffix(path, "/") + "/"
	seen := map[string]bool{}
	for p := range s.Entries {
		rest, found := strings.CutPrefix(p, prefix)
		if !found {
			continue
		}
		if dir, _, isDir := strings.Cut(rest, "/"); isDir {
			seen[dir+"/"] = true
		} else {
			seen[rest] = true
		}
	}
	if len(seen) == 0 {
		return kubeclient.FakeExecResponse{Stdout: "{}\n", Err: errExit}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return okJSON(keys)
}

func ok(stdout string) kubeclient.FakeExecResponse {
	return kubeclient.FakeExecResponse{Stdout: stdout + "\n"}
}

func okJSON(v any) kubeclient.FakeExecResponse {
	b, err := json.Marshal(v)
	if err != nil {
		return fail(err.Error())
	}
	return ok(string(b))
}

func fail(stderr string) kubeclient.FakeExecResponse {
	return kubeclient.FakeExecResponse{Stderr: stderr + "\n", Err: errExit}
}
