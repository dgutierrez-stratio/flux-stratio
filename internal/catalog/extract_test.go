package catalog

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestExtractSchema_Dependencies(t *testing.T) {
	block := `
    <<- $dependencies := get $componentConfig "dependencies" | default dict >>
    <<- $postgresDependency := get $dependencies "postgres" | default dict >>
    <<- $pgbouncerDependency := get $dependencies "pgbouncer" | default dict >>
    <<- $postgresDependency := get $dependencies "postgres" | default dict >>
`
	s := extractSchema("foo", block)
	want := []Dependency{{Key: "postgres"}, {Key: "pgbouncer"}}
	if !reflect.DeepEqual(s.Dependencies, want) {
		t.Errorf("Dependencies = %+v, want %+v (dedup, first-seen order)", s.Dependencies, want)
	}
}

func TestExtractSchema_StorageConditionalDependencies(t *testing.T) {
	block := `
    <<- $dependencies := get $componentConfig "dependencies" | default dict >>
    <<- $connectorsDependency := get $dependencies "connectors" | default dict >>
    <<- $hdfsDependency := get $dependencies "hdfs" | default dict >>
    <<- if eq $storageType "hdfs" >>
      connectors: << get $connectorsDependency "name" >>
      hdfs: << get $hdfsDependency "name" >>
    <<- end >>
`
	s := extractSchema("foo", block)
	byKey := map[string]Dependency{}
	for _, d := range s.Dependencies {
		byKey[d.Key] = d
	}
	if !reflect.DeepEqual(byKey["connectors"].StorageTypes, []string{"hdfs"}) {
		t.Errorf("connectors.StorageTypes = %v, want [hdfs]", byKey["connectors"].StorageTypes)
	}
	if !reflect.DeepEqual(byKey["hdfs"].StorageTypes, []string{"hdfs"}) {
		t.Errorf("hdfs.StorageTypes = %v, want [hdfs]", byKey["hdfs"].StorageTypes)
	}
}

func TestExtractSchema_ExtraConfigWithAndWithoutDefault(t *testing.T) {
	block := `
      SCHEDULE: << get $componentConfig "schedule" | default "0 0 * * *" | quote >>
      MODELS: << get $componentConfig "models" >>
      SIZE_IGNORED: << get $componentConfig "size" >>
`
	s := extractSchema("foo", block)
	byKey := map[string]ExtraConfigField{}
	for _, f := range s.ExtraConfig {
		byKey[f.Key] = f
	}
	if _, ok := byKey["size"]; ok {
		t.Error("\"size\" should be ignored, matching the tenant-level field it actually is")
	}
	if f := byKey["schedule"]; !f.HasDefault || f.Default != "0 0 * * *" {
		t.Errorf("schedule = %+v, want HasDefault=true Default=\"0 0 * * *\"", f)
	}
	if f := byKey["models"]; f.HasDefault {
		t.Errorf("models = %+v, want HasDefault=false", f)
	}
}

func TestExtractKustomizations_DefaultAnchorAndChartName(t *testing.T) {
	block := `
    ---
    apiVersion: kustomize.toolkit.fluxcd.io/v1
    kind: Kustomization
    metadata:
      name: apps-<< get $component "name" >>
    spec:
      path: components/rocket/app/overlays/<< $componentSize >>
      patches: << get $component "patches" | default list | toJson >>
`
	anchors, chart := extractKustomizations(block)
	if chart != "rocket" {
		t.Errorf("chart = %q, want %q", chart, "rocket")
	}
	if len(anchors) != 1 || anchors[0].Suffix != "" || anchors[0].Kind != AnchorDefault {
		t.Errorf("anchors = %+v, want one default anchor with empty suffix", anchors)
	}
}

func TestExtractKustomizations_NestedGosecAgent(t *testing.T) {
	block := `
    ---
    apiVersion: kustomize.toolkit.fluxcd.io/v1
    kind: Kustomization
    metadata:
      name: apps-<< get $component "name" >>
    spec:
      path: components/postgres/app/overlays/<< $componentSize >>
      patches: << get $component "patches" | default list | toJson >>
    <<- $agentConfig := get $componentConfig "agent" | default dict >>
    <<- $agentName := printf "%s-gosec-agent" (get $component "name") >>
    ---
    apiVersion: kustomize.toolkit.fluxcd.io/v1
    kind: Kustomization
    metadata:
      name: apps-<< $agentName >>
    spec:
      path: ./components/gosec-agent/app/overlays/postgres/<< $agentSize >>
      patches: << get $agentConfig "patches" | default list | toJson >>
`
	anchors, chart := extractKustomizations(block)
	if chart != "postgres" {
		t.Errorf("chart = %q, want %q", chart, "postgres")
	}
	if len(anchors) != 2 {
		t.Fatalf("len(anchors) = %d, want 2", len(anchors))
	}
	if anchors[0].Suffix != "" || anchors[0].Kind != AnchorDefault {
		t.Errorf("anchors[0] = %+v, want default with empty suffix", anchors[0])
	}
	if anchors[1].Suffix != "-gosec-agent" || anchors[1].Kind != AnchorNested || anchors[1].Field != "config.agent" {
		t.Errorf("anchors[1] = %+v, want nested \"-gosec-agent\" -> config.agent", anchors[1])
	}
}

func TestExtractKustomizations_NotAConfusedByAnUnrelatedComponentConfigAssignment(t *testing.T) {
	// $dependencies is also assigned from get $componentConfig, and comes
	// BEFORE $agentConfig in real templates — the classifier must match by
	// which variable name the patches: line actually references, not by
	// which assignment appears first.
	block := `
    <<- $dependencies := get $componentConfig "dependencies" | default dict >>
    <<- $agentConfig := get $componentConfig "agent" | default dict >>
    <<- $agentName := printf "%s-gosec-agent" (get $component "name") >>
    ---
    apiVersion: kustomize.toolkit.fluxcd.io/v1
    kind: Kustomization
    metadata:
      name: apps-<< $agentName >>
    spec:
      path: ./components/gosec-agent/app/overlays/opensearch/<< $agentSize >>
      patches: << get $agentConfig "patches" | default list | toJson >>
`
	anchors, _ := extractKustomizations(block)
	if len(anchors) != 1 || anchors[0].Kind != AnchorNested || anchors[0].Field != "config.agent" {
		t.Errorf("anchors = %+v, want a single nested anchor at config.agent", anchors)
	}
}

func TestExtractKustomizations_NotPatchable(t *testing.T) {
	block := `
    ---
    apiVersion: kustomize.toolkit.fluxcd.io/v1
    kind: Kustomization
    metadata:
      name: apps-<< get $component "name" >>-postrequisites
    spec:
      path: components/rocket/config/talk-to-your-data/postrequisites
      patches: []
`
	anchors, _ := extractKustomizations(block)
	if len(anchors) != 1 || anchors[0].Suffix != "-postrequisites" || anchors[0].Kind != AnchorNotPatchable {
		t.Errorf("anchors = %+v, want one not-patchable \"-postrequisites\" anchor", anchors)
	}
}

func TestExtractKustomizations_UnrecognizedPatchesShapeIsUnknownNotDefault(t *testing.T) {
	block := `
    ---
    apiVersion: kustomize.toolkit.fluxcd.io/v1
    kind: Kustomization
    metadata:
      name: apps-<< get $component "name" >>-something-new
    spec:
      patches: << get $somethingElse "patches" | default list | toJson >>
`
	anchors, _ := extractKustomizations(block)
	if len(anchors) != 1 || anchors[0].Kind != AnchorUnknown {
		t.Errorf("anchors = %+v, want AnchorUnknown, never silently AnchorDefault", anchors)
	}
}

func TestExtractKustomizations_DependsOnCrossReferenceIgnored(t *testing.T) {
	// A dependsOn entry referencing another component's own gosec agent
	// by name must not be treated as this component's own Kustomization.
	block := `
    <<- $postgresAgentName := printf "%s-gosec-agent" (get $postgresDependency "name") >>
    ---
    apiVersion: kustomize.toolkit.fluxcd.io/v1
    kind: Kustomization
    metadata:
      name: apps-<< get $component "name" >>
    spec:
      dependsOn:
        - name: apps-<< $postgresAgentName >>
      patches: << get $component "patches" | default list | toJson >>
`
	anchors, _ := extractKustomizations(block)
	if len(anchors) != 1 || anchors[0].Suffix != "" {
		t.Errorf("anchors = %+v, want only the component's own default anchor, the dependsOn reference ignored", anchors)
	}
}

func TestResolveSuffix(t *testing.T) {
	block := `<<- $agentName := printf "%s-gosec-agent" (get $component "name") >>`
	cases := []struct {
		expr     string
		wantOK   bool
		wantSuff string
	}{
		{`<< get $component "name" >>`, true, ""},
		{`<< get $component "name" >>-postrequisites`, true, "-postrequisites"},
		{`<< $agentName >>`, true, "-gosec-agent"},
		{`<< $unrelatedVar >>`, false, ""},
		{`<< get $postgresDependency "name" >>`, false, ""},
	}
	for _, c := range cases {
		suffix, ok := resolveSuffix(c.expr, block)
		if ok != c.wantOK || (ok && suffix != c.wantSuff) {
			t.Errorf("resolveSuffix(%q) = (%q, %v), want (%q, %v)", c.expr, suffix, ok, c.wantSuff, c.wantOK)
		}
	}
}

func TestExtractCRDs(t *testing.T) {
	block := `
      healthCheckExprs:
        - apiVersion: postgres.stratio.com/v1
          kind: PgCluster
          failed: status.phase == 'Failed'
`
	got := extractCRDs(block, "postgres")
	want := map[string]CRDInfo{
		"pgclusters.postgres.stratio.com": {
			GVK:          schema.GroupVersionKind{Group: "postgres.stratio.com", Version: "v1", Kind: "PgCluster"},
			ComponentKey: "postgres",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractCRDs = %+v, want %+v", got, want)
	}
}

func TestDeriveCRDPlural(t *testing.T) {
	cases := []struct{ kind, group, want string }{
		{"pgcluster", "postgres.stratio.com", "pgclusters.postgres.stratio.com"},
		{"osdashboards", "opensearch.stratio.com", "osdashboardses.opensearch.stratio.com"},
		{"pgbackuprepository", "postgres.stratio.com", "pgbackuprepositories.postgres.stratio.com"},
		{"hdfscluster", "hdfs.stratio.com", "hdfsclusters.hdfs.stratio.com"},
	}
	for _, c := range cases {
		if got := deriveCRDPlural(c.kind, c.group); got != c.want {
			t.Errorf("deriveCRDPlural(%q, %q) = %q, want %q", c.kind, c.group, got, c.want)
		}
	}
}

func TestExtractSchema_ExtraConfigHasKeyElseLiteralIsADefault(t *testing.T) {
	tests := []struct {
		name        string
		block       string
		key         string
		wantDefault string
		wantBare    bool
	}{
		{
			name:        "virtualizer uiEnabled, the real template line",
			block:       `          STRATIO_VIRTUALIZER_UI_ENABLED: '<< if hasKey $componentConfig "uiEnabled" >><< get $componentConfig "uiEnabled" >><< else >>false<< end >>'`,
			key:         "uiEnabled",
			wantDefault: "false",
			wantBare:    true,
		},
		{
			name:        "quoted literal stays a string",
			block:       `X: << if hasKey $componentConfig "mode" >><< get $componentConfig "mode" >><< else >>"fast"<< end >>`,
			key:         "mode",
			wantDefault: "fast",
		},
		{
			name:        "number literal",
			block:       `X: << if hasKey $componentConfig "replicas" >><< get $componentConfig "replicas" >><< else >>3<< end >>`,
			key:         "replicas",
			wantDefault: "3",
			wantBare:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := extractSchema("foo", tt.block)
			if len(s.ExtraConfig) != 1 {
				t.Fatalf("ExtraConfig = %+v, want exactly one field", s.ExtraConfig)
			}
			f := s.ExtraConfig[0]
			if f.Key != tt.key || !f.HasDefault || f.Default != tt.wantDefault || f.Bare != tt.wantBare {
				t.Errorf("field = %+v, want key=%q default=%q bare=%v", f, tt.key, tt.wantDefault, tt.wantBare)
			}
		})
	}
}

func TestExtractSchema_HasKeyWithoutElseKeepsNoDefault(t *testing.T) {
	block := `A: << if hasKey $componentConfig "a" >><< get $componentConfig "a" >><< end >>`
	s := extractSchema("foo", block)
	if len(s.ExtraConfig) != 1 || s.ExtraConfig[0].HasDefault {
		t.Errorf("ExtraConfig = %+v, want one field without a default", s.ExtraConfig)
	}
}

func TestExtractSchema_ExtraConfigBareDefaultsAndContainers(t *testing.T) {
	block := `
      A: << get $componentConfig "recoveryEnabled" | default false >>
      B: "<< get $componentConfig "governanceOpenSearchEnabled" | default true >>"
      C: << get $componentConfig "replicas" | default 3 | quote >>
      D: << get $componentConfig "ratio" | default 0.5 >>
      E: << $agentConfig := get $componentConfig "agent" | default dict >>
      F: << get $componentConfig "indices" | default list | toJson | quote >>
      G: << get $componentConfig "quoted" | default "x y" | quote >>
      H: << get $componentConfig "dynamic" | default $something >>
      I: << get $componentConfig "plain" | quote >>
`
	byKey := map[string]ExtraConfigField{}
	for _, f := range extractSchema("foo", block).ExtraConfig {
		byKey[f.Key] = f
	}
	for key, want := range map[string]ExtraConfigField{
		"recoveryEnabled":             {Key: "recoveryEnabled", HasDefault: true, Default: "false", Bare: true},
		"governanceOpenSearchEnabled": {Key: "governanceOpenSearchEnabled", HasDefault: true, Default: "true", Bare: true},
		"replicas":                    {Key: "replicas", HasDefault: true, Default: "3", Bare: true},
		"ratio":                       {Key: "ratio", HasDefault: true, Default: "0.5", Bare: true},
		"agent":                       {Key: "agent", Container: "dict"},
		"indices":                     {Key: "indices", Container: "list"},
		"quoted":                      {Key: "quoted", HasDefault: true, Default: "x y"},
		"dynamic":                     {Key: "dynamic"},
		"plain":                       {Key: "plain"},
	} {
		got := byKey[key]
		if got.Key != want.Key || got.HasDefault != want.HasDefault || got.Default != want.Default || got.Bare != want.Bare || got.Container != want.Container {
			t.Errorf("%s = %+v, want %+v", key, got, want)
		}
	}
}

func TestExtractSchema_ExtraConfigStorageTypeBranches(t *testing.T) {
	block := `
      <<- if eq $storageType "local" >>
      CLASS: << get $componentConfig "storageClassName" | quote >>
      MODE: << get $componentConfig "accessMode" | default "ReadWriteOnce" | quote >>
      <<- else >>
      OTHER: << get $componentConfig "elseOnly" | quote >>
      <<- end >>
      <<- if eq $storageType "hdfs" >>
      A: << get $componentConfig "both" | quote >>
      <<- end >>
      <<- if eq $storageType "local" >>
      B: << get $componentConfig "both" | quote >>
      C: << get $componentConfig "storageClassName" | quote >>
      <<- end >>
      OUTSIDE: << get $componentConfig "accessMode" | default "ReadWriteOnce" | quote >>
`
	byKey := map[string]ExtraConfigField{}
	for _, f := range extractSchema("foo", block).ExtraConfig {
		byKey[f.Key] = f
	}
	if f := byKey["storageClassName"]; len(f.StorageTypes) != 1 || f.StorageTypes[0] != "local" || f.AppliesTo("s3") || !f.AppliesTo("local") {
		t.Errorf("storageClassName = %+v, want local only", f)
	}
	if f := byKey["both"]; len(f.StorageTypes) != 2 || f.StorageTypes[0] != "hdfs" || f.StorageTypes[1] != "local" || f.AppliesTo("s3") {
		t.Errorf("both = %+v, want hdfs and local", f)
	}
	if f := byKey["accessMode"]; len(f.StorageTypes) != 0 || !f.AppliesTo("s3") {
		t.Errorf("accessMode = %+v, want unconditional (also read outside the branch)", f)
	}
	if f := byKey["elseOnly"]; len(f.StorageTypes) != 0 {
		t.Errorf("elseOnly = %+v, want unconditional (read in the else of the branch)", f)
	}
}

func TestExtractSchema_NestedConfigLeaves(t *testing.T) {
	block := `
    <<- $dependencies := get $componentConfig "dependencies" | default dict >>
    <<- $storageType := get $componentConfig "type" | default "s3" >>
    <<- $llmModels := get $componentConfig "llmModels" | default dict >>
    <<- $backupConfig := get $componentConfig "backup" | default dict >>
    <<- $backupEnabled := get $backupConfig "enabled" | default false >>
    <<- $backupType := get $backupConfig "type" | default "s3" >>
          CLASS: << get $backupConfig "storageClassName" | quote >>
          LLM_MODEL_CHAT: "<< get $llmModels "chat" | default "" >>"
          LLM_MODEL_GOVERNANCE: "<< get $llmModels "governance" | default "" >>"
          LLM_MODEL_TRANSLATE: "<< get $llmModels "translate" | default "" >>"
          AGAIN: "<< get $llmModels "chat" | default "" >>"
          OTHER: << get $other "x" >>
          INDICES: << get $componentConfig "indices" | default list | toJson >>
`
	byKey := map[string]ExtraConfigField{}
	for _, f := range extractSchema("foo", block).ExtraConfig {
		byKey[f.Key] = f
	}
	for key, want := range map[string][]ExtraConfigField{
		"llmModels": {
			{Key: "chat", HasDefault: true},
			{Key: "governance", HasDefault: true},
			{Key: "translate", HasDefault: true},
		},
		"backup": {
			{Key: "enabled", HasDefault: true, Default: "false", Bare: true},
			{Key: "type", HasDefault: true, Default: "s3"},
			{Key: "storageClassName"},
		},
		"indices": nil, // a list: no leaves
	} {
		if got := byKey[key].Leaves; !reflect.DeepEqual(got, want) {
			t.Errorf("%s.Leaves = %+v, want %+v", key, got, want)
		}
	}
	if _, ok := byKey["x"]; ok {
		t.Error(`a read off a variable not assigned from $componentConfig is not a field`)
	}
}

func TestExtractSchema_GatedDependencies(t *testing.T) {
	block := `
    <<- $dependencies := get $componentConfig "dependencies" | default dict >>
    <<- $postgresDependency := get $dependencies "postgres" | default dict >>
    <<- $opensearchDependency := get $dependencies "opensearch" | default dict >>
    <<- $discoveryDependency := get $dependencies "discovery" | default dict >>
    <<- $virtualizerDependency := get $dependencies "virtualizer" | default dict >>
      dependsOn:
        - name: apps-<< get $postgresDependency "name" >>
        <<- if get $opensearchDependency "enabled" | default false >>
        - name: apps-<< get $opensearchDependency "name" >>
        <<- if $opensearchAgentEnabled >>
        - name: apps-<< $opensearchAgentName >>
        <<- end >>
        <<- end >>
        - name: apps-<< get $virtualizerDependency "name" >>
    <<- if get $discoveryDependency "enabled" | default false >>
    ---
    kind: Kustomization
    metadata:
      name: apps-<< get $component "name" >>-postrequisites
    <<- end >>
      substitute:
        DISCOVERY_ENABLED: "<< get $discoveryDependency "enabled" | default false >>"
        DISCOVERY_NAME: << get $discoveryDependency "name" | default "" >>
`
	s := extractSchema("foo", block)
	byKey := map[string]Dependency{}
	for _, d := range s.Dependencies {
		byKey[d.Key] = d
	}

	for _, key := range []string{"postgres", "virtualizer"} {
		if d := byKey[key]; d.Gated || len(d.Adds) != 0 {
			t.Errorf("%s: Gated=%v Adds=%v, want an ungated dependency (its `name` is read, never its `enabled`)", key, d.Gated, d.Adds)
		}
	}

	os := byKey["opensearch"]
	if !os.Gated {
		t.Fatalf("opensearch: Gated = false, want true")
	}
	// The nested `if $opensearchAgentEnabled ... end` must not close the block early.
	if want := []string{"makes the app wait for the dependency (dependsOn)"}; !reflect.DeepEqual(os.Adds, want) {
		t.Errorf("opensearch.Adds = %q, want %q", os.Adds, want)
	}

	disc := byKey["discovery"]
	if !disc.Gated {
		t.Fatalf("discovery: Gated = false, want true")
	}
	want := []string{
		"sets DISCOVERY_ENABLED in the app's chart values",
		"creates the Kustomization apps-*-postrequisites",
	}
	if !reflect.DeepEqual(disc.Adds, want) {
		t.Errorf("discovery.Adds = %q, want %q", disc.Adds, want)
	}
}

func TestExtractSchema_ReadOfEnabledOnAnUnboundVariableIsIgnored(t *testing.T) {
	// A variable no `get $dependencies` bound is not a dependency of the component.
	block := `
    <<- $dependencies := get $componentConfig "dependencies" | default dict >>
    <<- $postgresDependency := get $dependencies "postgres" | default dict >>
    <<- if get $strangerDependency "enabled" | default false >>
    <<- end >>
`
	s := extractSchema("foo", block)
	if len(s.Dependencies) != 1 || s.Dependencies[0].Gated {
		t.Errorf("Dependencies = %+v, want only an ungated postgres", s.Dependencies)
	}
}
