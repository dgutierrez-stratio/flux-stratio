package tenantfile

import (
	"os"
	"path/filepath"
	"slices"
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
	if err := d.AddToEntry(entry, nil, []DependencyRef{{Key: "pgbouncer", Name: "pool", Enabled: Bool(true)}}); err != nil {
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

func TestEmptyConfigLeaves(t *testing.T) {
	leaves := []string{"chat", "governance", "translate"}
	for name, tc := range map[string]struct {
		config string
		want   []string
	}{
		"absent":       {"", leaves},
		"not a map":    {"        config:\n          llmModels: x\n", leaves},
		"empty values": {"        config:\n          llmModels:\n            chat: \"\"\n            governance: ~\n            translate:\n", leaves},
		"partly set":   {"        config:\n          llmModels: {chat: a, translate: false}\n", []string{"governance"}},
		"all set":      {"        config:\n          llmModels:\n            chat: a\n            governance: b\n            translate: c\n", nil},
	} {
		d := docFrom(t, "spec:\n  defaultValues:\n    components:\n      genai:\n      - name: g\n"+tc.config)
		if got := EmptyConfigLeaves(entryNamed(t, d, "g"), "llmModels", leaves); !slices.Equal(got, tc.want) {
			t.Errorf("%s: EmptyConfigLeaves = %v, want %v", name, got, tc.want)
		}
	}
}

const emptyComponentsHead = "spec:\n  defaultValues:\n    size: S\n"

// An empty components value (a flow [] or {}, or a bare key) holds no entry
// yet, so AppendEntry turns it into a block mapping: only the line carrying
// the value changes (its trailing comment stays), everything else is added.
func TestAppendEntry_IntoAnEmptyComponents(t *testing.T) {
	appended := "      postgres:\n        - name: psql\n        - name: pg2\n          config:\n            type: s3\n      kafka:\n        - name: k1\n"
	for name, tc := range map[string]struct{ source, want string }{
		"empty flow sequence": {"    components: []\n", "    components:\n" + appended},
		"empty flow mapping":  {"    components: {}\n", "    components:\n" + appended},
		"bare key":            {"    components:\n", "    components:\n" + appended},
		"null":                {"    components: null\n", "    components:\n" + appended},
		"trailing comment":    {"    components: [] # none yet\n", "    components: # none yet\n" + appended},
		"spaced brackets":     {"    components:   [ ]   \n", "    components:\n" + appended},
		"no trailing newline": {"    components: []", "    components:\n" + appended},
	} {
		t.Run(name, func(t *testing.T) {
			d := docFrom(t, emptyComponentsHead+tc.source)
			for _, e := range []struct {
				key, name string
				config    map[string]any
			}{
				{"postgres", "psql", nil},
				{"postgres", "pg2", map[string]any{"type": "s3"}},
				{"kafka", "k1", nil},
			} {
				if err := d.AppendEntry(e.key, e.name, e.config); err != nil {
					t.Fatal(err)
				}
			}
			if got, want := string(d.Text()), emptyComponentsHead+tc.want; got != want {
				t.Errorf("got:\n%s\nwant:\n%s", got, want)
			}
			if got, err := d.ActiveEntries(); err != nil || len(got) != 0 {
				t.Errorf("ActiveEntries reads the loaded tree, which the textual additions don't change: got %v, %v", got, err)
			}
		})
	}
}

// Without a components key at all the block is created at the end of
// spec.defaultValues, after the scalars the same run adds there.
func TestAppendEntry_WithoutAComponentsKey(t *testing.T) {
	d := docFrom(t, emptyComponentsHead+"status: {}\n")
	if got, err := d.ActiveEntries(); err != nil || len(got) != 0 {
		t.Fatalf("a file without components has no active entries, got %v, %v", got, err)
	}
	if err := d.AddTopLevel([]Field{{"tenantName", "stratio"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.AppendEntry("postgres", "psql", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.AppendEntry("postgres", "pg2", nil); err != nil {
		t.Fatal(err)
	}
	want := emptyComponentsHead + "    tenantName: stratio\n    components:\n      postgres:\n        - name: psql\n        - name: pg2\nstatus: {}\n"
	if got := string(d.Text()); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Two entries under the same new key share one "key:" line, whatever order
// the calls interleave in, and a new key goes after every addition to an
// entry that precedes it.
func TestAppendEntry_GroupsEntriesUnderOneNewKey(t *testing.T) {
	d := docFrom(t, editBase)
	for _, e := range [][2]string{{"connectors", "c1"}, {"opensearch", "o1"}, {"connectors", "c2"}} {
		if err := d.AppendEntry(e[0], e[1], nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.AddToEntry(entryNamed(t, d, "kafka1"), []Field{{"a", 1}}, nil); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editBase, "      - name: kafka1\n",
		"      - name: kafka1\n        config:\n          a: 1\n"+
			"      connectors:\n      - name: c1\n      - name: c2\n      opensearch:\n      - name: o1\n", 1)
	if got := string(d.Text()); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Saving the result and reading it back gives a file whose entries are all
// found, so a second run over its own output has nothing left to add.
func TestAppendEntry_IntoAnEmptyComponentsSurvivesASaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant.yaml")
	if err := os.WriteFile(path, []byte(emptyComponentsHead+"    components: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range [][2]string{{"postgres", "psql"}, {"postgres", "pg2"}, {"kafka", "k1"}} {
		if err := d.AppendEntry(e[0], e[1], nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := d.AppendEntry("kafka", "k2", nil); err != nil {
		t.Fatalf("the document keeps editing after a save: %v", err)
	}
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := reloaded.ActiveEntries()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Key+"/"+e.Name)
	}
	if want := []string{"postgres/psql", "postgres/pg2", "kafka/k1", "kafka/k2"}; !slices.Equal(got, want) {
		t.Errorf("entries after a save and reload: got %v, want %v", got, want)
	}
}

func TestAppendEntry_ComponentsThatCannotBeExtendedAreErrors(t *testing.T) {
	for name, source := range map[string]string{
		"multi-line flow sequence": "    components: [\n    ]\n",
		"scalar":                   "    components: none\n",
		"non-empty flow mapping":   "    components: {kafka: [{name: k}]}\n",
	} {
		t.Run(name, func(t *testing.T) {
			d := docFrom(t, emptyComponentsHead+source)
			if err := d.AppendEntry("kafka", "k1", nil); err == nil {
				t.Errorf("AppendEntry succeeded on %q, want an error telling the operator to edit by hand", source)
			}
		})
	}
}
