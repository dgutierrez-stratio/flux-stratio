package diff

import (
	"reflect"
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
