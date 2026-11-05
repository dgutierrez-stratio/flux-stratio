package diff

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func workload(name string, replicas any, containers ...map[string]any) *unstructured.Unstructured {
	cs := make([]any, len(containers))
	for i, c := range containers {
		cs[i] = c
	}
	spec := map[string]any{"template": map[string]any{"spec": map[string]any{"containers": cs}}}
	if replicas != nil {
		spec["replicas"] = replicas
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"kind": "Deployment", "metadata": map[string]any{"name": name}, "spec": spec,
	}}
}

func container(name, image string, requests, limits map[string]any) map[string]any {
	c := map[string]any{"name": name, "image": image}
	res := map[string]any{}
	if requests != nil {
		res["requests"] = requests
	}
	if limits != nil {
		res["limits"] = limits
	}
	if len(res) > 0 {
		c["resources"] = res
	}
	return c
}

func specKey(d SpecDiff) string { return d.Container + " " + d.Field }

func TestCompareWorkloadSpecs(t *testing.T) {
	tests := []struct {
		name           string
		rendered, live *unstructured.Unstructured
		want           map[string]SpecDiff // by container + field
	}{
		{
			name:     "equal quantities written differently are no difference",
			rendered: workload("a", int64(1), container("c", "r/img:1", nil, map[string]any{"memory": "4Gi", "cpu": "1000m"})),
			live:     workload("a", int64(1), container("c", "r/img:1", nil, map[string]any{"memory": "4096Mi", "cpu": "1"})),
			want:     map[string]SpecDiff{},
		},
		{
			name:     "less memory rendered than live is a risk",
			rendered: workload("a", nil, container("c", "r/img:1", map[string]any{"memory": "2048Mi"}, map[string]any{"memory": "2048Mi"})),
			live:     workload("a", nil, container("c", "r/img:1", map[string]any{"memory": "4096M"}, map[string]any{"memory": "4096M"})),
			want: map[string]SpecDiff{
				"c resources.requests.memory": {Rendered: "2048Mi", Live: "4096M", Severity: SpecRisk},
				"c resources.limits.memory":   {Rendered: "2048Mi", Live: "4096M", Severity: SpecRisk},
			},
		},
		{
			name:     "more memory rendered than live is information",
			rendered: workload("a", nil, container("c", "r/img:1", nil, map[string]any{"memory": "7168Mi"})),
			live:     workload("a", nil, container("c", "r/img:1", nil, map[string]any{"memory": "768M"})),
			want:     map[string]SpecDiff{"c resources.limits.memory": {Rendered: "7168Mi", Live: "768M", Severity: SpecInfo}},
		},
		{
			name:     "a limit where live had none is a risk",
			rendered: workload("a", nil, container("c", "r/img:1", nil, map[string]any{"memory": "512M"})),
			live:     workload("a", nil, container("c", "r/img:1", nil, nil)),
			want:     map[string]SpecDiff{"c resources.limits.memory": {Rendered: "512M", Live: "", Severity: SpecRisk}},
		},
		{
			name:     "a request where live had none is information",
			rendered: workload("a", nil, container("c", "r/img:1", map[string]any{"cpu": "500m"}, nil)),
			live:     workload("a", nil, container("c", "r/img:1", nil, nil)),
			want:     map[string]SpecDiff{"c resources.requests.cpu": {Rendered: "500m", Live: "", Severity: SpecInfo}},
		},
		{
			name:     "cpu limit lowered is information",
			rendered: workload("a", nil, container("c", "r/img:1", nil, map[string]any{"cpu": "1"})),
			live:     workload("a", nil, container("c", "r/img:1", nil, map[string]any{"cpu": "4"})),
			want:     map[string]SpecDiff{"c resources.limits.cpu": {Rendered: "1", Live: "4", Severity: SpecInfo}},
		},
		{
			name:     "a single container pairs whatever it is named",
			rendered: workload("a", nil, container("api", "r/img:1", nil, map[string]any{"memory": "1Gi"})),
			live:     workload("a", nil, container("genai-api", "r/img:1", nil, map[string]any{"memory": "2Gi"})),
			want:     map[string]SpecDiff{"api resources.limits.memory": {Rendered: "1Gi", Live: "2Gi", Severity: SpecRisk}},
		},
		{
			name: "several containers pair by name; an unmatched one is skipped",
			rendered: workload("a", nil,
				container("one", "r/img:1", nil, map[string]any{"memory": "1Gi"}),
				container("two", "r/img:1", nil, map[string]any{"memory": "1Gi"})),
			live: workload("a", nil,
				container("one", "r/img:1", nil, map[string]any{"memory": "2Gi"}),
				container("other", "r/img:1", nil, map[string]any{"memory": "9Gi"})),
			want: map[string]SpecDiff{"one resources.limits.memory": {Rendered: "1Gi", Live: "2Gi", Severity: SpecRisk}},
		},
		{
			name:     "a different image tag is a risk",
			rendered: workload("a", nil, container("c", "reg/dg-agent:1.1.1", nil, nil)),
			live:     workload("a", nil, container("c", "reg/dg-agent:1.2.0", nil, nil)),
			want:     map[string]SpecDiff{"c image": {Rendered: "reg/dg-agent:1.1.1", Live: "reg/dg-agent:1.2.0", Severity: SpecRisk}},
		},
		{
			name:     "a build suffix alone is information",
			rendered: workload("a", nil, container("c", "reg/genai-api:0.8.0", nil, nil)),
			live:     workload("a", nil, container("c", "reg/genai-api:0.8.0-6dd7bb1", nil, nil)),
			want:     map[string]SpecDiff{"c image": {Rendered: "reg/genai-api:0.8.0", Live: "reg/genai-api:0.8.0-6dd7bb1", Severity: SpecInfo}},
		},
		{
			name:     "a registry port is not a tag",
			rendered: workload("a", nil, container("c", "reg:5000/img", nil, nil)),
			live:     workload("a", nil, container("c", "reg:5000/img:2", nil, nil)),
			want:     map[string]SpecDiff{"c image": {Rendered: "reg:5000/img", Live: "reg:5000/img:2", Severity: SpecRisk}},
		},
		{
			name:     "replicas 0 to 1 is a risk",
			rendered: workload("a", int64(1), container("c", "r/img:1", nil, nil)),
			live:     workload("a", int64(0), container("c", "r/img:1", nil, nil)),
			want:     map[string]SpecDiff{" replicas": {Rendered: "1", Live: "0", Severity: SpecRisk}},
		},
		{
			name:     "unset replicas count as 1",
			rendered: workload("a", nil, container("c", "r/img:1", nil, nil)),
			live:     workload("a", int64(1), container("c", "r/img:1", nil, nil)),
			want:     map[string]SpecDiff{},
		},
		{
			name:     "other replica differences are information",
			rendered: workload("a", int64(1), container("c", "r/img:1", nil, nil)),
			live:     workload("a", int64(3), container("c", "r/img:1", nil, nil)),
			want:     map[string]SpecDiff{" replicas": {Rendered: "1", Live: "3", Severity: SpecInfo}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := map[string]SpecDiff{}
			for _, d := range CompareWorkloadSpecs(tt.rendered, tt.live) {
				got[specKey(d)] = d
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d diffs %+v, want %d %+v", len(got), got, len(tt.want), tt.want)
			}
			for k, want := range tt.want {
				d, ok := got[k]
				if !ok {
					t.Errorf("missing diff %q in %+v", k, got)
					continue
				}
				if d.Rendered != want.Rendered || d.Live != want.Live || d.Severity != want.Severity || d.Workload != "a" {
					t.Errorf("%s = %+v, want rendered %q live %q %s", k, d, want.Rendered, want.Live, want.Severity)
				}
			}
		})
	}
}

func TestCompareWorkloadSpecs_NoLiveSideIsSilent(t *testing.T) {
	if got := CompareWorkloadSpecs(workload("a", nil), nil); got != nil {
		t.Errorf("got %+v, want none", got)
	}
	live := []LiveWorkloadEnv{{Rendered: workload("a", nil)}}
	if got := CompareLiveSpecs(live); got != nil {
		t.Errorf("CompareLiveSpecs with no live manifest = %+v, want none", got)
	}
}

func withEnv(w *unstructured.Unstructured, env map[string]string) *unstructured.Unstructured {
	var list []any
	for k, v := range env {
		list = append(list, map[string]any{"name": k, "value": v})
	}
	cs, _, _ := unstructured.NestedSlice(w.Object, "spec", "template", "spec", "containers")
	c := cs[0].(map[string]any)
	c["env"] = list
	_ = unstructured.SetNestedSlice(w.Object, cs, "spec", "template", "spec", "containers")
	return w
}

func TestCheckEnvAgainstLimits(t *testing.T) {
	limited := func(limit string, env map[string]string) *unstructured.Unstructured {
		var limits map[string]any
		if limit != "" {
			limits = map[string]any{"memory": limit}
		}
		return withEnv(workload("a", nil, container("agent", "r/i:1", nil, limits)), env)
	}
	tests := []struct {
		name      string
		w         *unstructured.Unstructured
		memoryEnv []string
		want      []string // Field of each expected diff
	}{
		{"heap bigger than the limit", limited("2048Mi", map[string]string{"JAVA_OPTS": "-Xms512m -Xmx3072m -XX:+UseG1GC"}), nil, []string{"env-vs-limit:JAVA_OPTS"}},
		{"heap in gigabytes", limited("2Gi", map[string]string{"JAVA_OPTS": "-Xmx3g"}), nil, []string{"env-vs-limit:JAVA_OPTS"}},
		{"heap inside the limit", limited("4096M", map[string]string{"JAVA_OPTS": "-Xmx3072m"}), nil, nil},
		{"a named size variable in MB", limited("2048Mi", map[string]string{"VR_POD_MEMORY_LIMIT": "4096"}), []string{"VR_POD_MEMORY_LIMIT"}, []string{"env-vs-limit:VR_POD_MEMORY_LIMIT"}},
		{"a size variable equal to the limit in decimal MB", limited("4096M", map[string]string{"MEMORY": "4096"}), []string{"MEMORY"}, nil},
		{"a size variable nobody named is not read", limited("2048Mi", map[string]string{"VR_POD_MEMORY_LIMIT": "4096"}), nil, nil},
		{"no limit to compare with", limited("", map[string]string{"JAVA_OPTS": "-Xmx3072m"}), nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckEnvAgainstLimits([]*unstructured.Unstructured{tt.w}, []string{"a"}, tt.memoryEnv)
			var fields []string
			for _, d := range got {
				fields = append(fields, d.Field)
				if d.Severity != SpecRisk || d.Workload != "a" || d.Container != "agent" {
					t.Errorf("diff = %+v, want a risk on a/agent", d)
				}
			}
			if !reflect.DeepEqual(fields, tt.want) {
				t.Errorf("fields = %v, want %v", fields, tt.want)
			}
		})
	}
	t.Run("a workload that isn't compared is skipped", func(t *testing.T) {
		w := limited("1Gi", map[string]string{"JAVA_OPTS": "-Xmx3072m"})
		if got := CheckEnvAgainstLimits([]*unstructured.Unstructured{w}, []string{"other"}, nil); len(got) != 0 {
			t.Errorf("got %+v, want none", got)
		}
	})
}

func withSelector(w *unstructured.Unstructured, labels map[string]any) *unstructured.Unstructured {
	_ = unstructured.SetNestedMap(w.Object, labels, "spec", "selector", "matchLabels")
	return w
}

func TestCompareWorkloadSpecs_SelectorIsAnImmutableRisk(t *testing.T) {
	legacy := withSelector(workload("a", nil), map[string]any{"cct.stratio.com/application_id": "a.ns"})
	chart := withSelector(workload("a", nil), map[string]any{"app.kubernetes.io/name": "a", "app.kubernetes.io/instance": "a"})

	got := CompareWorkloadSpecs(chart, legacy)
	if len(got) != 1 || got[0].Field != SelectorField || got[0].Severity != SpecRisk ||
		got[0].Rendered != "app.kubernetes.io/instance=a,app.kubernetes.io/name=a" || got[0].Live != "cct.stratio.com/application_id=a.ns" {
		t.Errorf("got %+v, want one selector risk", got)
	}
	if got := CompareWorkloadSpecs(legacy, legacy); len(got) != 0 {
		t.Errorf("equal selectors: got %+v, want none", got)
	}
	if got := CompareWorkloadSpecs(workload("a", nil), legacy); len(got) != 0 {
		t.Errorf("no rendered selector to compare: got %+v, want none", got)
	}
}
