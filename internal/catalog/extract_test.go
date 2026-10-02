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
