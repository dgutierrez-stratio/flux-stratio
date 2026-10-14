package diff

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// probeVarPrefixes maps the prefix of a legacy probe variable to the
// container field that carries the probe natively.
var probeVarPrefixes = map[string]string{
	"LIVENESS_PROBE_":  "livenessProbe",
	"READINESS_PROBE_": "readinessProbe",
	"STARTUP_PROBE_":   "startupProbe",
}

// probeVarFields maps the suffix of a legacy probe variable to the
// container probe field it sets.
var probeVarFields = map[string]string{
	"FAILURE_THRESHOLD":     "failureThreshold",
	"INITIAL_DELAY_SECONDS": "initialDelaySeconds",
	"PERIOD_SECONDS":        "periodSeconds",
	"SUCCESS_THRESHOLD":     "successThreshold",
	"TIMEOUT_SECONDS":       "timeoutSeconds",
}

// nativeProbe looks for the container probe field the legacy probe variable
// name stands for (the Command Center injected a descriptor's probe settings
// as environment variables; a chart sets them on the container instead) in
// the rendered workload. It returns the field ("readinessProbe.failureThreshold")
// and its rendered value, or "" when name isn't a probe variable or the
// workload has no such probe field.
func nativeProbe(workload *unstructured.Unstructured, name string) (field, value string) {
	if workload == nil {
		return "", ""
	}
	probe, setting := "", ""
	for prefix, p := range probeVarPrefixes {
		if rest, ok := strings.CutPrefix(name, prefix); ok {
			if f, ok := probeVarFields[rest]; ok {
				probe, setting = p, f
			}
		}
	}
	if probe == "" {
		return "", ""
	}
	containers, _, _ := unstructured.NestedSlice(workload.Object, workloadContainersPath(workload)...)
	for _, c := range containers {
		container, _ := c.(map[string]any)
		spec, _ := container[probe].(map[string]any)
		if v, ok := spec[setting]; ok {
			return probe + "." + setting, fmt.Sprint(v)
		}
	}
	return "", ""
}

// workloadContainersPath is where a workload kind keeps its containers: a
// CronJob nests its pod template one level deeper.
func workloadContainersPath(workload *unstructured.Unstructured) []string {
	if workload.GetKind() == "CronJob" {
		return []string{"spec", "jobTemplate", "spec", "template", "spec", "containers"}
	}
	return []string{"spec", "template", "spec", "containers"}
}
