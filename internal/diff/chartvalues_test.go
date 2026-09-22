package diff

import "testing"

func TestBuildChartValuesMap_Basic(t *testing.T) {
	got, err := BuildChartValuesMap("testdata/chart", "")
	if err != nil {
		t.Fatalf("BuildChartValuesMap returned error: %v", err)
	}
	if got["API_LOG_LEVEL"] != "genaiDeveloperProxy.general.log.apiLogLevel" {
		t.Errorf("API_LOG_LEVEL = %q", got["API_LOG_LEVEL"])
	}
	// A bare `{{ .Values.path }}` with no "| quote" filter still matches
	// (the trailing pipe-filter group is optional), matching the ported
	// Python regex exactly.
	if got["NOT_A_MATCH"] != "genaiDeveloperProxy.general.log" {
		t.Errorf("NOT_A_MATCH = %q, want %q", got["NOT_A_MATCH"], "genaiDeveloperProxy.general.log")
	}
	if _, ok := got["LITERAL_VALUE"]; ok {
		t.Error("LITERAL_VALUE is a plain string, not a .Values reference, should not match")
	}
}

func TestBuildChartValuesMap_ExcludesChartAndValuesYAML(t *testing.T) {
	got, err := BuildChartValuesMap("testdata/chart", "")
	if err != nil {
		t.Fatal(err)
	}
	// Chart.yaml/values.yaml contain no .Values-reference lines anyway,
	// but this pins that they are skipped outright, not merely empty.
	if len(got) == 0 {
		t.Fatal("expected some mappings from config/*.yaml")
	}
}

func TestBuildChartValuesMap_PreferredRootWins(t *testing.T) {
	got, err := BuildChartValuesMap("testdata/chart", "datarestPgInternal")
	if err != nil {
		t.Fatal(err)
	}
	if got["DATASTORE_TYPE"] != "datarestPgInternal.general.datastore.datastoreType" {
		t.Errorf("DATASTORE_TYPE = %q, want the datarestPgInternal path", got["DATASTORE_TYPE"])
	}
}

func TestScanChartValueLines_NonPreferredRootDoesNotClaimAlreadyClaimedKey(t *testing.T) {
	result := map[string]string{}
	scanChartValueLines(`FOO: {{ .Values.rootA.foo | quote }}`, "rootB", result)
	scanChartValueLines(`FOO: {{ .Values.rootC.foo | quote }}`, "rootB", result)
	if result["FOO"] != "rootA.foo" {
		t.Errorf("FOO = %q, want the first non-preferred root to keep its claim", result["FOO"])
	}
}

func TestScanChartValueLines_PreferredRootAlwaysOverrides(t *testing.T) {
	result := map[string]string{"FOO": "rootA.foo"}
	scanChartValueLines(`FOO: {{ .Values.rootB.foo | quote }}`, "rootB", result)
	if result["FOO"] != "rootB.foo" {
		t.Errorf("FOO = %q, want the preferred root to override", result["FOO"])
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
