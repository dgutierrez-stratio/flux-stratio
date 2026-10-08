package tenantfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func docFrom(t *testing.T, text string) *Doc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tenant.yaml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func entryNamed(t *testing.T, d *Doc, name string) *yaml.Node {
	t.Helper()
	entry, err := FindComponentEntry(d, name)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

const editBase = `spec:
  defaultValues:
    size: S
    components:
      postgres:
      - name: psql
        config:
          dependencies:
            pgbackuprepository:
              name: backups
          # trailing comment of config
        patches:
        - target:
            kind: PgCluster
          patch: |-
            - op: add
              path: /x
              value: 1
      kafka:
      - name: kafka1
      # hdfs:
      # - name: hdfs1
`

func TestAddToEntry_AddsAfterTheLastKeyOfConfigAndKeepsEveryOtherLine(t *testing.T) {
	d := docFrom(t, editBase)
	err := d.AddToEntry(entryNamed(t, d, "psql"),
		[]Field{{"retention", "7d"}, {"enabled", true}},
		[]DependencyRef{{Key: "pgbouncer", Name: "pool"}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(d.Text())
	want := strings.Replace(editBase, "              name: backups\n",
		"              name: backups\n            pgbouncer:\n              name: pool\n          retention: 7d\n          enabled: true\n", 1)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAddToEntry_CreatesConfigWhenAbsentAfterTheWholeEntry(t *testing.T) {
	d := docFrom(t, editBase)
	if err := d.AddToEntry(entryNamed(t, d, "kafka1"), []Field{{"kafkaClusterExposed", "false"}}, []DependencyRef{{Key: "postgres", Name: "psql"}}); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editBase, "      - name: kafka1\n",
		"      - name: kafka1\n        config:\n          kafkaClusterExposed: \"false\"\n          dependencies:\n            postgres:\n              name: psql\n", 1)
	if got := string(d.Text()); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAddToEntry_AfterABlockScalarEntry(t *testing.T) {
	// The psql entry ends in a literal block scalar; config sits before it,
	// but an entry whose last key is the block scalar must get its config
	// after the scalar's last line.
	text := `spec:
  defaultValues:
    components:
      postgres:
      - name: psql
        patches:
        - patch: |-
            - op: add
              path: /x
      kafka:
      - name: kafka1
`
	d := docFrom(t, text)
	if err := d.AddToEntry(entryNamed(t, d, "psql"), []Field{{"a", 1}}, nil); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(text, "              path: /x\n", "              path: /x\n        config:\n          a: 1\n", 1)
	if got := string(d.Text()); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAddToEntry_NullConfigAndFlowConfig(t *testing.T) {
	d := docFrom(t, "spec:\n  defaultValues:\n    components:\n      kafka:\n      - name: k\n        config:\n")
	if err := d.AddToEntry(entryNamed(t, d, "k"), []Field{{"a", "b"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := string(d.Text()), "spec:\n  defaultValues:\n    components:\n      kafka:\n      - name: k\n        config:\n          a: b\n"; got != want {
		t.Errorf("null config: got:\n%s\nwant:\n%s", got, want)
	}

	d = docFrom(t, "spec:\n  defaultValues:\n    components:\n      kafka:\n      - name: k\n        config: {a: 1}\n")
	if err := d.AddToEntry(entryNamed(t, d, "k"), []Field{{"b", 2}}, nil); err == nil {
		t.Error("a flow-style config was edited textually, want an error")
	}
}

func TestAddToEntry_FileWithoutTrailingNewline(t *testing.T) {
	d := docFrom(t, "spec:\n  defaultValues:\n    components:\n      kafka:\n      - name: k")
	if err := d.AddToEntry(entryNamed(t, d, "k"), []Field{{"a", "b"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := string(d.Text()), "spec:\n  defaultValues:\n    components:\n      kafka:\n      - name: k\n        config:\n          a: b\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAddTopLevel_BeforeComponents(t *testing.T) {
	d := docFrom(t, editBase)
	if err := d.AddTopLevel([]Field{{"tenantName", "stratio"}}); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editBase, "    size: S\n", "    size: S\n    tenantName: stratio\n", 1)
	if got := string(d.Text()); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if !d.HasTopLevel("size") || d.HasTopLevel("tenantName") {
		t.Error("HasTopLevel reads the loaded tree, which the textual additions don't change")
	}
}

func TestAppendEntry(t *testing.T) {
	d := docFrom(t, editBase)
	if err := d.AppendEntry("kafka", "kafka2", map[string]any{"type": "s3"}); err != nil {
		t.Fatal(err)
	}
	if err := d.AppendEntry("connectors", "connectors", nil); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editBase, "      - name: kafka1\n",
		"      - name: kafka1\n      - name: kafka2\n        config:\n          type: s3\n      connectors:\n      - name: connectors\n", 1)
	if got := string(d.Text()); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSaveAfterTextualAdditionsWritesThemAndKeepsEditing(t *testing.T) {
	d := docFrom(t, editBase)
	path := filepath.Join(t.TempDir(), "out.yaml")
	if err := os.WriteFile(path, []byte(editBase), 0o644); err != nil {
		t.Fatal(err)
	}
	d.source = []byte(editBase)
	if err := d.AddToEntry(entryNamed(t, d, "kafka1"), []Field{{"a", 1}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	if string(saved) != string(d.Text()) || !strings.Contains(string(saved), "a: 1") || !strings.Contains(string(saved), "# hdfs:\n") {
		t.Errorf("saved:\n%s", saved)
	}
	// The tree was re-read: a second, independent addition finds its line.
	if err := d.AddToEntry(entryNamed(t, d, "psql"), []Field{{"b", 2}}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Text()), "          b: 2\n") {
		t.Errorf("second addition lost:\n%s", d.Text())
	}
}

func TestAddToDependency_AddsAfterTheDependencyAndBeforeANewOne(t *testing.T) {
	d := docFrom(t, editBase)
	entry := entryNamed(t, d, "psql")
	// Recorded in this order: the dependency's own field, then a new
	// dependency landing on the same line.
	if err := d.AddToDependency(entry, "pgbackuprepository", []Field{{"enabled", true}}); err != nil {
		t.Fatal(err)
	}
	if err := d.AddToEntry(entry, nil, []DependencyRef{{Key: "pgbouncer", Name: "pool", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editBase, "              name: backups\n",
		"              name: backups\n              enabled: true\n            pgbouncer:\n              enabled: true\n              name: pool\n", 1)
	if got := string(d.Text()); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAddToDependency_FlowAndMissingDependencyAreErrors(t *testing.T) {
	d := docFrom(t, "spec:\n  defaultValues:\n    components:\n      kafka:\n      - name: k\n        config:\n          dependencies:\n            zk: {name: z}\n")
	entry := entryNamed(t, d, "k")
	if err := d.AddToDependency(entry, "zk", []Field{{"enabled", true}}); err == nil {
		t.Error("a flow-style dependency was edited textually, want an error")
	}
	if err := d.AddToDependency(entry, "nope", []Field{{"enabled", true}}); err == nil {
		t.Error("an undeclared dependency was edited, want an error")
	}
}
