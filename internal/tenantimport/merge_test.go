package tenantimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

func loadDoc(t *testing.T, name string) *tenantfile.Doc {
	t.Helper()
	doc, err := tenantfile.Load(filepath.Join("testdata", "complete", name))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// reload writes b to a scratch file and loads it, to feed a Complete's own
// output back into Complete.
func reload(t *testing.T, b []byte) *tenantfile.Doc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tenant.yaml")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := tenantfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func complete(t *testing.T, doc *tenantfile.Doc, scanned Components, live Live, opts CompleteOptions) (Report, string) {
	t.Helper()
	if opts.TenantName == "" {
		opts.TenantName = "stratio"
	}
	r, err := Complete(doc, scanned, live, loadFixtureCatalog(t), opts)
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	out := string(doc.Text())
	var parsed any
	if err := yaml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("Complete produced invalid YAML: %v\n%s", err, out)
	}
	return r, out
}

func liveVirtualizer(enabled bool, siblings ...string) Live {
	return Live{
		Values:   Derived{"virtualizer": {"uiEnabled": enabled}},
		Siblings: map[string][]string{"virtualizer": siblings},
	}
}

func contains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestComplete_PreservesEverythingAndOnlyAdds(t *testing.T) {
	doc := loadDoc(t, "eosdev.yaml")
	original, err := os.ReadFile(filepath.Join("testdata", "complete", "eosdev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	scanned := Components{
		"virtualizer":        {{Name: "virtualizer"}},
		"postgres":           {{Name: "psql"}},
		"pgbackuprepository": {{Name: "pgbackuprepository"}},
	}
	r, got := complete(t, doc, scanned, liveVirtualizer(true, "virtualizer-monitor", "virtualizer-ui"), CompleteOptions{})

	golden := filepath.Join("testdata", "complete", "eosdev.golden.yaml")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("completed file differs from golden:\n%s", got)
	}

	// Pure additions: every original line survives, in order.
	if !isSubsequence(strings.Split(string(original), "\n"), strings.Split(got, "\n")) {
		t.Errorf("the completed file is not the original plus added lines:\n%s", got)
	}
	for _, kept := range []string{"universeVersion: 15.1.0", "# kafka:", "#     topics: []", "# migrated by hand", "# the server, already migrated"} {
		if !strings.Contains(got, kept) {
			t.Errorf("lost %q", kept)
		}
	}
	for _, want := range []string{"uiEnabled: true", "internalS3BucketName: \"\""} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if !contains(r.Added, "components.virtualizer[virtualizer]: config.uiEnabled: true") {
		t.Errorf("Added = %v, want the uiEnabled addition", r.Added)
	}
	if !contains(r.Warnings, `dependency "pgbouncer" could not be resolved`) {
		t.Errorf("Warnings = %v, want the unresolved pgbouncer dependency", r.Warnings)
	}
	if strings.Contains(got, `name: ""`) {
		t.Error("an unresolved dependency was written as name: \"\"")
	}
	if !contains(r.LeftAlone, "left commented out: hdfs, kafka") {
		t.Errorf("LeftAlone = %v, want the commented-out blocks reported", r.LeftAlone)
	}
}

// isSubsequence reports whether every line of sub appears in full, in order.
func isSubsequence(sub, full []string) bool {
	i := 0
	for _, line := range full {
		if i < len(sub) && sub[i] == line {
			i++
		}
	}
	return i == len(sub)
}

func TestComplete_IsIdempotent(t *testing.T) {
	scanned := Components{
		"virtualizer": {{Name: "virtualizer"}},
		"postgres":    {{Name: "psql"}},
		"connectors":  {{Name: "connectors"}},
	}
	live := liveVirtualizer(true, "virtualizer-ui")
	_, first := complete(t, loadDoc(t, "eosdev.yaml"), scanned, live, CompleteOptions{})

	r, second := complete(t, reload(t, []byte(first)), scanned, live, CompleteOptions{})
	if !r.Empty() {
		t.Errorf("second run added %v, want nothing", r.Added)
	}
	if second != first {
		t.Errorf("second run changed the file:\n--- first\n%s\n--- second\n%s", first, second)
	}
}

func TestComplete_UIEnabled(t *testing.T) {
	const base = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  defaultValues:
    tenantName: stratio
    size: S
    sopsSecretProvided: false
    internalS3BucketName: ""
    userEmail: ""
    userId: ""
    userName: ""
    components:
      virtualizer:
      - name: virtualizer%s
`
	tests := []struct {
		name         string
		existing     string
		live         Live
		wantLine     string // must be in the result
		wantAdded    bool
		wantWarnWith string
	}{
		{name: "live legacy or Helm-managed siblings", existing: "", live: liveVirtualizer(true, "virtualizer-monitor"), wantLine: "uiEnabled: true", wantAdded: true, wantWarnWith: "apps migrate virtualizer"},
		{name: "no siblings", existing: "", live: liveVirtualizer(false), wantLine: "uiEnabled: false", wantAdded: true},
		{name: "existing false next to live siblings is kept and warned about", existing: "\n        config:\n          uiEnabled: false", live: liveVirtualizer(true, "virtualizer-ui"), wantLine: "uiEnabled: false", wantWarnWith: "virtualizer-ui"},
		{name: "existing true is kept", existing: "\n        config:\n          uiEnabled: true", live: liveVirtualizer(false), wantLine: "uiEnabled: true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := reload(t, []byte(strings.Replace(base, "%s", tt.existing, 1)))
			r, got := complete(t, doc, Components{"virtualizer": {{Name: "virtualizer"}}}, tt.live, CompleteOptions{})
			if !strings.Contains(got, tt.wantLine) {
				t.Errorf("result lacks %q:\n%s", tt.wantLine, got)
			}
			if added := contains(r.Added, "config.uiEnabled"); added != tt.wantAdded {
				t.Errorf("uiEnabled added = %v, want %v (Added %v)", added, tt.wantAdded, r.Added)
			}
			if tt.wantWarnWith != "" && !contains(r.Warnings, tt.wantWarnWith) {
				t.Errorf("Warnings = %v, want one naming %q", r.Warnings, tt.wantWarnWith)
			}
			if tt.wantWarnWith == "" && contains(r.Warnings, "uiEnabled") {
				t.Errorf("unexpected uiEnabled warning: %v", r.Warnings)
			}
		})
	}
}

func TestComplete_CommentedOutComponentIsNeverUncommentedOrReAdded(t *testing.T) {
	scanned := Components{"kafka": {{Name: "kafka1"}}}
	r, got := complete(t, loadDoc(t, "eosdev.yaml"), scanned, Live{}, CompleteOptions{})
	if strings.Contains(got, "\n      kafka:") || (strings.Contains(got, "name: kafka1\n") && !strings.Contains(got, "# - name: kafka1")) {
		t.Errorf("kafka was re-added:\n%s", got)
	}
	if !strings.Contains(got, "# kafka:\n") {
		t.Error("the commented-out kafka block did not survive")
	}
	if !contains(r.LeftAlone, "left commented out") || !contains(r.LeftAlone, "kafka") {
		t.Errorf("LeftAlone = %v, want a note about kafka", r.LeftAlone)
	}
}

func TestComplete_NoSkeletonForComponentsTheScanDidNotFind(t *testing.T) {
	// Create would add skeleton entries for postgres' and virtualizer's
	// dependencies; Complete must add no entry at all.
	_, got := complete(t, loadDoc(t, "eosdev.yaml"), Components{}, Live{}, CompleteOptions{})
	for _, key := range []string{"\n      pgbouncer:", "\n      connectors:", "\n      hdfs:"} {
		if strings.Contains(got, key) {
			t.Errorf("a skeleton %q was added:\n%s", strings.TrimSpace(key), got)
		}
	}
}

func TestComplete_UndeclaredLiveComponentIsAdded(t *testing.T) {
	scanned := Components{"connectors": {{Name: "connectors", Deps: map[string]string{}}}}

	r, got := complete(t, loadDoc(t, "eosdev.yaml"), scanned, Live{}, CompleteOptions{})
	if !strings.Contains(got, "      connectors:\n      - name: connectors") {
		t.Errorf("connectors not added:\n%s", got)
	}
	if !contains(r.Added, "components.connectors[connectors]") {
		t.Errorf("Added = %v", r.Added)
	}
	if contains(r.Warnings, "already declares") {
		t.Errorf("Warnings = %v, want none: no connectors entry existed", r.Warnings)
	}
}

func TestComplete_UndeclaredLiveComponentGetsItsRequiredNestedConfigScaffolded(t *testing.T) {
	required := &config.Catalog{Types: []config.ComponentType{{Type: "genai", Component: "genai", RequiredConfig: []string{"llmModels"}}}}
	// As Scan leaves it: enriched, its own warnings discarded.
	scanned := Components{"genai": {{Name: "genai", Deps: map[string]string{}}}}
	enrich(loadFixtureCatalog(t), scanned, requiredConfig(required), func(string, ...any) {})

	r, got := complete(t, loadDoc(t, "eosdev.yaml"), scanned, Live{}, CompleteOptions{Config: required})
	if !strings.Contains(got, "      genai:\n      - name: genai\n        config:\n          llmModels:\n            chat: \"\"\n            governance: \"\"\n            translate: \"\"\n") {
		t.Errorf("genai not added with its llmModels block:\n%s", got)
	}
	n := 0
	for _, w := range r.Warnings {
		if strings.Contains(w, `components.genai[genai]: config "llmModels" is required: added with its keys chat, governance, translate empty`) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Warnings = %v, want the scaffold reported once", r.Warnings)
	}
}

func TestComplete_UndeclaredLiveComponentUnderAKeyThatHasAnotherEntryWarns(t *testing.T) {
	scanned := Components{"postgres": {{Name: "psql-live", Deps: map[string]string{}}}}
	r, got := complete(t, loadDoc(t, "eosdev.yaml"), scanned, Live{}, CompleteOptions{})
	if !strings.Contains(got, "name: psql-live") {
		t.Errorf("psql-live not added:\n%s", got)
	}
	if !contains(r.Warnings, "components.postgres already declares psql") {
		t.Errorf("Warnings = %v, want the possible-rename warning", r.Warnings)
	}
}

// Several instances of one component the cluster runs are not a rename of one
// another: the possible-rename warning is for an entry the file declared.
func TestComplete_SeveralLiveInstancesAreNotReportedAsRenames(t *testing.T) {
	scanned := Components{"connectors": {
		{Name: "connectors-a", Deps: map[string]string{}},
		{Name: "connectors-b", Deps: map[string]string{}},
	}}
	r, got := complete(t, loadDoc(t, "eosdev.yaml"), scanned, Live{}, CompleteOptions{})
	if !strings.Contains(got, "name: connectors-a") || !strings.Contains(got, "name: connectors-b") {
		t.Errorf("both instances should be added:\n%s", got)
	}
	if contains(r.Warnings, "already declares") {
		t.Errorf("Warnings = %v, want no rename warning", r.Warnings)
	}
}

func TestComplete_DependencyResolvedFromScanOrReported(t *testing.T) {
	scanned := Components{"virtualizer": {{Name: "virtualizer"}}, "pgbouncer": {{Name: "pool-psql"}}}
	r, got := complete(t, loadDoc(t, "eosdev.yaml"), scanned, Live{}, CompleteOptions{})
	cfg := entryConfigOf(t, got, "virtualizer", "virtualizer")
	deps, _ := cfg["dependencies"].(map[string]any)
	if pg, _ := deps["pgbouncer"].(map[string]any); pg["name"] != "pool-psql" {
		t.Errorf("resolvable dependency not added: dependencies = %v\n%s", deps, got)
	}
	if !contains(r.Added, "config.dependencies.pgbouncer.name: pool-psql") {
		t.Errorf("Added = %v", r.Added)
	}
	// postgres was already named by the operator, differently from the scan.
	scanned["postgres"] = []*Entry{{Name: "other-psql"}}
	_, got = complete(t, loadDoc(t, "eosdev.yaml"), scanned, Live{}, CompleteOptions{})
	deps, _ = entryConfigOf(t, got, "virtualizer", "virtualizer")["dependencies"].(map[string]any)
	if pg, _ := deps["postgres"].(map[string]any); pg["name"] != "psql" {
		t.Errorf("an existing dependency name was changed: %v\n%s", deps, got)
	}
}

// A component the scan found but the file lacks is appended with the
// dependencies the scan could not resolve; the file may declare them (the
// operator added the target by hand), and the run must see that.
func TestComplete_NewEntryDependencyResolvedFromTheFile(t *testing.T) {
	// As Scan leaves a legacy pgbouncer whose postgres it could not resolve.
	scanned := Components{"pgbouncer": {{Name: "pool-psql", Deps: map[string]string{"postgres": ""}}}}
	r, got := complete(t, loadDoc(t, "eosdev.yaml"), scanned, Live{}, CompleteOptions{})

	deps, _ := entryConfigOf(t, got, "pgbouncer", "pool-psql")["dependencies"].(map[string]any)
	if pg, _ := deps["postgres"].(map[string]any); pg["name"] != "psql" {
		t.Errorf("postgres not resolved from the file's psql: dependencies = %v\n%s", deps, got)
	}
	if contains(r.Warnings, `dependency "postgres" could not be resolved`) {
		t.Errorf("Warnings = %v, want no unresolved postgres: the file declares it", r.Warnings)
	}
}

// ...or appended in the same run.
func TestComplete_NewEntryDependencyResolvedFromAnotherNewEntry(t *testing.T) {
	const file = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  defaultValues:
    tenantName: stratio
    size: S
    sopsSecretProvided: false
    internalS3BucketName: ""
    userEmail: ""
    userId: ""
    userName: ""
    components: []
`
	scanned := Components{
		"pgbouncer": {{Name: "pool-psql", Deps: map[string]string{"postgres": ""}}},
		"postgres":  {{Name: "psql", Deps: map[string]string{}}},
	}
	_, got := complete(t, reload(t, []byte(file)), scanned, Live{}, CompleteOptions{})
	deps, _ := entryConfigOf(t, got, "pgbouncer", "pool-psql")["dependencies"].(map[string]any)
	if pg, _ := deps["postgres"].(map[string]any); pg["name"] != "psql" {
		t.Errorf("postgres not resolved from the entry added next to it: dependencies = %v\n%s", deps, got)
	}
}

func TestComplete_NewEntryDependencyStillUnresolvedIsReportedNotWritten(t *testing.T) {
	scanned := Components{"pgbouncer": {{Name: "pool-psql", Deps: map[string]string{"postgres": ""}}}}
	const file = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  defaultValues:
    tenantName: stratio
    size: S
    sopsSecretProvided: false
    internalS3BucketName: ""
    userEmail: ""
    userId: ""
    userName: ""
    components: []
`
	r, got := complete(t, reload(t, []byte(file)), scanned, Live{}, CompleteOptions{})
	if !contains(r.Warnings, `components.pgbouncer[pool-psql]: dependency "postgres" could not be resolved`) {
		t.Errorf("Warnings = %v, want the unresolved postgres reported once", r.Warnings)
	}
	if strings.Contains(got, `name: ""`) {
		t.Errorf("an unresolved dependency was written as name: \"\":\n%s", got)
	}
}

func TestComplete_SeveralCandidatesPreferTheFilesChoiceAndWarn(t *testing.T) {
	// The scan finds two postgres; the file declares only psql, which is one of them.
	scanned := Components{
		"postgres":  {{Name: "other", Deps: map[string]string{}}, {Name: "psql", Deps: map[string]string{}}},
		"pgbouncer": {{Name: "pool-psql", Deps: map[string]string{"postgres": ""}}},
	}
	r, got := complete(t, loadDoc(t, "eosdev.yaml"), scanned, Live{}, CompleteOptions{})
	deps, _ := entryConfigOf(t, got, "pgbouncer", "pool-psql")["dependencies"].(map[string]any)
	if pg, _ := deps["postgres"].(map[string]any); pg["name"] != "psql" {
		t.Errorf("the entry both the scan and the file have was not preferred: %v", deps)
	}
	if !contains(r.Warnings, `dependency "postgres" has 2 candidates (psql, other); used "psql"`) {
		t.Errorf("Warnings = %v, want the candidates listed", r.Warnings)
	}
}

func TestResolveDep_Order(t *testing.T) {
	scanned := Components{"k": {{Name: "c"}, {Name: "a"}, {Name: "d"}}}
	byKey := map[string][]string{"k": {"b", "a", "a", "c"}}
	got := resolveDep("k", "", scanned, byKey)
	// in both lists (file order: a, c), then the file's alone (b), then the scan's alone (d)
	want := []string{"a", "c", "b", "d"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("resolveDep = %v, want %v", got, want)
	}
	if got := resolveDep("none", "", scanned, byKey); len(got) != 0 {
		t.Errorf("resolveDep for an unknown key = %v, want none", got)
	}
}

const gitopsOnlyBase = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  defaultValues:
    tenantName: stratio
    size: S
    sopsSecretProvided: false
    internalS3BucketName: ""
    userEmail: ""
    userId: ""
    userName: ""
    components:
      postgres:
      - name: psql%s
`

// A component only GitOps has (no catalog type recognises it on a legacy
// cluster) that something waits on unconditionally is added, with a warning,
// and its dependent is pointed at it.
func TestComplete_GitOpsOnlyDependencyIsScaffolded(t *testing.T) {
	doc := reload(t, []byte(strings.Replace(gitopsOnlyBase, "%s", "", 1)))
	r, got := complete(t, doc, Components{}, Live{}, CompleteOptions{})

	if !strings.Contains(got, "      pgbackuprepository:\n      - name: pgbackuprepository") {
		t.Errorf("the GitOps-only entry was not added:\n%s", got)
	}
	deps, _ := entryConfigOf(t, got, "postgres", "psql")["dependencies"].(map[string]any)
	if pg, _ := deps["pgbackuprepository"].(map[string]any); pg["name"] != "pgbackuprepository" {
		t.Errorf("postgres does not depend on it: %v\n%s", deps, got)
	}
	if !contains(r.Warnings, "components.pgbackuprepository[pgbackuprepository]: GitOps-only") || !contains(r.Warnings, "components.postgres[psql] depends on it") {
		t.Errorf("Warnings = %v, want the GitOps-only entry explained", r.Warnings)
	}

	r2, second := complete(t, reload(t, []byte(got)), Components{}, Live{}, CompleteOptions{})
	if !r2.Empty() || second != got {
		t.Errorf("second run added %v, want nothing", r2.Added)
	}
}

// What the catalog can discover is never invented: no virtualizer dependency
// on connectors scaffolds one, since the cluster would have it if it ran.
func TestComplete_GitOpsOnlyScaffoldSkipsWhatTheCatalogCanDiscover(t *testing.T) {
	doc := reload(t, []byte(strings.Replace(gitopsOnlyBase, "      postgres:\n      - name: psql%s", "      virtualizer:\n      - name: virtualizer%s", 1)))
	_, got := complete(t, doc, Components{}, Live{}, CompleteOptions{})
	if strings.Contains(got, "      connectors:") {
		t.Errorf("a connectors entry was invented:\n%s", got)
	}
}

func TestComplete_GitOpsOnlyScaffoldRespectsACommentedOutBlock(t *testing.T) {
	file := strings.Replace(gitopsOnlyBase, "%s", "", 1) + "      # pgbackuprepository:\n      # - name: pgbackuprepository\n"
	r, got := complete(t, reload(t, []byte(file)), Components{}, Live{}, CompleteOptions{})
	if strings.Contains(got, "\n      pgbackuprepository:") || contains(r.Warnings, "GitOps-only") {
		t.Errorf("a commented-out block was scaffolded over:\n%s\n%v", got, r.Warnings)
	}
}

func TestComplete_FieldWithoutDefaultIsReportedNotDropped(t *testing.T) {
	// The fixture's kafka template reads "topics" with no usable default.
	const file = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  defaultValues:
    components:
      kafka:
      - name: kafka1
`
	// topics is read with `| default list`: optional, unless the catalog
	// lists it as required — then it is reported, never invented.
	r, got := complete(t, reload(t, []byte(file)), Components{}, Live{}, CompleteOptions{})
	if contains(r.Warnings, `"topics"`) {
		t.Errorf("Warnings = %v, want the optional topics field left alone", r.Warnings)
	}
	required := &config.Catalog{Types: []config.ComponentType{{Type: "kafka", Component: "kafka", RequiredConfig: []string{"topics"}}}}
	r, got = complete(t, reload(t, []byte(file)), Components{}, Live{}, CompleteOptions{Config: required})
	if !contains(r.Warnings, `config "topics" has no default`) {
		t.Errorf("Warnings = %v, want the required topics field reported", r.Warnings)
	}
	if !strings.Contains(got, `kafkaClusterExposed: "false"`) {
		t.Errorf("the kafkaClusterExposed default was not added:\n%s", got)
	}
	if strings.Contains(got, "topics") {
		t.Errorf("a placeholder was invented for topics:\n%s", got)
	}
}

func TestComplete_RequiredNestedConfigIsScaffoldedThenReportedUntilFilled(t *testing.T) {
	const head = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  defaultValues:
    components:
      genai:
      - name: genai
`
	required := &config.Catalog{Types: []config.ComponentType{{Type: "genai", Component: "genai", RequiredConfig: []string{"llmModels"}}}}
	opts := CompleteOptions{Config: required}

	// Absent: the block is added with its keys empty, and reported.
	r, got := complete(t, reload(t, []byte(head)), Components{}, Live{}, opts)
	if !strings.Contains(got, "        config:\n          llmModels:\n            chat: \"\"\n            governance: \"\"\n            translate: \"\"\n") {
		t.Errorf("llmModels was not scaffolded as a block:\n%s", got)
	}
	if !contains(r.Added, `config.llmModels: {chat: "", governance: "", translate: ""}`) {
		t.Errorf("Added = %v, want the llmModels block", r.Added)
	}
	if !contains(r.Warnings, `config "llmModels" is required: added with its keys chat, governance, translate empty`) {
		t.Errorf("Warnings = %v, want the scaffold reported", r.Warnings)
	}

	// Run again on the result: nothing added, the empty keys still reported.
	r, again := complete(t, reload(t, []byte(got)), Components{}, Live{}, opts)
	if again != got || len(r.Added) > 0 {
		t.Errorf("second run changed the file (Added = %v):\n%s", r.Added, again)
	}
	for _, leaf := range []string{"chat", "governance", "translate"} {
		if !contains(r.Warnings, "config.llmModels."+leaf+" is empty") {
			t.Errorf("Warnings = %v, want the empty %s reported", r.Warnings, leaf)
		}
	}

	// Partly filled by hand: only the empty and missing keys are reported.
	partial := head + "        config:\n          llmModels:\n            chat: gpt-4o\n            governance: \"\"\n"
	r, out := complete(t, reload(t, []byte(partial)), Components{}, Live{}, opts)
	if strings.Contains(out, "translate") || contains(r.Warnings, "llmModels.chat") ||
		!contains(r.Warnings, "llmModels.governance is empty") || !contains(r.Warnings, "llmModels.translate is empty") {
		t.Errorf("Warnings = %v, want governance and translate reported and nothing added inside llmModels:\n%s", r.Warnings, out)
	}

	// Filled: nothing to say.
	filled := head + "        config:\n          llmModels:\n            chat: a\n            governance: b\n            translate: c\n"
	if r, _ := complete(t, reload(t, []byte(filled)), Components{}, Live{}, opts); contains(r.Warnings, "llmModels") {
		t.Errorf("Warnings = %v, want none for a filled llmModels", r.Warnings)
	}
}

func TestComplete_TopLevelFieldsOnlyWhenAbsent(t *testing.T) {
	r, got := complete(t, loadDoc(t, "eosdev.yaml"), Components{}, Live{}, CompleteOptions{Size: "M"})
	if !contains(r.Added, "spec.defaultValues.internalS3BucketName") {
		t.Errorf("Added = %v, want internalS3BucketName", r.Added)
	}
	for _, kept := range []string{"size: S\n", "sopsSecretProvided: true", "userEmail: me@stratio.com", "universePrereleaseSuffix: -rc1"} {
		if !strings.Contains(got, kept) {
			t.Errorf("lost or changed %q", kept)
		}
	}
	if strings.Contains(got, "size: M") {
		t.Error("an existing size was overwritten")
	}
}

func TestCreate_DroppedFieldsWarn(t *testing.T) {
	var warnings []string
	components := Components{"kafka": {{Name: "kafka1", Deps: map[string]string{}}}}
	warn := func(format string, a ...any) { warnings = append(warnings, strings.TrimSpace(format)) }
	enrich(loadFixtureCatalog(t), components, nil, warn)
	if contains(warnings, "has no default") {
		t.Errorf("warnings = %v, want none: topics defaults to an empty list", warnings)
	}
	enrich(loadFixtureCatalog(t), components, map[string]map[string]bool{"kafka": {"topics": true}}, warn)
	if !contains(warnings, "has no default") {
		t.Errorf("warnings = %v, want one for the required topics field", warnings)
	}
}

func TestDefaultValue(t *testing.T) {
	tests := []struct {
		field string
		bare  bool
		want  any
	}{
		{"false", true, false},
		{"true", true, true},
		{"3", true, int64(3)},
		{"0.5", true, 0.5},
		{"x", true, "x"},
		{"false", false, "false"},
	}
	for _, tt := range tests {
		got := defaultValue(catalogField(tt.field, tt.bare))
		if got != tt.want {
			t.Errorf("defaultValue(%q, bare=%v) = %#v, want %#v", tt.field, tt.bare, got, tt.want)
		}
	}
}

func catalogField(def string, bare bool) catalog.ExtraConfigField {
	return catalog.ExtraConfigField{Key: "k", Default: def, HasDefault: true, Bare: bare}
}

// entryConfigOf parses a tenant file and returns the config of one entry.
func entryConfigOf(t *testing.T, text, key, name string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("result is not valid YAML: %v\n%s", err, text)
	}
	components := doc["spec"].(map[string]any)["defaultValues"].(map[string]any)["components"].(map[string]any)
	entries, _ := components[key].([]any)
	for _, e := range entries {
		if m := e.(map[string]any); m["name"] == name {
			cfg, _ := m["config"].(map[string]any)
			return cfg
		}
	}
	t.Fatalf("no components.%s[%s] in:\n%s", key, name, text)
	return nil
}
