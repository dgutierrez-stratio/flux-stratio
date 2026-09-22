// Package kubeclient builds a controller-runtime client from kubeconfig
// flags and provides typed and unstructured get/list/wait helpers for
// reading (and, for the handful of prepare steps that need it, mutating)
// live cluster state. Flux and Stratio custom resources (HelmRelease,
// Kustomization, ResourceSetInputProvider, PgCluster, OsCluster, ...) are
// never imported as API packages — they are read as
// unstructured.Unstructured, matching flux-keos's own internal/kubeclient.
package kubeclient

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// fieldOwner is the field manager flux-stratio identifies itself as on any
// server-side write (e.g. prepare-datamarket-agent's scale-to-zero).
const fieldOwner = "flux-stratio"

// New builds a controller-runtime client from the given kubeconfig flags.
func New(kubeconfigArgs *genericclioptions.ConfigFlags) (client.Client, error) {
	cfg, err := kubeconfigArgs.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig failed: %w", err)
	}

	restMapper, err := kubeconfigArgs.ToRESTMapper()
	if err != nil {
		return nil, err
	}

	scheme, err := NewScheme()
	if err != nil {
		return nil, err
	}

	c, err := client.New(cfg, client.Options{Mapper: restMapper, Scheme: scheme})
	if err != nil {
		return nil, err
	}
	return client.WithFieldOwner(c, fieldOwner), nil
}

// NewScheme returns the typed API scheme flux-stratio registers: the core
// workload/networking/RBAC types it reads or (in prepare steps) mutates
// directly. Everything else — every Flux and Stratio custom resource — is
// handled as unstructured.Unstructured instead, so it never needs adding
// here. Exported so tests can build a matching fake client.
func NewScheme() (*apiruntime.Scheme, error) {
	scheme := apiruntime.NewScheme()
	for _, add := range []func(*apiruntime.Scheme) error{
		corev1.AddToScheme,
		appsv1.AddToScheme,
		rbacv1.AddToScheme,
		networkingv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}
	return scheme, nil
}
