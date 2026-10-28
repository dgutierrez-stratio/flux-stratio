package backup

import (
	"context"
	"strings"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/log"
)

// resolveHelmReleaseValues reads hr's spec.values, falling back — a
// faithful port of the legacy Python migration client's backup.py
// (_run_system_service, its exact resolution order) — to the first
// spec.valuesFrom entry whose kind is ConfigMap and whose name starts
// with "00" (that prefix convention is Stratio's own chart-values
// ConfigMap ordering, not something this plugin invented). Returns a nil
// map, not an error, when nothing resolves — that's a normal outcome
// (Python logs a warning and moves on, it doesn't fail the backup).
func resolveHelmReleaseValues(ctx context.Context, opts Options, hr *unstructured.Unstructured) (map[string]any, error) {
	values, _, _ := unstructured.NestedMap(hr.Object, "spec", "values")
	if len(values) > 0 {
		return values, nil
	}

	valuesFrom, _, _ := unstructured.NestedSlice(hr.Object, "spec", "valuesFrom")
	for _, raw := range valuesFrom {
		entry, ok := raw.(map[string]any)
		if !ok || entry["kind"] != "ConfigMap" {
			continue
		}
		name, _ := entry["name"].(string)
		if !strings.HasPrefix(name, "00") {
			continue
		}
		parsed, ok := valuesFromConfigMap(ctx, opts, hr.GetNamespace(), name, entry)
		if ok {
			return parsed, nil
		}
	}
	return nil, nil
}

func valuesFromConfigMap(ctx context.Context, opts Options, namespace, name string, entry map[string]any) (map[string]any, bool) {
	var cm corev1.ConfigMap
	if err := opts.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &cm); err != nil {
		opts.Log.Debugf("resolving values from ConfigMap %s/%s: %v", namespace, name, err)
		return nil, false
	}
	key, _ := entry["valuesKey"].(string)
	if key == "" {
		key = "values.yaml"
	}
	raw, ok := cm.Data[key]
	if !ok {
		return nil, false
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(raw), &parsed); err != nil {
		opts.Log.Debugf("parsing values from ConfigMap %s/%s key %q: %v", namespace, name, key, err)
		return nil, false
	}
	if len(parsed) == 0 {
		return nil, false
	}
	return parsed, true
}

// writeHelmReleaseFiles writes helmrelease.yaml unconditionally and
// values.yaml only when values resolved to something — matching
// backup.py's own "files_saved" accounting, where the HelmRelease manifest
// is still worth keeping even when no values could be resolved.
func writeHelmReleaseFiles(dir string, hr *unstructured.Unstructured, values map[string]any, l *log.Logger) ([]string, error) {
	if err := writeYAMLFile(dir, "helmrelease.yaml", hr.Object); err != nil {
		return nil, err
	}
	files := []string{"helmrelease.yaml"}
	if len(values) == 0 {
		l.Warningf("no values found in HelmRelease %q spec or ConfigMaps", hr.GetName())
		return files, nil
	}
	if err := writeYAMLFile(dir, "values.yaml", values); err != nil {
		return nil, err
	}
	return append(files, "values.yaml"), nil
}
