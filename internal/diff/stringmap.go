package diff

import (
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// stringMapAt reads the string map at fields of obj the way the API server
// decodes it into a map[string]string: a null value is "" (what a chart
// renders for `KEY: {{ .Values.x | quote }}` when .Values.x is nil, since
// sprig's quote drops a nil), and a bool or number is its text.
// unstructured.NestedStringMap instead errors on the first non-string value
// and returns an empty map, so one null emptied a whole ConfigMap. A map or
// a list as a value is an error (the API server rejects it too).
func stringMapAt(obj map[string]any, fields ...string) (map[string]string, bool, error) {
	raw, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil || !found || raw == nil {
		return nil, false, err
	}
	in, ok := raw.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%v is a %T, expected a map", fields, raw)
	}
	m := make(map[string]string, len(in))
	for k, v := range in {
		switch s := v.(type) {
		case nil:
			m[k] = ""
		case string:
			m[k] = s
		case bool, int, int32, int64, float32, float64:
			m[k] = fmt.Sprint(s)
		default:
			return nil, false, fmt.Errorf("%v: key %q holds a %T, expected a scalar", fields, k, v)
		}
	}
	return m, true, nil
}

// nullKeys lists, sorted, the keys of the map at fields of obj whose value
// is null: what a chart rendered for a nil .Values path.
func nullKeys(obj map[string]any, fields ...string) []string {
	raw, _, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	in, _ := raw.(map[string]any)
	var keys []string
	for k, v := range in {
		if v == nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// NullConfigMapKeys lists, by ConfigMap name, the data keys docs render as
// null — a nil .Values path piped through `quote`. The API server stores
// them as "" and the diff reads them that way; the caller should say so,
// because the usual cause is an unquoted ${VAR} substitution that came out
// empty in the app's HelmRelease values.
func NullConfigMapKeys(docs []*unstructured.Unstructured) map[string][]string {
	out := map[string][]string{}
	for _, d := range docs {
		if d.GetKind() != "ConfigMap" {
			continue
		}
		if keys := nullKeys(d.Object, "data"); len(keys) > 0 {
			out[d.GetName()] = keys
		}
	}
	return out
}

// RenderedConfigMapErrors lists the rendered ConfigMaps whose data the API
// server would reject (a map or a list as a value, or data that isn't a map):
// the chart would not install, so comparing against it means nothing.
func RenderedConfigMapErrors(docs []*unstructured.Unstructured) []error {
	var errs []error
	for _, d := range docs {
		if d.GetKind() != "ConfigMap" {
			continue
		}
		if _, _, err := stringMapAt(d.Object, "data"); err != nil {
			errs = append(errs, fmt.Errorf("rendered ConfigMap %s: %w (the API server rejects it, the chart would not install)", d.GetName(), err))
		}
	}
	return errs
}
