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
//
// Unlike the Python client, a Secret's values never leave this package:
// a variable read from a Secret resolves to a "<secret:NAME/KEY>"
// placeholder, the same one internal/diff renders a chart's secretKeyRef
// as, so backups, diffs and warnings can't print or store a credential,
// and a patch can't write one into the tenant file. A reference that
// can't be resolved (a missing ConfigMap or key, a failed read) resolves
// to an "<unresolved:...>" placeholder instead of "", so it's never
// mistaken for a real empty value and patched over a chart default.
package envvars

import (
	"context"
	"fmt"
	"sort"

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
		// The kubelet prepends prefix to every key an envFrom source
		// injects; internal/diff's rendered side does the same.
		prefix, _ := entry["prefix"].(string)
		if ref, ok := entry["configMapRef"].(map[string]any); ok {
			mergeFrom(ctx, g.ConfigMap, namespace, ref, prefix, "envFrom configMapRef", false, result, warn)
		}
		if ref, ok := entry["secretRef"].(map[string]any); ok {
			mergeFrom(ctx, g.Secret, namespace, ref, prefix, "envFrom secretRef", true, result, warn)
		}
	}
}

// mergeFrom adds every key of the ConfigMap or Secret ref names to result,
// each under prefix; a Secret's keys get placeholders, not their values.
// A source that can't be read adds nothing — there are no keys to name —
// but is always warned about.
func mergeFrom(ctx context.Context, fetch func(context.Context, string, string) (map[string]string, error), namespace string, ref map[string]any, prefix, label string, secret bool, result map[string]string, warn Warnf) {
	name, _ := ref["name"].(string)
	data, err := fetch(ctx, namespace, name)
	if err != nil {
		warnf(warn, "%s %s/%s: %v", label, namespace, name, err)
		return
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if secret {
			result[prefix+k] = SecretPlaceholder(name, k)
			continue
		}
		result[prefix+k] = data[k]
	}
}

// SecretPlaceholder is what a variable read from key of Secret name
// resolves to — the same form internal/diff renders a chart's
// secretKeyRef as.
func SecretPlaceholder(name, key string) string {
	return "<secret:" + name + "/" + key + ">"
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
		return resolveKeyRef(ctx, g.ConfigMap, namespace, ref, "configMapKeyRef", varName, false, warn)
	}
	if ref, ok := valueFrom["secretKeyRef"].(map[string]any); ok {
		return resolveKeyRef(ctx, g.Secret, namespace, ref, "secretKeyRef", varName, true, warn)
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
	warnf(warn, "env %s: unsupported valueFrom source", varName)
	return "<valueFrom:unknown>"
}

// resolveKeyRef resolves a configMapKeyRef or secretKeyRef: the key's
// value for a ConfigMap, a placeholder for a Secret. A Secret is still
// read, so a missing one is reported like a missing ConfigMap.
func resolveKeyRef(ctx context.Context, fetch func(context.Context, string, string) (map[string]string, error), namespace string, ref map[string]any, label, varName string, secret bool, warn Warnf) string {
	refName, _ := ref["name"].(string)
	key, _ := ref["key"].(string)
	unresolved := "<unresolved:" + label + ":" + refName + "/" + key + ">"
	data, err := fetch(ctx, namespace, refName)
	if err != nil {
		warnf(warn, "env %s: %s %s/%s[%s]: %v", varName, label, namespace, refName, key, err)
		return unresolved
	}
	v, ok := data[key]
	if !ok {
		warnf(warn, "env %s: %s %s/%s has no key %q", varName, label, namespace, refName, key)
		return unresolved
	}
	if secret {
		return SecretPlaceholder(refName, key)
	}
	return v
}

func warnf(warn Warnf, format string, a ...any) {
	if warn != nil {
		warn(format, a...)
	}
}
