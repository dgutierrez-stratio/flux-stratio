package tenantimport

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRender_CorrectLabel(t *testing.T) {
	// The Python client emitted flux.stratio.com/tenant: "true" here,
	// making every tenant it generated invisible to every ResourceSet —
	// this pins the fix (design decision 8 in the project plan).
	out, err := Render("stratio", "S", Components{})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "keos.stratio.com/resourceset-type: tenant-config") {
		t.Errorf("output missing the correct label; got:\n%s", s)
	}
	if strings.Contains(s, "flux.stratio.com/tenant") {
		t.Errorf("output contains the old, wrong label; got:\n%s", s)
	}
}

func TestRender_UsesTwoSpaceIndent(t *testing.T) {
	// gopkg.in/yaml.v3's default Marshal indents 4 spaces; this plugin's
	// convention (internal/tenantfile.Doc.Bytes) is 2, everywhere it
	// writes YAML.
	components := Components{"postgres": {{Name: "psql"}}}
	out, err := Render("stratio", "S", components)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "\n  name: stratio") {
		t.Errorf("output not indented 2 spaces under metadata; got:\n%s", s)
	}
	if strings.Contains(s, "\n    name:") || strings.Contains(s, "\n    namespace:") {
		t.Errorf("output is 4-space indented, want 2; got:\n%s", s)
	}
}

func TestRender_ValidYAMLStructure(t *testing.T) {
	components := Components{
		"postgres": {{
			Name: "psql",
			Deps: map[string]string{"pgbackuprepository": "pgbackuprepository"},
		}},
	}
	out, err := Render("stratio", "S", components)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("Render produced invalid YAML: %v\n%s", err, out)
	}
	if doc["apiVersion"] != "fluxcd.controlplane.io/v1" || doc["kind"] != "ResourceSetInputProvider" {
		t.Errorf("apiVersion/kind = %v/%v", doc["apiVersion"], doc["kind"])
	}

	meta := doc["metadata"].(map[string]any)
	if meta["name"] != "stratio" || meta["namespace"] != "flux-system" {
		t.Errorf("metadata = %+v", meta)
	}

	spec := doc["spec"].(map[string]any)
	if spec["type"] != "Static" {
		t.Errorf("spec.type = %v, want Static", spec["type"])
	}
	dv := spec["defaultValues"].(map[string]any)
	if dv["tenantName"] != "stratio" || dv["size"] != "S" {
		t.Errorf("defaultValues = %+v", dv)
	}

	comps := dv["components"].(map[string]any)
	pgList := comps["postgres"].([]any)
	if len(pgList) != 1 {
		t.Fatalf("postgres list = %v, want 1 entry", pgList)
	}
	entry := pgList[0].(map[string]any)
	if entry["name"] != "psql" {
		t.Errorf("entry.name = %v, want psql", entry["name"])
	}
	config := entry["config"].(map[string]any)
	deps := config["dependencies"].(map[string]any)
	pgbackuprepo := deps["pgbackuprepository"].(map[string]any)
	if pgbackuprepo["name"] != "pgbackuprepository" {
		t.Errorf("deps.pgbackuprepository.name = %v", pgbackuprepo["name"])
	}
}

func TestRender_NoConfigBlockWhenEntryIsBare(t *testing.T) {
	components := Components{"connectors": {{Name: "connectors"}}}
	out, err := Render("stratio", "S", components)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	dv := doc["spec"].(map[string]any)["defaultValues"].(map[string]any)
	comps := dv["components"].(map[string]any)
	entries := comps["connectors"].([]any)
	entry := entries[0].(map[string]any)
	if _, hasConfig := entry["config"]; hasConfig {
		t.Errorf("entry = %+v, want no config block for a bare entry", entry)
	}
}

func TestRender_TypeIncludedWhenSet(t *testing.T) {
	components := Components{"virtualizer": {{Name: "virtualizer", Type: "hdfs"}}}
	out, err := Render("stratio", "S", components)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "type: hdfs") {
		t.Errorf("output missing type: hdfs; got:\n%s", out)
	}
}
