package kubeclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/Stratio/flux-stratio/internal/runner"
)

// ExecTimeout bounds a command run inside a pod (a prepare step's SQL),
// so one waiting on a lock can't block a migration forever.
const ExecTimeout = 10 * time.Minute

// Execer runs a command inside a live pod and captures its output — the Go
// equivalent of `kubectl exec`, for the rare prepare step (a data rewrite)
// that no typed or unstructured API call can perform.
type Execer interface {
	Exec(ctx context.Context, namespace, pod, container string, command []string, stdin io.Reader) (stdout, stderr string, err error)
}

// execer is the production Execer, built from the same kubeconfig flags New
// builds the controller-runtime client from.
type execer struct {
	clientset *kubernetes.Clientset
	restCfg   *rest.Config
}

// NewExecer builds an Execer from the given kubeconfig flags.
func NewExecer(kubeconfigArgs *genericclioptions.ConfigFlags) (Execer, error) {
	cfg, err := kubeconfigArgs.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig failed: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building a Kubernetes client: %w", err)
	}
	return &execer{clientset: clientset, restCfg: cfg}, nil
}

// Exec implements Execer. A non-zero exit surfaces as a normal error
// (client-go returns exec.CodeExitError for that), so a real failure
// (bad SQL, connection refused) is caught immediately rather than only
// visible in the captured stderr.
func (e *execer) Exec(ctx context.Context, namespace, pod, container string, command []string, stdin io.Reader) (string, string, error) {
	ctx, cancel := runner.WithDefaultTimeout(ctx, ExecTimeout)
	defer cancel()
	req := e.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(e.restCfg, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("preparing exec into %s/%s: %w", namespace, pod, err)
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("exec into %s/%s: %w", namespace, pod, err)
	}
	return stdout.String(), stderr.String(), nil
}
