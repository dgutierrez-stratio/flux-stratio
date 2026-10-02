package appdiff

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

// helmSim is a runner.Fake whose `helm template` output follows the values
// it's given, the way a real chart's would: every rendered ConfigMap key a
// chart file maps to a .Values path (diff.ScanChartFiles) is rendered as
// that path's value, when the values file sets it. The canned output is
// otherwise returned as is. It lets a test exercise the re-render of a
// patched chart (verifyPatch) without a real helm binary.
type helmSim struct{ *runner.Fake }

func (h helmSim) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	stdout, stderr, err := h.Fake.Run(ctx, name, args...)
	if err != nil || name != "helm" || len(args) < 3 || args[0] != "template" {
		return stdout, stderr, err
	}
	chartPath, valuesFile := args[2], ""
	for i, a := range args {
		if a == "-f" && i+1 < len(args) {
			valuesFile = args[i+1]
		}
	}
	data, err := os.ReadFile(valuesFile)
	if err != nil {
		return nil, nil, err
	}
	var values map[string]any
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, nil, err
	}
	files, err := diff.ScanChartFiles(chartPath)
	if err != nil {
		return nil, nil, err
	}
	docs, err := yamldocs.Decode(stdout)
	if err != nil {
		return nil, nil, err
	}
	attributed := diff.AttributeConfigMaps(docs, files, "")
	var out strings.Builder
	for _, d := range docs {
		if file := attributed[d.GetName()]; file != nil {
			cmData, _ := d.Object["data"].(map[string]any)
			for key, path := range file.Values {
				if v, ok := lookupDotPath(values, path); ok {
					cmData[key] = fmt.Sprint(v)
				}
			}
		}
		applyResourceValues(d, values)
		b, err := yaml.Marshal(d.Object)
		if err != nil {
			return nil, nil, err
		}
		out.WriteString("---\n")
		out.Write(b)
	}
	return []byte(out.String()), stderr, nil
}

// applyResourceValues renders a workload's container resources from the
// bjw-s controllers.<controller>.containers.<container>.resources values,
// a null deleting the key as Helm does.
func applyResourceValues(w *unstructured.Unstructured, values map[string]any) {
	controllers, _ := values["controllers"].(map[string]any)
	containers, _, _ := unstructured.NestedSlice(w.Object, "spec", "template", "spec", "containers")
	for _, ctrl := range controllers {
		spec, _ := ctrl.(map[string]any)
		declared, _ := spec["containers"].(map[string]any)
		for i, c := range containers {
			container, _ := c.(map[string]any)
			name, _ := container["name"].(string)
			decl, _ := declared[name].(map[string]any)
			res, _ := decl["resources"].(map[string]any)
			for list, raw := range res {
				entries, _ := raw.(map[string]any)
				for k, v := range entries {
					if v == nil {
						unstructured.RemoveNestedField(container, "resources", list, k)
						continue
					}
					_ = unstructured.SetNestedField(container, v, "resources", list, k)
				}
			}
			containers[i] = container
		}
	}
	_ = unstructured.SetNestedSlice(w.Object, containers, "spec", "template", "spec", "containers")
}

func lookupDotPath(m map[string]any, path string) (any, bool) {
	var cur any = m
	for _, p := range strings.Split(path, ".") {
		next, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = next[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}
