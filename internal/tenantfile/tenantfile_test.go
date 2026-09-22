package tenantfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
)

func loadFixture(t *testing.T) *Doc {
	t.Helper()
	d, err := Load("testdata/tenant.yaml")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	return d
}

func loadFixtureCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load("testdata/catalog")
	if err != nil {
		t.Fatalf("catalog.Load returned error: %v", err)
	}
	return cat
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load("testdata/does-not-exist.yaml"); err == nil {
		t.Fatal("Load on a missing file: got nil error, want non-nil")
	}
}

func TestSave_RoundTripPreservesCommentsAndCommentedOutBlocks(t *testing.T) {
	d := loadFixture(t)
	out := filepath.Join(t.TempDir(), "tenant.yaml")
	if err := d.Save(out); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"# This is the tenant's ResourceSetInputProvider",
		"# internal bucket used for backups",
		"# gosec agent for psql is configured below",
		"# rocket is not yet migrated",
		"# rocket:",
		"# - name: rocket",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("round-tripped file missing %q; got:\n%s", want, s)
		}
	}
}

func TestSave_AtomicWriteLeavesNoTempFile(t *testing.T) {
	d := loadFixture(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "tenant.yaml")
	if err := d.Save(out); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "tenant.yaml" {
		t.Errorf("dir entries = %v, want exactly [tenant.yaml] (no leftover temp file)", entries)
	}
}

func TestFindComponentEntry_ScansEveryComponentKey(t *testing.T) {
	d := loadFixture(t)

	psql, err := FindComponentEntry(d, "psql")
	if err != nil {
		t.Fatalf("FindComponentEntry(psql) returned error: %v", err)
	}
	if name := mapGet(psql, "name"); name == nil || name.Value != "psql" {
		t.Errorf("psql entry name = %v", name)
	}

	// opensearch1 lives under a different top-level component key
	// (opensearch), several keys after postgres — confirms every key is
	// searched, not just the first.
	os1, err := FindComponentEntry(d, "opensearch1")
	if err != nil {
		t.Fatalf("FindComponentEntry(opensearch1) returned error: %v", err)
	}
	if name := mapGet(os1, "name"); name == nil || name.Value != "opensearch1" {
		t.Errorf("opensearch1 entry name = %v", name)
	}
}

func TestFindComponentEntry_NotFound(t *testing.T) {
	d := loadFixture(t)
	if _, err := FindComponentEntry(d, "does-not-exist"); err == nil {
		t.Fatal("FindComponentEntry for a nonexistent name: got nil error, want non-nil")
	}
}

func TestFindComponentEntry_CommentedOutEntryIsInvisible(t *testing.T) {
	// "rocket" is commented out in the fixture — it must not be found,
	// proving the parser sees real YAML structure, not raw text.
	d := loadFixture(t)
	if _, err := FindComponentEntry(d, "rocket"); err == nil {
		t.Fatal("FindComponentEntry(rocket) found a commented-out entry: got nil error, want non-nil")
	}
}

func TestResolveAnchorNode_Default(t *testing.T) {
	d := loadFixture(t)
	entry, err := FindComponentEntry(d, "psql")
	if err != nil {
		t.Fatal(err)
	}
	node, err := ResolveAnchorNode(entry, catalog.ResolvedAnchor{Kind: catalog.AnchorDefault})
	if err != nil {
		t.Fatal(err)
	}
	if node != entry {
		t.Error("ResolveAnchorNode with AnchorDefault should return the entry itself")
	}
}

func TestResolveAnchorNode_NestedCreatesIntermediateMapsIfAbsent(t *testing.T) {
	d := loadFixture(t)
	entry, err := FindComponentEntry(d, "opensearch1") // has no "config" block at all in the fixture
	if err != nil {
		t.Fatal(err)
	}
	node, err := ResolveAnchorNode(entry, catalog.ResolvedAnchor{Kind: catalog.AnchorNested, Field: "config.agent"})
	if err != nil {
		t.Fatalf("ResolveAnchorNode returned error: %v", err)
	}
	if node.Kind != yaml.MappingNode {
		t.Errorf("node.Kind = %v, want a mapping node", node.Kind)
	}
	// Confirm it was actually wired into the entry, not just returned standalone.
	config := mapGet(entry, "config")
	if config == nil {
		t.Fatal("entry.config was not created")
	}
	if mapGet(config, "agent") != node {
		t.Error("entry.config.agent does not point at the returned node")
	}
}

func TestResolveAnchorNode_NotPatchableErrors(t *testing.T) {
	d := loadFixture(t)
	entry, err := FindComponentEntry(d, "psql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveAnchorNode(entry, catalog.ResolvedAnchor{Kind: catalog.AnchorNotPatchable}); err == nil {
		t.Fatal("ResolveAnchorNode with AnchorNotPatchable: got nil error, want non-nil")
	}
}

func samplePatch(kind string) diff.PatchDoc {
	return diff.PatchDoc{TargetKind: kind, Patch: map[string]any{
		"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"name": "x"},
		"spec": map[string]any{"foo": "bar"},
	}}
}

func TestUpsertPatch_AddsNewPatch(t *testing.T) {
	d := loadFixture(t)
	entry, err := FindComponentEntry(d, "psql")
	if err != nil {
		t.Fatal(err)
	}
	if err := UpsertPatch(entry, samplePatch("PgCluster")); err != nil {
		t.Fatalf("UpsertPatch returned error: %v", err)
	}
	patches := mapGet(entry, "patches")
	if patches == nil || len(patches.Content) != 1 {
		t.Fatalf("patches = %v, want exactly 1 entry", patches)
	}
	if patchTargetKind(patches.Content[0]) != "PgCluster" {
		t.Errorf("target.kind = %q, want PgCluster", patchTargetKind(patches.Content[0]))
	}
}

func TestUpsertPatch_IdempotentSameKind(t *testing.T) {
	d := loadFixture(t)
	entry, err := FindComponentEntry(d, "psql")
	if err != nil {
		t.Fatal(err)
	}
	if err := UpsertPatch(entry, samplePatch("PgCluster")); err != nil {
		t.Fatal(err)
	}
	if err := UpsertPatch(entry, samplePatch("PgCluster")); err != nil {
		t.Fatal(err)
	}
	patches := mapGet(entry, "patches")
	if len(patches.Content) != 1 {
		t.Errorf("len(patches) = %d, want 1 after applying the same kind twice", len(patches.Content))
	}
}

func TestUpsertPatch_DifferentKindsCoexist(t *testing.T) {
	d := loadFixture(t)
	entry, err := FindComponentEntry(d, "psql")
	if err != nil {
		t.Fatal(err)
	}
	if err := UpsertPatch(entry, samplePatch("PgCluster")); err != nil {
		t.Fatal(err)
	}
	if err := UpsertPatch(entry, samplePatch("HelmRelease")); err != nil {
		t.Fatal(err)
	}
	patches := mapGet(entry, "patches")
	if len(patches.Content) != 2 {
		t.Fatalf("len(patches) = %d, want 2 (different kinds must coexist)", len(patches.Content))
	}
}

func TestSplice_DefaultAnchor(t *testing.T) {
	d := loadFixture(t)
	cat := loadFixtureCatalog(t)
	app := config.App{ID: "psql", Kustomization: "apps-psql", Object: "psql"}

	if err := Splice(d, cat, app, samplePatch("PgCluster")); err != nil {
		t.Fatalf("Splice returned error: %v", err)
	}

	entry, err := FindComponentEntry(d, "psql")
	if err != nil {
		t.Fatal(err)
	}
	patches := mapGet(entry, "patches")
	if patches == nil || len(patches.Content) != 1 {
		t.Fatalf("psql.patches = %v, want exactly 1 entry", patches)
	}
}

func TestSplice_NestedAnchor_LandsOnParentEntryNotObjectName(t *testing.T) {
	d := loadFixture(t)
	cat := loadFixtureCatalog(t)
	// Object is the gosec agent's own HelmRelease name — there is no
	// "psql-gosec-agent" component entry in the tenant file at all; the
	// patch must land at the PARENT postgres entry's config.agent.
	app := config.App{ID: "psql-gosec-agent", Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent"}

	if err := Splice(d, cat, app, samplePatch("HelmRelease")); err != nil {
		t.Fatalf("Splice returned error: %v", err)
	}

	parent, err := FindComponentEntry(d, "psql")
	if err != nil {
		t.Fatal(err)
	}
	agentConfig := mapGet(mapGet(parent, "config"), "agent")
	if agentConfig == nil {
		t.Fatal("psql.config.agent was not created")
	}
	patches := mapGet(agentConfig, "patches")
	if patches == nil || len(patches.Content) != 1 {
		t.Fatalf("psql.config.agent.patches = %v, want exactly 1 entry", patches)
	}

	// And it must NOT have created a bogus top-level "psql-gosec-agent" entry.
	if _, err := FindComponentEntry(d, "psql-gosec-agent"); err == nil {
		t.Error("a top-level entry named after the Object should not exist for a nested-anchor app")
	}
}

func TestSplice_AnchorOverrideMismatchErrors(t *testing.T) {
	d := loadFixture(t)
	cat := loadFixtureCatalog(t)
	app := config.App{ID: "psql", Kustomization: "apps-psql", Object: "psql", Anchor: "config.somethingElse"}

	if err := Splice(d, cat, app, samplePatch("PgCluster")); err == nil {
		t.Fatal("Splice with a mismatched Anchor override: got nil error, want non-nil")
	}
}

func TestSplice_AnchorOverrideMatchingSucceeds(t *testing.T) {
	d := loadFixture(t)
	cat := loadFixtureCatalog(t)
	app := config.App{ID: "psql-gosec-agent", Kustomization: "apps-psql-gosec-agent", Object: "psql-gosec-agent", Anchor: "config.agent"}

	if err := Splice(d, cat, app, samplePatch("HelmRelease")); err != nil {
		t.Fatalf("Splice with a correct Anchor override returned error: %v", err)
	}
}

func TestSplice_NotPatchableKustomizationErrors(t *testing.T) {
	d := loadFixture(t)
	cat := loadFixtureCatalog(t)
	app := config.App{ID: "rocket-postreq", Kustomization: "apps-rocket-postrequisites", Object: "rocket"}

	if err := Splice(d, cat, app, samplePatch("Kustomization")); err == nil {
		t.Fatal("Splice targeting a not-patchable Kustomization: got nil error, want non-nil")
	}
}

func TestOwnerName(t *testing.T) {
	cases := []struct {
		kustomization string
		suffix        string
		want          string
	}{
		{"apps-psql", "", "psql"},
		{"apps-psql-gosec-agent", "-gosec-agent", "psql"},
		{"apps-governance-datamarket-agent", "", "governance-datamarket-agent"},
	}
	for _, c := range cases {
		got := OwnerName(c.kustomization, catalog.ResolvedAnchor{Suffix: c.suffix})
		if got != c.want {
			t.Errorf("OwnerName(%q, suffix=%q) = %q, want %q", c.kustomization, c.suffix, got, c.want)
		}
	}
}

func TestDependencyNames(t *testing.T) {
	d := loadFixture(t)
	entry, err := FindComponentEntry(d, "pool-psql")
	if err != nil {
		t.Fatal(err)
	}
	got := DependencyNames(entry)
	if got["postgres"] != "psql" {
		t.Errorf("DependencyNames = %v, want postgres=psql", got)
	}
}

func TestDependencyNames_NoDependenciesReturnsNil(t *testing.T) {
	d := loadFixture(t)
	entry, err := FindComponentEntry(d, "connectors")
	if err != nil {
		t.Fatal(err)
	}
	if got := DependencyNames(entry); got != nil {
		t.Errorf("DependencyNames = %v, want nil", got)
	}
}
