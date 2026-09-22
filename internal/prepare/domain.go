package prepare

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterExternalDomain reads CLUSTER_EXTERNAL_DOMAIN from the
// flux-system/keos-runtime-info ConfigMap — every step that names a
// legacy domain-based Ingress needs it, matching the Python client's own
// lookup.
func clusterExternalDomain(ctx context.Context, c client.Client) (string, error) {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: "flux-system", Name: "keos-runtime-info"}, &cm); err != nil {
		return "", fmt.Errorf("reading flux-system/keos-runtime-info: %w", err)
	}
	domain := cm.Data["CLUSTER_EXTERNAL_DOMAIN"]
	if domain == "" {
		return "", fmt.Errorf("flux-system/keos-runtime-info has no CLUSTER_EXTERNAL_DOMAIN key")
	}
	return domain, nil
}
