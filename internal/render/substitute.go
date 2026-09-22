package render

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// resolveSubstituteFrom returns a copy of ks with every
// spec.postBuild.substituteFrom entry fetched from the live cluster and
// inlined into spec.postBuild.substitute (never overwriting a key already
// there, matching the Python client's merge rule), then substituteFrom
// removed — required because `flux build --dry-run` cannot reach the
// cluster itself. ks is never mutated.
func resolveSubstituteFrom(ctx context.Context, c client.Client, ks *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	out := ks.DeepCopy()

	subFrom, found, err := unstructured.NestedSlice(out.Object, "spec", "postBuild", "substituteFrom")
	if err != nil {
		return nil, err
	}
	if !found || len(subFrom) == 0 {
		return out, nil
	}

	substitute, _, err := unstructured.NestedStringMap(out.Object, "spec", "postBuild", "substitute")
	if err != nil {
		return nil, err
	}
	if substitute == nil {
		substitute = map[string]string{}
	}
	namespace := out.GetNamespace()

	for _, item := range subFrom {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := entry["kind"].(string)
		name, _ := entry["name"].(string)
		if name == "" {
			continue
		}

		data, err := fetchKeyValues(ctx, c, kind, namespace, name)
		if err != nil {
			return nil, err
		}
		for k, v := range data {
			if _, exists := substitute[k]; !exists {
				substitute[k] = v
			}
		}
	}

	if err := unstructured.SetNestedStringMap(out.Object, substitute, "spec", "postBuild", "substitute"); err != nil {
		return nil, err
	}
	unstructured.RemoveNestedField(out.Object, "spec", "postBuild", "substituteFrom")
	return out, nil
}

func fetchKeyValues(ctx context.Context, c client.Client, kind, namespace, name string) (map[string]string, error) {
	switch kind {
	case "ConfigMap":
		var cm corev1.ConfigMap
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &cm); err != nil {
			return nil, err
		}
		return cm.Data, nil
	case "Secret":
		var secret corev1.Secret
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &secret); err != nil {
			return nil, err
		}
		out := make(map[string]string, len(secret.Data))
		for k, v := range secret.Data {
			out[k] = string(v)
		}
		return out, nil
	default:
		return nil, nil
	}
}
