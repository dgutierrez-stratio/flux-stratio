package kubeclient

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IgnoreNotFound returns nil if err is a Kubernetes "not found" error,
// matching `kubectl get --ignore-not-found` semantics; otherwise it
// returns err unchanged.
func IgnoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// IsNotFound reports whether err is a Kubernetes "not found" error.
func IsNotFound(err error) bool {
	return apierrors.IsNotFound(err)
}

// IsNoMatch reports whether err says the kind isn't served by the cluster
// at all — its CRD isn't installed.
func IsNoMatch(err error) bool {
	return apimeta.IsNoMatchError(err)
}

// GetUnstructured fetches a single object of the given kind by namespace
// and name, without requiring its API package to be registered in the
// client's scheme — the mechanism every Flux and Stratio custom resource
// read in this plugin goes through.
func GetUnstructured(ctx context.Context, c client.Client, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// ListUnstructured lists every object of the given kind. An empty
// namespace lists across all namespaces, matching `kubectl get <kind> -A`.
func ListUnstructured(ctx context.Context, c client.Client, gvk schema.GroupVersionKind, namespace string) (*unstructured.UnstructuredList, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := c.List(ctx, list, opts...); err != nil {
		return nil, err
	}
	return list, nil
}

// MergePatch applies a JSON merge patch to obj, matching `kubectl patch
// --type merge`. patch is marshaled to JSON.
func MergePatch(ctx context.Context, c client.Client, obj client.Object, patch map[string]any) error {
	data, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshaling merge patch: %w", err)
	}
	return c.Patch(ctx, obj, client.RawPatch(types.MergePatchType, data))
}
