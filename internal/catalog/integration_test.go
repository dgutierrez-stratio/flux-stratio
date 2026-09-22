package catalog

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// realKeosUseCasesDir locates a sibling keos-use-cases checkout, the same
// layout convention documented for the whole flux-stratio module (a
// "--base" directory holding keos-apps, keos-use-cases, keos-fleet and
// keos-system-services side by side with this repo). Unlike the Python
// client's equivalent test — which resolved a path that never existed in
// its own checkout and so was always skipped — this one runs whenever the
// sibling repo is actually present, which is the common case for anyone
// working in this monorepo-of-repos layout.
func realKeosUseCasesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "keos-use-cases"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "apps", "components")); err != nil {
		t.Skipf("sibling keos-use-cases checkout not found at %s: %v", dir, err)
	}
	return dir
}

// TestLoad_RealTemplates pins the facts this session verified by hand
// against the actual keos-use-cases templates (via direct grep and a
// throwaway diagnostic dump, both discarded once these assertions were
// written) — the regression coverage the Python client's own always-skipped
// integration test never actually provided.
func TestLoad_RealTemplates(t *testing.T) {
	dir := realKeosUseCasesDir(t)
	cat, err := Load(dir)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cat.Schemas) != 25 {
		t.Errorf("len(Schemas) = %d, want 25", len(cat.Schemas))
	}

	// The two known gosec-agent sub-resources.
	for _, key := range []string{"postgres", "opensearch"} {
		s, ok := cat.Schemas[key]
		if !ok {
			t.Fatalf("Schemas[%q] missing", key)
		}
		found := false
		for _, k := range s.Kustomizations {
			if k.Suffix == "-gosec-agent" {
				found = true
				if k.Kind != AnchorNested || k.Field != "config.agent" {
					t.Errorf("%s's -gosec-agent anchor = %+v, want AnchorNested at config.agent", key, k)
				}
			}
		}
		if !found {
			t.Errorf("%s: no -gosec-agent Kustomization found", key)
		}
	}

	// The known -postrequisites (not patchable) components.
	for _, key := range []string{"rocket", "litellm", "genai", "discovery", "datamarketAgent"} {
		s, ok := cat.Schemas[key]
		if !ok {
			t.Fatalf("Schemas[%q] missing", key)
		}
		found := false
		for _, k := range s.Kustomizations {
			if k.Suffix == "-postrequisites" {
				found = true
				if k.Kind != AnchorNotPatchable {
					t.Errorf("%s's -postrequisites anchor = %+v, want AnchorNotPatchable", key, k)
				}
			}
		}
		if !found {
			t.Errorf("%s: no -postrequisites Kustomization found", key)
		}
	}

	// pgbackup is genuinely ambiguous between pgbackupLogical/pgbackupPhysical.
	pgbackup, ok := cat.Charts["pgbackup"]
	if !ok {
		t.Fatal("Charts[\"pgbackup\"] missing")
	}
	if pgbackup.Key != "" || len(pgbackup.Ambiguous) != 2 {
		t.Errorf("Charts[\"pgbackup\"] = %+v, want ambiguous between exactly 2 component keys", pgbackup)
	}

	// A representative sample of unambiguous chart mappings.
	wantCharts := map[string]string{
		"bdl-datarest":  "bdlDatarest",
		"dlc-entity":    "dlcEntity",
		"spark-history": "sparkHistory",
	}
	for chart, want := range wantCharts {
		if got := cat.Charts[chart].Key; got != want {
			t.Errorf("Charts[%q].Key = %q, want %q", chart, got, want)
		}
	}

	// genai's declared dependencies, in order.
	genai, ok := cat.Schemas["genai"]
	if !ok {
		t.Fatal("Schemas[\"genai\"] missing")
	}
	var genaiDeps []string
	for _, d := range genai.Dependencies {
		genaiDeps = append(genaiDeps, d.Key)
	}
	wantGenaiDeps := []string{"postgres", "pgbouncer", "opensearch", "virtualizer", "discovery", "litellm"}
	if len(genaiDeps) != len(wantGenaiDeps) {
		t.Fatalf("genai deps = %v, want %v", genaiDeps, wantGenaiDeps)
	}
	for i, d := range wantGenaiDeps {
		if genaiDeps[i] != d {
			t.Errorf("genai deps[%d] = %q, want %q (order matters: %v)", i, genaiDeps[i], d, genaiDeps)
		}
	}

	// Storage-conditional dependencies: virtualizer's hdfs/connectors are
	// only referenced inside an `if eq $storageType "hdfs"` block.
	virtualizer, ok := cat.Schemas["virtualizer"]
	if !ok {
		t.Fatal("Schemas[\"virtualizer\"] missing")
	}
	for _, dep := range virtualizer.Dependencies {
		if dep.Key == "hdfs" || dep.Key == "connectors" {
			if len(dep.StorageTypes) != 1 || dep.StorageTypes[0] != "hdfs" {
				t.Errorf("virtualizer dep %q StorageTypes = %v, want [hdfs]", dep.Key, dep.StorageTypes)
			}
		}
		if dep.Key == "postgres" || dep.Key == "pgbouncer" {
			if len(dep.StorageTypes) != 0 {
				t.Errorf("virtualizer dep %q StorageTypes = %v, want unconditional (empty)", dep.Key, dep.StorageTypes)
			}
		}
	}

	// The 8 known CRD mappings, plus a spot-check of one full GVK.
	wantCRDs := map[string]string{
		"pgclusters.postgres.stratio.com":           "postgres",
		"pgbouncers.postgres.stratio.com":           "pgbouncer",
		"pgbackuprepositories.postgres.stratio.com": "pgbackuprepository",
		"osclusters.opensearch.stratio.com":         "opensearch",
		"osbackups.opensearch.stratio.com":          "osbackup",
		"osdashboardses.opensearch.stratio.com":     "opendashboards",
		"hdfsclusters.hdfs.stratio.com":             "hdfs",
		"kafkaclusters.kafka.stratio.com":           "kafka",
	}
	if len(cat.CRDs) != len(wantCRDs) {
		t.Errorf("len(CRDs) = %d, want %d (%v)", len(cat.CRDs), len(wantCRDs), cat.CRDs)
	}
	for plural, want := range wantCRDs {
		if got := cat.CRDs[plural]; got.ComponentKey != want {
			t.Errorf("CRDs[%q].ComponentKey = %q, want %q", plural, got.ComponentKey, want)
		}
	}
	wantGVK := schema.GroupVersionKind{Group: "postgres.stratio.com", Version: "v1", Kind: "PgCluster"}
	if got := cat.CRDs["pgclusters.postgres.stratio.com"].GVK; got != wantGVK {
		t.Errorf("pgclusters GVK = %+v, want %+v", got, wantGVK)
	}
}

// TestResolveAnchor_RealGosecAgentNames exercises ResolveAnchor with real
// app-shaped Kustomization names, the way internal/appmigrate will call it
// from Phase 6 onward.
func TestResolveAnchor_RealGosecAgentNames(t *testing.T) {
	dir := realKeosUseCasesDir(t)
	cat, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		wantKind AnchorKind
	}{
		{"apps-psql", AnchorDefault},
		{"apps-psql-gosec-agent", AnchorNested},
		{"apps-opensearch1", AnchorDefault},
		{"apps-opensearch1-gosec-agent", AnchorNested},
		{"apps-genai", AnchorDefault},
		{"apps-genai-postrequisites", AnchorNotPatchable},
		{"apps-governance-datamarket-agent-postrequisites", AnchorNotPatchable},
	}
	for _, c := range cases {
		anchor, err := cat.ResolveAnchor(c.name)
		if err != nil {
			t.Errorf("ResolveAnchor(%q) returned error: %v", c.name, err)
			continue
		}
		if anchor.Kind != c.wantKind {
			t.Errorf("ResolveAnchor(%q).Kind = %v, want %v", c.name, anchor.Kind, c.wantKind)
		}
	}
}
