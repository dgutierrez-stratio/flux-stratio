package envvars

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClientGetter implements Getter against a live cluster via a
// controller-runtime client. Unlike the Python client, which had to
// base64-decode a Secret's values by hand after fetching it as JSON,
// corev1.Secret.Data already comes back decoded — client-go's own
// (un)marshaling handles the base64 encoding on the wire.
type ClientGetter struct {
	Client client.Client
}

// ConfigMap implements Getter.
func (g ClientGetter) ConfigMap(ctx context.Context, namespace, name string) (map[string]string, error) {
	var cm corev1.ConfigMap
	if err := g.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &cm); err != nil {
		return nil, err
	}
	return cm.Data, nil
}

// Secret implements Getter.
func (g ClientGetter) Secret(ctx context.Context, namespace, name string) (map[string]string, error) {
	var secret corev1.Secret
	if err := g.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &secret); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(secret.Data))
	for k, v := range secret.Data {
		result[k] = string(v)
	}
	return result, nil
}
