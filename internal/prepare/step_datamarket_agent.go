package prepare

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/kubeclient"
)

var stepDatamarketAgent = Step{
	Name:        "prepare-datamarket-agent",
	Description: "suspend and scale down the legacy datamarket-agent HelmRelease before cutover",
	Automated:   true,
	Satisfied:   datamarketAgentSatisfied,
	Run:         runDatamarketAgent,
}

// datamarketAgentGVK is the legacy Ansible-managed HelmRelease this step
// suspends — tenant-aware, unlike the Python client's own version, which
// hardcoded the "stratio-datastores" namespace regardless of --tenant.
var datamarketAgentGVK = schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}

func datamarketAgentNamespace(tenantName string) string {
	return tenantName + "-datastores"
}

func datamarketAgentSatisfied(ctx context.Context, opts Options) (bool, error) {
	ns := datamarketAgentNamespace(opts.TenantName)

	var dep appsv1.Deployment
	err := opts.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: "datamarket-agent"}, &dep)
	if apierrors.IsNotFound(err) {
		return true, nil // nothing left to suspend or scale down
	}
	if err != nil {
		return false, fmt.Errorf("fetching Deployment %s/datamarket-agent: %w", ns, err)
	}
	return dep.Spec.Replicas != nil && *dep.Spec.Replicas == 0, nil
}

func runDatamarketAgent(ctx context.Context, opts Options) error {
	ns := datamarketAgentNamespace(opts.TenantName)

	hr, err := kubeclient.GetUnstructured(ctx, opts.Client, datamarketAgentGVK, ns, "datamarket-agent")
	switch {
	case kubeclient.IsNotFound(err):
		// no HelmRelease to suspend
	case err != nil:
		return fmt.Errorf("fetching HelmRelease %s/datamarket-agent: %w", ns, err)
	default:
		if err := kubeclient.MergePatch(ctx, opts.Client, hr, map[string]any{"spec": map[string]any{"suspend": true}}); err != nil {
			return fmt.Errorf("suspending HelmRelease %s/datamarket-agent: %w", ns, err)
		}
	}

	var dep appsv1.Deployment
	if err := opts.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: "datamarket-agent"}, &dep); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // nothing to scale down
		}
		return fmt.Errorf("fetching Deployment %s/datamarket-agent: %w", ns, err)
	}
	zero := int32(0)
	dep.Spec.Replicas = &zero
	if err := opts.Client.Update(ctx, &dep); err != nil {
		return fmt.Errorf("scaling Deployment %s/datamarket-agent to 0: %w", ns, err)
	}

	return waitForNoPods(ctx, opts.Client, ns, "datamarket-agent", 2*time.Minute)
}

// waitForNoPods polls until no pod labeled app.kubernetes.io/name=appLabel
// remains in namespace, matching `kubectl wait pod --for=delete`.
func waitForNoPods(ctx context.Context, c client.Client, namespace, appLabel string, timeout time.Duration) error {
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		var pods corev1.PodList
		if err := c.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{"app.kubernetes.io/name": appLabel}); err != nil {
			return false, err
		}
		return len(pods.Items) == 0, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for %s pods in %s to terminate: %w", appLabel, namespace, err)
	}
	return nil
}
