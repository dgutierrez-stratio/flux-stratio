package components

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

// scripted is a test Prompter answering each Choose with the next index in
// answers (ErrNoAnswer once they run out), recording every question asked.
type scripted struct {
	answers   []int
	questions []string
	options   [][]string
}

func (s *scripted) Choose(q string, options []string) (int, error) {
	s.questions = append(s.questions, q)
	s.options = append(s.options, options)
	if len(s.answers) == 0 {
		return 0, ErrNoAnswer
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a, nil
}

func loadTenant(t *testing.T) *tenantfile.Doc {
	t.Helper()
	d, err := tenantfile.Load("testdata/tenant.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func baseOptions(t *testing.T) Options {
	return Options{
		Catalog: seededCatalog(),
		Objects: loadLiveFixture(t),
		Tenant:  "stratio",
		Doc:     loadTenant(t),
		Log:     log.New(&bytes.Buffer{}, false),
	}
}

func TestResolve_ManifestInstanceByLiveName(t *testing.T) {
	app, err := Resolve(baseOptions(t), "psql")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if app.ID != "psql" || app.Type != "postgres" || app.Kustomization != "apps-psql" || app.Object != "psql" || app.Entry != "psql" {
		t.Errorf("unexpected app: %+v", app)
	}
	if app.ChartPath != "" || len(app.Exclude) == 0 || app.Rset == "" {
		t.Errorf("type facts not carried over: %+v", app)
	}
	if app.LiveName() != "psql" || app.LiveNamespace() != "stratio-datastores" || app.Live[0].GVK.Kind != "PgCluster" {
		t.Errorf("live ref = %+v", app.Live)
	}
}

func TestResolve_GosecAgentByLegacyOrGitOpsName(t *testing.T) {
	for _, name := range []string{"psql-agent", "psql-gosec-agent"} {
		t.Run(name, func(t *testing.T) {
			app, err := Resolve(baseOptions(t), name)
			if err != nil {
				t.Fatalf("Resolve returned error: %v", err)
			}
			if app.Entry != "psql" || app.Object != "psql-gosec-agent" || app.Kustomization != "apps-psql-gosec-agent" {
				t.Errorf("unexpected names: entry=%q object=%q kustomization=%q", app.Entry, app.Object, app.Kustomization)
			}
			if app.LiveName() != "psql-agent" || app.ChartPath != "gosec-agent" {
				t.Errorf("LiveName=%q ChartPath=%q", app.LiveName(), app.ChartPath)
			}
		})
	}
}

func TestResolve_ChartTypeNeverPicksSameNamedPgDatabase(t *testing.T) {
	app, err := Resolve(baseOptions(t), "genai")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if app.Type != "genai" || app.Live[0].GVK.Kind != "Deployment" || app.LiveName() != "genai-api" {
		t.Errorf("app = %+v, want the genai chart anchored on its genai-api Deployment", app)
	}
}

func TestResolve_UndeclaredEntryPromptsAmongTenantEntries(t *testing.T) {
	opts := baseOptions(t)
	p := &scripted{answers: []int{0}}
	opts.Prompter = p

	app, err := Resolve(opts, "kafka1")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if app.Entry != "kafka" || app.Object != "kafka" || app.Kustomization != "apps-kafka" || app.LiveName() != "kafka1" {
		t.Errorf("unexpected app: %+v", app)
	}
	if len(p.questions) != 1 || strings.Join(p.options[0], ",") != "kafka" {
		t.Errorf("questions=%v options=%v, want one question offering the declared kafka entries", p.questions, p.options)
	}
}

func TestResolve_UndeclaredEntryWithoutAnswerNamesAs(t *testing.T) {
	_, err := Resolve(baseOptions(t), "kafka1") // NonInteractive by default
	if err == nil || !strings.Contains(err.Error(), "--as") {
		t.Errorf("Resolve error = %v, want it to suggest --as", err)
	}
}

func TestResolve_AsPinsEntry(t *testing.T) {
	opts := baseOptions(t)
	opts.As = "kafka/kafka"
	app, err := Resolve(opts, "kafka1")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if app.Entry != "kafka" {
		t.Errorf("Entry = %q, want kafka", app.Entry)
	}
}

func TestResolve_AsWithUndeclaredEntryFails(t *testing.T) {
	opts := baseOptions(t)
	opts.As = "kafka/nope"
	_, err := Resolve(opts, "kafka1")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("Resolve error = %v, want it to reject the undeclared entry", err)
	}
}

func TestResolve_AsUnknownTypeFails(t *testing.T) {
	opts := baseOptions(t)
	opts.As = "bogus"
	if _, err := Resolve(opts, "psql"); err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Errorf("Resolve error = %v, want unknown type", err)
	}
}

func TestResolve_SameEntryInTwoNamespacesAsks(t *testing.T) {
	// Same tenant, same entry, two namespaces — a real "which one?" (the
	// other-tenant case never gets this far; see
	// TestClassify_OtherTenantsObjectsExcluded).
	opts := baseOptions(t)
	opts.Doc = nil
	opts.Objects = []*unstructured.Unstructured{
		deployment("opensearch1-agent", "keos-core", map[string]string{"cct.stratio.com/application_service": "os-gosec-agent"}),
		deployment("opensearch1-agent", "stratio-datastores", map[string]string{"cct.stratio.com/application_service": "os-gosec-agent"}),
	}
	p := &scripted{answers: []int{1}}
	opts.Prompter = p

	app, err := Resolve(opts, "opensearch1-agent")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(p.questions) != 1 || len(p.options[0]) != 2 {
		t.Fatalf("questions=%v options=%v, want one question with 2 options", p.questions, p.options)
	}
	if app.LiveNamespace() != "stratio-datastores" || app.Object != "opensearch1-gosec-agent" {
		t.Errorf("chose %s/%s object %q, want the second (stratio-datastores) candidate", app.LiveNamespace(), app.LiveName(), app.Object)
	}
}

func TestResolve_OtherTenantsCopyNeverAsked(t *testing.T) {
	app, err := Resolve(baseOptions(t), "opensearch1-agent") // NonInteractive: any question would fail
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if app.LiveNamespace() != "stratio-datastores" {
		t.Errorf("LiveNamespace = %q, want the stratio tenant's copy", app.LiveNamespace())
	}
}

func TestResolve_NoTenantDocSkipsEntryChecks(t *testing.T) {
	opts := baseOptions(t)
	opts.Doc = nil
	app, err := Resolve(opts, "kafka1")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if app.Entry != "kafka1" {
		t.Errorf("Entry = %q, want the derived kafka1 unchanged", app.Entry)
	}
}

func TestResolve_UnclassifiedLiveObjectExplained(t *testing.T) {
	// genai-gateway is a genai chart sibling: genai's selector pins the
	// genai-api model, so no type selects it.
	_, err := Resolve(baseOptions(t), "genai-gateway")
	if err == nil || !strings.Contains(err.Error(), "none is selected") || !strings.Contains(err.Error(), "Deployment stratio-genai/genai-gateway") {
		t.Errorf("Resolve error = %v, want it to name the live object no type selects", err)
	}
}

func TestResolve_UnknownNameFails(t *testing.T) {
	if _, err := Resolve(baseOptions(t), "does-not-exist"); err == nil {
		t.Error("Resolve returned nil error for an unknown name")
	}
}

func TestResolveAll_SkipsUndeclaredComponentsAndAsksAboutTheRest(t *testing.T) {
	opts := baseOptions(t)
	var logs bytes.Buffer
	opts.Log = log.New(&logs, false)
	// Three questions, all answered with the first option: kafka1's entry
	// (only "kafka" is declared), dg-postgresql-internal-agent's entry
	// (only "dg-hdfs-agent" is declared), then the resulting duplicate —
	// both dg-agents now mapping to dg-hdfs-agent. (The keos tenant's
	// opensearch1 copies are excluded outright, never asked about.)
	p := &scripted{answers: []int{0, 0, 0}}
	opts.Prompter = p

	apps, unresolved, err := ResolveAll(opts)
	if err != nil || len(unresolved) > 0 {
		t.Fatalf("ResolveAll returned error %v, unresolved %v", err, unresolved)
	}

	var ids []string
	for _, a := range apps {
		ids = append(ids, a.Type+"/"+a.ID)
	}
	want := "postgres/psql,pgbouncer/pool-psql,postgres-gosec-agent/psql-gosec-agent,opensearch/opensearch1," +
		"opensearch-gosec-agent/opensearch1-gosec-agent,kafka/kafka,dg-agent/dg-hdfs-agent,genai/genai"
	if strings.Join(ids, ",") != want {
		t.Errorf("apps = %s\nwant   %s", strings.Join(ids, ","), want)
	}
	if len(p.questions) != 3 {
		t.Errorf("asked %d questions, want 3: %v", len(p.questions), p.questions)
	}
	for _, skipped := range []string{"components.hdfs", "components.rocket", "components.virtualizer"} {
		if !strings.Contains(logs.String(), skipped) {
			t.Errorf("log missing a skip warning for %s:\n%s", skipped, logs.String())
		}
	}
}

func TestResolveAll_NonInteractiveReportsEveryUnansweredAndResolvesTheRest(t *testing.T) {
	apps, unresolved, err := ResolveAll(baseOptions(t)) // NonInteractive
	if err != nil {
		t.Fatalf("ResolveAll returned error: %v", err)
	}
	// kafka1 and dg-postgresql-internal-agent both need an entry answer.
	if len(unresolved) != 2 {
		t.Fatalf("unresolved = %v, want 2 (kafka1, dg-postgresql-internal-agent)", unresolved)
	}
	for _, u := range unresolved {
		if !errors.Is(u, ErrNoAnswer) || !strings.Contains(u.Error(), "--as") {
			t.Errorf("unresolved error %q should wrap ErrNoAnswer and name --as", u)
		}
	}
	var ids []string
	for _, a := range apps {
		ids = append(ids, a.ID)
	}
	if want := "psql,pool-psql,psql-gosec-agent,opensearch1,opensearch1-gosec-agent,dg-hdfs-agent,genai"; strings.Join(ids, ",") != want {
		t.Errorf("resolved apps = %s, want %s", strings.Join(ids, ","), want)
	}
}

func TestResolveAll_WithoutTenantDocResolvesEverything(t *testing.T) {
	opts := baseOptions(t)
	opts.Doc = nil
	apps, unresolved, err := ResolveAll(opts) // NonInteractive: nothing left to ask
	if err != nil || len(unresolved) > 0 {
		t.Fatalf("ResolveAll returned error %v, unresolved %v", err, unresolved)
	}
	if len(apps) != 20 {
		t.Errorf("len(apps) = %d, want 20 (one per stratio-tenant instance)", len(apps))
	}
}

// TestResolve_LitellmKeepsItsLegacyName: litellm's data is bound to its
// Helm *release* name (cert CN, Vault paths, gosec identity), so its tenant
// entry stays the legacy genai-litellm name — that's what keos-use-cases
// substitutes into LITELLM_NAME, which only sets spec.releaseName. The
// GitOps object itself (HelmRelease/Kustomization, and so ID, which is
// derived from Object) is decoupled from that and stays the fixed, generic
// "litellm", uniform with every other component: Object is pinned rather
// than following the default templates. The live object is still found by
// its legacy name (Resolve("genai-litellm") below) even though the
// instance now identifies itself as "litellm" going forward.
func TestResolve_LitellmKeepsItsLegacyName(t *testing.T) {
	opts := baseOptions(t)
	opts.Doc = nil
	app, err := Resolve(opts, "genai-litellm")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if app.Type != "litellm" || app.ID != "litellm" || app.Entry != "genai-litellm" || app.Object != "litellm" ||
		app.Kustomization != "apps-litellm" || app.ChartPath != "litellm" || app.LiveNamespace() != "stratio-genai" {
		t.Errorf("Resolve(genai-litellm) = %+v", app)
	}
}

func TestTerminal_ChoosesReaskAndEOF(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    int
		wantErr error
	}{
		{"valid", "2\n", 1, nil},
		{"reask then valid", "9\nx\n1\n", 0, nil},
		{"blank line", "\n", 0, ErrNoAnswer},
		{"EOF", "", 0, ErrNoAnswer},
		{"too many invalid", "9\n9\n9\n1\n", 0, ErrNoAnswer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := NewTerminal(strings.NewReader(c.input), &out).Choose("which?", []string{"a", "b"})
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if err == nil && got != c.want {
				t.Errorf("Choose = %d, want %d", got, c.want)
			}
			if !strings.Contains(out.String(), "1) a") || !strings.Contains(out.String(), "2) b") {
				t.Errorf("prompt output missing numbered options:\n%s", out.String())
			}
		})
	}
}

// TestResolve_MigratedChartInstance: after a same-name migration the
// HelmRelease adopts the legacy Deployment and Helm drops its CCT
// annotations; a named lookup still resolves it through the HelmRelease
// deploying the type's chart.
func TestResolve_MigratedChartInstance(t *testing.T) {
	opts := baseOptions(t)
	opts.Doc = nil
	opts.Objects = migrated(opts.Objects, "virtualizer", "stratio-apps", "virtualizer")
	app, err := Resolve(opts, "virtualizer")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if app.Type != "virtualizer" || app.Object != "virtualizer" || app.ChartPath != "virtualizer" || app.LiveNamespace() != "stratio-apps" {
		t.Errorf("unexpected app: %+v", app)
	}
}

// TestResolve_MigratedSharedChartAsksWhichType: both gosec agent types
// deploy charts/gosec-agent, so with no legacy object left to anchor it a
// migrated agent is asked about — or answered with --as.
func TestResolve_MigratedSharedChartAsksWhichType(t *testing.T) {
	opts := baseOptions(t)
	opts.Doc = nil
	opts.Objects = append(opts.Objects,
		managedDeployment("pg2-gosec-agent", "stratio-datastores", "pg2-gosec-agent", "stratio"),
		helmRelease("pg2-gosec-agent", "stratio-datastores", "gosec-agent"))

	if _, err := Resolve(opts, "pg2-gosec-agent"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("Resolve error = %v, want an unanswered ambiguity between the gosec agent types", err)
	}

	opts.As = "postgres-gosec-agent"
	app, err := Resolve(opts, "pg2-gosec-agent")
	if err != nil {
		t.Fatalf("Resolve --as returned error: %v", err)
	}
	if app.Type != "postgres-gosec-agent" || app.Entry != "pg2" || app.Object != "pg2-gosec-agent" {
		t.Errorf("unexpected app: %+v", app)
	}
}

// TestResolve_MigratedOtherTenantsWorkloadExcluded: the platform's own
// keos-core gosec agent carries keos's tenant label, not CCT's annotation;
// it's still never an instance for the stratio tenant.
func TestResolve_MigratedOtherTenantsWorkloadExcluded(t *testing.T) {
	opts := baseOptions(t)
	opts.Doc = nil
	opts.Objects = append(opts.Objects,
		managedDeployment("opensearcher-gosec-agent", "keos-core", "opensearcher-gosec-agent", "keos"),
		helmRelease("opensearcher-gosec-agent", "keos-core", "gosec-agent"))
	if _, err := Resolve(opts, "opensearcher-gosec-agent"); err == nil || !strings.Contains(err.Error(), "none is selected") {
		t.Errorf("Resolve error = %v, want the keos tenant's agent left unselected", err)
	}
}

// migratedGenai is eosdev's genai after migration: the genai HelmRelease
// adopted both legacy workloads, and Helm stripped their CCT annotations.
func migratedGenai(objs []*unstructured.Unstructured) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, o := range objs {
		if o.GetKind() == "Deployment" && o.GetNamespace() == "stratio-genai" && o.GetName() == "genai-api" {
			continue
		}
		out = append(out, o)
	}
	return append(out,
		managedDeployment("genai-api", "stratio-genai", "genai", "stratio"),
		managedDeployment("genai-ui", "stratio-genai", "genai", "stratio"),
		helmRelease("genai", "stratio-genai", "genai"))
}

// TestResolve_MigratedChartInstanceByAnyOfItsWorkloads: any workload a
// migrated instance's HelmRelease renders, or the HelmRelease itself,
// resolves to that one instance — its object the HelmRelease's name, its
// entry the declared one whose object that is, inferred without asking.
func TestResolve_MigratedChartInstanceByAnyOfItsWorkloads(t *testing.T) {
	for _, name := range []string{"genai-ui", "genai-api", "genai"} {
		t.Run(name, func(t *testing.T) {
			opts := baseOptions(t)
			opts.Objects = migratedGenai(opts.Objects)
			prompter := &scripted{}
			opts.Prompter = prompter

			app, err := Resolve(opts, name)
			if err != nil {
				t.Fatalf("Resolve returned error: %v", err)
			}
			if len(prompter.questions) != 0 {
				t.Errorf("asked %v, want the entry inferred", prompter.questions)
			}
			if app.Type != "genai" || app.ID != "genai" || app.Object != "genai" || app.Entry != "genai" || app.Kustomization != "apps-genai" {
				t.Errorf("unexpected app: %+v", app)
			}
			var live []string
			for _, ref := range app.Live {
				live = append(live, ref.Name)
			}
			if len(live) != 2 || (name != "genai" && live[0] != name) {
				t.Errorf("Live = %v, want both workloads, %q first", live, name)
			}
		})
	}
}

// TestResolve_MigratedChartInstanceWithoutTenantFile: backup and drift
// resolve without a tenant file; the object still comes from the
// HelmRelease, so a capture lands under the same app ID as the legacy one.
func TestResolve_MigratedChartInstanceWithoutTenantFile(t *testing.T) {
	opts := baseOptions(t)
	opts.Doc = nil
	opts.Objects = migratedGenai(opts.Objects)
	app, err := Resolve(opts, "genai-ui")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if app.ID != "genai" || app.Object != "genai" {
		t.Errorf("unexpected app: %+v", app)
	}
}

// TestResolve_SiblingNotesOnlyForTheResolvedType: virtualizer on eosdev —
// its anchor migrated, its -monitor/-ui siblings left legacy. Resolving an
// unrelated app says nothing about them; resolving virtualizer says what
// they are.
func TestResolve_SiblingNotesOnlyForTheResolvedType(t *testing.T) {
	var buf bytes.Buffer
	opts := baseOptions(t)
	opts.Doc = nil
	opts.Log = log.New(&buf, false)
	opts.Objects = migrated(opts.Objects, "virtualizer", "stratio-apps", "virtualizer")

	if _, err := Resolve(opts, "psql"); err != nil {
		t.Fatalf("Resolve(psql) returned error: %v", err)
	}
	if strings.Contains(buf.String(), "virtualizer") {
		t.Errorf("resolving psql logged %q, want nothing about virtualizer", buf.String())
	}

	buf.Reset()
	if _, err := Resolve(opts, "virtualizer"); err != nil {
		t.Fatalf("Resolve(virtualizer) returned error: %v", err)
	}
	want := "Deployment stratio-apps/virtualizer-monitor is still a legacy CCT workload, but HelmRelease stratio-apps/virtualizer, which migrated virtualizer, doesn't render it"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("resolving virtualizer logged %q, want it to contain %q", buf.String(), want)
	}
}

func TestResolve_ManagedButUncataloguedChartExplained(t *testing.T) {
	opts := baseOptions(t)
	opts.Objects = append(opts.Objects,
		managedDeployment("connectors", "stratio-apps", "connectors", "stratio"),
		helmRelease("connectors", "stratio-apps", "connectors"))
	_, err := Resolve(opts, "connectors")
	if err == nil || !strings.Contains(err.Error(), "rendered by HelmRelease stratio-apps/connectors") {
		t.Errorf("Resolve error = %v, want it to say the object is already Helm-managed", err)
	}
}

// TestResolve_DgAgentObjectIsLiveNameNotEntry: dg-agent's storage-type
// overlay fixes the HelmRelease's name (dg-hdfs-agent) whatever the tenant
// entry is called, while the Kustomization is named after the entry — so
// mapping the live agent to entry dg-agent must still find dg-hdfs-agent
// in apps-dg-agent.
func TestResolve_DgAgentObjectIsLiveNameNotEntry(t *testing.T) {
	opts := baseOptions(t)
	opts.Doc = nil
	opts.As = "dg-agent/dg-agent"
	app, err := Resolve(opts, "dg-hdfs-agent")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if app.Entry != "dg-agent" || app.Object != "dg-hdfs-agent" || app.Kustomization != "apps-dg-agent" {
		t.Errorf("entry/object/kustomization = %q/%q/%q, want dg-agent/dg-hdfs-agent/apps-dg-agent", app.Entry, app.Object, app.Kustomization)
	}
}

func TestResolve_DatamarketAgentObjectIsFixedWhateverTheEntry(t *testing.T) {
	cases := []struct {
		as, entry string
	}{
		{"", "governance-datamarket-agent"},
		// A tenant file that kept the legacy entry name.
		{"datamarket-agent/datamarket-agent", "datamarket-agent"},
	}
	for _, c := range cases {
		t.Run(c.entry, func(t *testing.T) {
			opts := baseOptions(t)
			opts.Doc = nil
			opts.As = c.as
			app, err := Resolve(opts, "datamarket-agent")
			if err != nil {
				t.Fatalf("Resolve returned error: %v", err)
			}
			if app.Entry != c.entry || app.Object != "governance-datamarket-agent" || app.Kustomization != "apps-"+c.entry {
				t.Errorf("entry/object/kustomization = %q/%q/%q, want %s/governance-datamarket-agent/apps-%s", app.Entry, app.Object, app.Kustomization, c.entry, c.entry)
			}
		})
	}
}
