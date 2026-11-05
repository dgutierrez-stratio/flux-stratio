package diff

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// probedWorkload is a Deployment whose main container has a readiness probe
// (failureThreshold 1, periodSeconds 30) and nothing else.
func probedWorkload() []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		configMapDoc("app-config", map[string]any{"FOO": "x"}),
		deploymentDoc("app", []any{map[string]any{
			"name":           "main",
			"readinessProbe": map[string]any{"failureThreshold": int64(1), "periodSeconds": int64(30)},
		}}),
	}
}

func TestNativeProbe(t *testing.T) {
	w := probedWorkload()[1]
	for _, tt := range []struct {
		name, wantField, wantValue string
	}{
		{"READINESS_PROBE_FAILURE_THRESHOLD", "readinessProbe.failureThreshold", "1"},
		{"READINESS_PROBE_PERIOD_SECONDS", "readinessProbe.periodSeconds", "30"},
		{"READINESS_PROBE_TIMEOUT_SECONDS", "", ""},  // the probe doesn't set it
		{"LIVENESS_PROBE_FAILURE_THRESHOLD", "", ""}, // the workload has no liveness probe
		{"READINESS_PROBE_UNKNOWN", "", ""},
		{"FOO", "", ""},
	} {
		field, value := nativeProbe(w, tt.name)
		if field != tt.wantField || value != tt.wantValue {
			t.Errorf("nativeProbe(%q) = (%q, %q), want (%q, %q)", tt.name, field, value, tt.wantField, tt.wantValue)
		}
	}
	if field, _ := nativeProbe(nil, "READINESS_PROBE_FAILURE_THRESHOLD"); field != "" {
		t.Errorf("nativeProbe(nil workload) = %q, want none", field)
	}
}

func TestChartDiff_ProbeVariableCarriedNativelyIsNotLost(t *testing.T) {
	docs := probedWorkload()
	result := ChartDiff(ChartDiffInput{
		Rendered: docs, HRName: "hr",
		Live: []LiveWorkloadEnv{liveFor(docs, "app", map[string]string{
			"READINESS_PROBE_FAILURE_THRESHOLD": "1",  // same as the chart's probe
			"READINESS_PROBE_PERIOD_SECONDS":    "10", // differs from the chart's probe
			"LIVENESS_PROBE_PERIOD_SECONDS":     "10", // the chart has no such probe
			"FOO":                               "x",
		})},
	})

	wantNative := []LiveOnlyVar{{
		Workload: "app", Name: "READINESS_PROBE_FAILURE_THRESHOLD", Live: "1",
		NativeField: "readinessProbe.failureThreshold", NativeValue: "1",
	}}
	if len(result.Native) != 1 || result.Native[0] != wantNative[0] {
		t.Errorf("Native = %+v, want %+v", result.Native, wantNative)
	}
	lost := map[string]LiveOnlyVar{}
	for _, v := range result.LiveOnly {
		lost[v.Name] = v
	}
	if len(lost) != 2 {
		t.Fatalf("LiveOnly = %+v, want the differing and the unexpressed probe variables", result.LiveOnly)
	}
	if got := lost["READINESS_PROBE_PERIOD_SECONDS"]; got.NativeField != "readinessProbe.periodSeconds" || got.NativeValue != "30" {
		t.Errorf("differing probe variable = %+v, want the native field and its value kept for the report", got)
	}
	if got := lost["LIVENESS_PROBE_PERIOD_SECONDS"]; got.NativeField != "" {
		t.Errorf("probe variable with no native probe = %+v, want no native field", got)
	}
}
