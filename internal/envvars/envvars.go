// Package envvars resolves the effective environment variables a
// Kubernetes workload's containers would see at runtime — envFrom
// ConfigMap/Secret references expanded key by key, then each container's
// own env entries, exactly as the kubelet applies them. It is a straight
// port of the Python client's EnvVarExtractor, kept "pure" per the project
// plan: the resolution logic depends only on the Getter interface below,
// never on a concrete cluster client, so it is fully testable without one.
//
// One simplification carried over unchanged from Python: Extract flattens
// every container in a Pod template into a single map, with a later
// container's variables overwriting an earlier one's on a name collision.
// That is what both the Python client and this plugin actually need it
// for — a flat "effective config" view to diff against a chart's rendered
// values — not a per-container inspection tool.
package envvars

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Getter resolves a ConfigMap's or a Secret's key/value data, decoded to
// plain strings. It is the one seam between Extract's pure resolution
// logic and a live cluster.
type Getter interface {
	ConfigMap(ctx context.Context, namespace, name string) (map[string]string, error)
	Secret(ctx context.Context, namespace, name string) (map[string]string, error)
}

// Warnf receives one message per recoverable failure while resolving env
// vars — a referenced ConfigMap, Secret, or key that doesn't exist. The
// workload's other variables still resolve normally; a nil Warnf discards
// warnings.
type Warnf func(format string, a ...any)

// Extract resolves every environment variable workload's containers would
// see at runtime. workload is a Deployment, StatefulSet or DaemonSet
// manifest — anything shaped like
// spec.template.spec.containers[].{env,envFrom} — read as unstructured so
// one function handles all three kinds without three near-identical typed
// code paths.
func Extract(ctx context.Context, g Getter, workload *unstructured.Unstructured, warn Warnf) (map[string]string, error) {
	containers, _, err := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", "containers")
	if err != nil {
		return nil, fmt.Errorf("reading spec.template.spec.containers: %w", err)
	}

	namespace, name := workload.GetNamespace(), workload.GetName()
	result := make(map[string]string)
	for _, c := range containers {
		container, ok := c.(map[string]any)
		if !ok {
			continue
		}
		// envFrom first, then env, so a direct env entry always wins over
		// an envFrom-injected one — standard Kubernetes container semantics.
		applyEnvFrom(ctx, g, namespace, container, result, warn)
		applyEnv(ctx, g, namespace, name, container, result, warn)
	}
	return result, nil
}

func applyEnvFrom(ctx context.Context, g Getter, namespace string, container map[string]any, result map[string]string, warn Warnf) {
	envFrom, _, _ := unstructured.NestedSlice(container, "envFrom")
	for _, e := range envFrom {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if ref, ok := entry["configMapRef"].(map[string]any); ok {
			mergeFrom(ctx, g.ConfigMap, namespace, ref, "envFrom configMapRef", result, warn)
		}
		if ref, ok := entry["secretRef"].(map[string]any); ok {
			mergeFrom(ctx, g.Secret, namespace, ref, "envFrom secretRef", result, warn)
		}
	}
}

func mergeFrom(ctx context.Context, fetch func(context.Context, string, string) (map[string]string, error), namespace string, ref map[string]any, label string, result map[string]string, warn Warnf) {
	name, _ := ref["name"].(string)
	data, err := fetch(ctx, namespace, name)
	if err != nil {
		warnf(warn, "%s %s/%s: %v", label, namespace, name, err)
		return
	}
	for k, v := range data {
		result[k] = v
	}
}

func applyEnv(ctx context.Context, g Getter, namespace, workloadName string, container map[string]any, result map[string]string, warn Warnf) {
	env, _, _ := unstructured.NestedSlice(container, "env")
	for _, e := range env {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if name == "" {
			continue
		}
		if v, ok := entry["value"].(string); ok {
			result[name] = v
			continue
		}
		valueFrom, _ := entry["valueFrom"].(map[string]any)
		result[name] = resolveValueFrom(ctx, g, namespace, workloadName, name, valueFrom, warn)
	}
}

func resolveValueFrom(ctx context.Context, g Getter, namespace, workloadName, varName string, valueFrom map[string]any, warn Warnf) string {
	if valueFrom == nil {
		return ""
	}
	if ref, ok := valueFrom["configMapKeyRef"].(map[string]any); ok {
		return resolveKeyRef(ctx, g.ConfigMap, namespace, ref, "configMapKeyRef", varName, warn)
	}
	if ref, ok := valueFrom["secretKeyRef"].(map[string]any); ok {
		return resolveKeyRef(ctx, g.Secret, namespace, ref, "secretKeyRef", varName, warn)
	}
	if ref, ok := valueFrom["fieldRef"].(map[string]any); ok {
		path, _ := ref["fieldPath"].(string)
		switch path {
		case "metadata.namespace":
			return namespace
		case "metadata.name":
			return workloadName
		default:
			return "<" + path + ">"
		}
	}
	if ref, ok := valueFrom["resourceFieldRef"].(map[string]any); ok {
		resource, _ := ref["resource"].(string)
		return "<" + resource + ">"
	}
	return ""
}

func resolveKeyRef(ctx context.Context, fetch func(context.Context, string, string) (map[string]string, error), namespace string, ref map[string]any, label, varName string, warn Warnf) string {
	refName, _ := ref["name"].(string)
	key, _ := ref["key"].(string)
	data, err := fetch(ctx, namespace, refName)
	if err != nil {
		warnf(warn, "env %s: %s %s/%s[%s]: %v", varName, label, namespace, refName, key, err)
		return ""
	}
	if v, ok := data[key]; ok {
		return v
	}
	warnf(warn, "env %s: %s %s/%s has no key %q", varName, label, namespace, refName, key)
	return ""
}

func warnf(warn Warnf, format string, a ...any) {
	if warn != nil {
		warn(format, a...)
	}
}
