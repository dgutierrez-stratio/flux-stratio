package diff

import (
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func scanFiles(t *testing.T, chart string) []ChartFile {
	t.Helper()
	files, err := ScanChartFiles(chart)
	if err != nil {
		t.Fatalf("ScanChartFiles(%q) returned error: %v", chart, err)
	}
	return files
}

func fileByPath(t *testing.T, files []ChartFile, path string) ChartFile {
	t.Helper()
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no scanned file %q among %+v", path, files)
	return ChartFile{}
}

func TestScanChartFiles_Basic(t *testing.T) {
	f := fileByPath(t, scanFiles(t, "testdata/chart"), "config/env_vars.yaml")
	if f.Values["API_LOG_LEVEL"] != "genaiDeveloperProxy.general.log.apiLogLevel" {
		t.Errorf("API_LOG_LEVEL = %q", f.Values["API_LOG_LEVEL"])
	}
	// A bare `{{ .Values.path }}` with no "| quote" filter still matches
	// (the trailing pipe-filter group is optional), matching the ported
	// Python regex exactly.
	if f.Values["NOT_A_MATCH"] != "genaiDeveloperProxy.general.log" {
		t.Errorf("NOT_A_MATCH = %q, want %q", f.Values["NOT_A_MATCH"], "genaiDeveloperProxy.general.log")
	}
	if _, ok := f.Values["LITERAL_VALUE"]; ok {
		t.Error("LITERAL_VALUE is a plain string, not a .Values reference, should not be mapped")
	}
	// ...but it's still one of the file's keys, which ConfigMap
	// attribution matches on.
	want := map[string]bool{"API_LOG_LEVEL": true, "NOT_A_MATCH": true, "LITERAL_VALUE": true}
	if !reflect.DeepEqual(f.Keys, want) {
		t.Errorf("Keys = %v, want %v", f.Keys, want)
	}
}

func TestScanChartFiles_ExcludesChartAndValuesYAML(t *testing.T) {
	for _, f := range scanFiles(t, "testdata/chart") {
		if f.Path == "Chart.yaml" || f.Path == "values.yaml" {
			t.Errorf("scanned %q, want it skipped outright", f.Path)
		}
	}
}

func TestScanChartFiles_KeepsSiblingFilesApart(t *testing.T) {
	files := scanFiles(t, "testdata/siblings")
	api := fileByPath(t, files, "config/api_env_vars.yaml")
	ui := fileByPath(t, files, "config/ui_env_vars.yaml")
	if api.Values["VAULT_ROLE"] != "api.identity.approlename" || ui.Values["VAULT_ROLE"] != "ui.identity.approlename" {
		t.Errorf("VAULT_ROLE: api %q, ui %q — each file must keep its own path", api.Values["VAULT_ROLE"], ui.Values["VAULT_ROLE"])
	}
}

func TestAttributeConfigMaps(t *testing.T) {
	sibling := scanFiles(t, "testdata/siblings")
	flavors := scanFiles(t, "testdata/flavors")

	cases := []struct {
		name          string
		files         []ChartFile
		data          map[string]any
		preferredRoot string
		want          string // attributed file's path, "" for none
	}{
		{
			name:  "exact key set",
			files: sibling,
			data:  map[string]any{"VAULT_ROLE": "r", "LOG_LEVEL": "INFO", "API_PORT": "8080"},
			want:  "config/api_env_vars.yaml",
		},
		{
			name:  "subset of a file's keys is not a match",
			files: sibling,
			data:  map[string]any{"VAULT_ROLE": "r", "LOG_LEVEL": "INFO"},
		},
		{
			name:  "identical key sets without a preferred root stay unattributed",
			files: flavors,
			data:  map[string]any{"DATASTORE_TYPE": "pg"},
		},
		{
			name:          "identical key sets settled by the preferred root",
			files:         flavors,
			data:          map[string]any{"DATASTORE_TYPE": "pg"},
			preferredRoot: "datarestOracle",
			want:          "config/oracle_env_vars.yaml",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docs := []*unstructured.Unstructured{configMapDoc("cm", tc.data)}
			got := AttributeConfigMaps(docs, tc.files, tc.preferredRoot)
			gotPath := ""
			if f := got["cm"]; f != nil {
				gotPath = f.Path
			}
			if gotPath != tc.want {
				t.Errorf("attributed to %q, want %q", gotPath, tc.want)
			}
		})
	}
}

func TestCandidatePaths(t *testing.T) {
	files := scanFiles(t, "testdata/siblings")
	if got, want := candidatePaths(files, "VAULT_ROLE", ""), []string{"api.identity.approlename", "ui.identity.approlename"}; !reflect.DeepEqual(got, want) {
		t.Errorf("candidatePaths = %v, want %v", got, want)
	}
	if got, want := candidatePaths(files, "VAULT_ROLE", "ui"), []string{"ui.identity.approlename"}; !reflect.DeepEqual(got, want) {
		t.Errorf("candidatePaths with preferred root = %v, want %v", got, want)
	}
	if got := candidatePaths(files, "UNKNOWN", ""); len(got) != 0 {
		t.Errorf("candidatePaths(UNKNOWN) = %v, want none", got)
	}
}

func TestIsScannableChartFile(t *testing.T) {
	cases := map[string]bool{
		"config/env_vars.yaml": true,
		"templates/helm.tpl":   true,
		"Chart.yaml":           false,
		"Chart.lock":           false,
		"values.yaml":          false,
		"README.md":            false,
	}
	for path, want := range cases {
		if got := isScannableChartFile(path); got != want {
			t.Errorf("isScannableChartFile(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestScanChartFile_OnlyValuePreservingPipelines: a line is mapped only
// when its pipeline renders the value unchanged, and only when it's
// unindented (a key-value env file's own variable).
func TestScanChartFile_OnlyValuePreservingPipelines(t *testing.T) {
	mapped := map[string]string{
		"PLAIN":     `PLAIN: {{ .Values.a.plain }}`,
		"QUOTE":     `QUOTE: {{ .Values.a.quote | quote }}`,
		"SQUOTE":    `SQUOTE: '{{ .Values.a.squote | squote }}'`,
		"TOSTRING":  `TOSTRING: {{ .Values.a.tostring | toString | quote }}`,
		"DEFAULT":   `DEFAULT: {{ .Values.a.default | default "x" | quote }}`,
		"DEFAULTB":  `DEFAULTB: {{ .Values.a.defaultb | default false | quote }}`,
		"NOSPACE":   `NOSPACE: {{ .Values.a.nospace | quote}}`,
		"TRIM_DASH": `TRIM_DASH: {{- .Values.a.trimdash | quote -}}`,
	}
	notMapped := []string{
		`PRINTF: {{ .Values.a.host | printf "https://%s" | quote }}`,
		`B64: {{ .Values.a.secret | b64enc }}`,
		`UPPER: {{ .Values.a.level | upper | quote }}`,
		`TRIM: {{ .Values.a.url | trimSuffix "/" | quote }}`,
		`  INDENTED: {{ .Values.a.indented | quote }}`,
	}
	var lines []string
	for _, l := range mapped {
		lines = append(lines, l)
	}
	lines = append(lines, notMapped...)
	file := scanChartFile(strings.Join(lines, "\n"))
	for key, line := range mapped {
		if file.Values[key] == "" {
			t.Errorf("%q not mapped", line)
		}
	}
	for _, line := range notMapped {
		key := strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
		if path, ok := file.Values[key]; ok {
			t.Errorf("%q mapped to %q, want it left unmapped", line, path)
		}
	}
}
