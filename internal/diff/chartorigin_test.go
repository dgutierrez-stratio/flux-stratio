package diff

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClassifyChartLine(t *testing.T) {
	defines := map[string]string{
		"common.dependencies.postgres.host":      `{{ .Values.dependencies.postgres.name }}.{{ .Values.dependencies.postgres.namespace }}`,
		"common.dependencies.postgres.port":      `{{ include "common.dependencies.postgres.portValue" . }}`,
		"common.dependencies.postgres.portValue": `{{ .Values.dependencies.postgres.port | default 5432 }}`,
		"loop.a":                                 `{{ include "loop.b" . }}`,
		"loop.b":                                 `{{ include "loop.a" . }}{{ .Values.x }}`,
	}
	tests := []struct {
		name string
		raw  string
		want ChartOrigin
	}{
		{"a literal", "https://governance-discovery-manager.keos-core:61000/discovery", ChartOrigin{Kind: OriginFixed}},
		{"an empty value", "", ChartOrigin{Kind: OriginFixed}},
		{"an action that only writes a literal", `{{ "false" }}`, ChartOrigin{Kind: OriginFixed}},
		{"the release name", "{{ .Release.Name }}", ChartOrigin{Kind: OriginRelease}},
		{"namespace and name", `"{{ .Release.Namespace }}_{{ .Release.Name }}"`, ChartOrigin{Kind: OriginRelease}},
		{"one value through a transform", `{{ .Values.a.b | upper }}`, ChartOrigin{Kind: OriginComposed, Values: []string{"a.b"}}},
		{"several values", `{{ .Values.a }}-{{ .Values.b.c }}`, ChartOrigin{Kind: OriginComposed, Values: []string{"a", "b.c"}}},
		{"helpers, followed into the values they read",
			`jdbc:postgresql://{{ include "common.dependencies.postgres.host" . }}:{{ include "common.dependencies.postgres.port" . }}/db`,
			ChartOrigin{Kind: OriginComposed, Values: []string{"dependencies.postgres.name", "dependencies.postgres.namespace", "dependencies.postgres.port"}}},
		{"a helper that isn't known", `{{ include "elsewhere" . }}`, ChartOrigin{Kind: OriginComposed}},
		{"a release name next to a value", `{{ .Release.Name }}-{{ .Values.suffix }}`, ChartOrigin{Kind: OriginComposed, Values: []string{"suffix"}}},
		{"helpers that include each other don't loop", `{{ include "loop.a" . }}`, ChartOrigin{Kind: OriginComposed, Values: []string{"x"}}},
	}
	for _, tt := range tests {
		got := classifyChartLine(tt.raw, defines)
		if got.Kind != tt.want.Kind || !reflect.DeepEqual(got.Values, tt.want.Values) {
			t.Errorf("%s:\n got  %+v\n want %+v", tt.name, got, tt.want)
		}
	}
}

// ScanChartDefines reads the chart's own templates and those of a library
// chart it takes from a local directory.
func TestScanChartDefines_FollowsLocalDependencies(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app/Chart.yaml", "name: app\ndependencies:\n  - name: common\n    repository: file://../common\n  - name: remote\n    repository: https://example.com\n")
	write("app/templates/_own.tpl", `{{- define "app.name" -}}{{ .Values.app.name }}{{- end -}}`)
	write("common/templates/_pg.tpl", "{{- define \"common.pg.host\" -}}\n{{ .Values.dependencies.postgres.name }}\n{{- end -}}\n{{- define \"common.pg.port\" -}}\n5432\n{{- end -}}")

	defines := ScanChartDefines(filepath.Join(root, "app"))
	for _, name := range []string{"app.name", "common.pg.host", "common.pg.port"} {
		if _, ok := defines[name]; !ok {
			t.Errorf("define %q not found in %v", name, defines)
		}
	}
	if got := classifyChartLine(`{{ include "common.pg.host" . }}`, defines); !reflect.DeepEqual(got.Values, []string{"dependencies.postgres.name"}) {
		t.Errorf("the host helper's values = %v", got.Values)
	}
	// A body stops at the next define, so one helper's values never leak into another.
	if got := classifyChartLine(`{{ include "common.pg.port" . }}`, defines); len(got.Values) != 0 {
		t.Errorf("the port helper reads %v, want nothing", got.Values)
	}
}
