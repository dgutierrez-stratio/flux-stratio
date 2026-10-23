package tenantimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/components"
	"github.com/Stratio/flux-stratio/internal/log"
)

// gatedCatalog is the fixture catalog with the named dependencies of component
// comp marked gated, as the templates would read them (the fixture's own
// ResourceSets gate nothing, and other tests rely on that).
func gatedCatalog(t *testing.T, comp string, adds map[string][]string, keys ...string) *catalog.Catalog {
	t.Helper()
	cat := loadFixtureCatalog(t)
	schema := cat.Schemas[comp]
	deps := slices.Clone(schema.Dependencies)
	for i := range deps {
		if slices.Contains(keys, deps[i].Key) {
			deps[i].Gated = true
			deps[i].Adds = adds[deps[i].Key]
		}
	}
	schema.Dependencies = deps
	cat.Schemas[comp] = schema
	return cat
}

// scriptedPrompter answers each question from answers in turn: an index, or a
// negative number for "no answer" (what a Terminal returns at EOF). Asked
// records the questions.
type scriptedPrompter struct {
	answers []int
	asked   []string
	err     error
}

func (p *scriptedPrompter) Choose(question string, _ []string) (int, error) {
	p.asked = append(p.asked, question)
	if p.err != nil {
		return 0, p.err
	}
	if len(p.answers) == 0 {
		return 0, components.ErrNoAnswer
	}
	a := p.answers[0]
	p.answers = p.answers[1:]
	if a < 0 {
		return 0, components.ErrNoAnswer
	}
	return a, nil
}

// failingPrompter fails the test if it is ever asked.
type failingPrompter struct{ t *testing.T }

func (p failingPrompter) Choose(q string, _ []string) (int, error) {
	p.t.Helper()
	p.t.Errorf("asked a question the tenant file already answers: %s", q)
	return 0, components.ErrNoAnswer
}

const (
	answerEnable = 0
	answerLeave  = 1
	answerNone   = -1
)

func warnings(dst *[]string) func(string, ...any) {
	return func(format string, a ...any) { *dst = append(*dst, fmt.Sprintf(format, a...)) }
}

func TestMarkGates_ProposesWhatRunsAndFlagsWhatDoesNot(t *testing.T) {
	cat := gatedCatalog(t, "virtualizer", map[string][]string{"postgres": {"sets PG_ENABLED"}}, "postgres", "pgbouncer")
	virt := &Entry{Name: "virtualizer", Deps: map[string]string{"postgres": "psql", "pgbouncer": "pool", "connectors": "connectors", "hdfs": ""}}
	psql := &Entry{Name: "psql"}
	placeholder := &Entry{Name: "pool"} // a skeleton the scan did not find: not running
	comps := Components{"virtualizer": {virt}, "postgres": {psql}, "pgbouncer": {placeholder}}
	running := map[*Entry]bool{virt: true, psql: true}

	markGates(cat, comps, running)

	if g, ok := virt.DepGate["postgres"]; !ok || g.Name != "psql" || !slices.Equal(g.Adds, []string{"sets PG_ENABLED"}) {
		t.Errorf("DepGate[postgres] = %+v (%v), want a proposal for psql carrying the template's hints", g, ok)
	}
	if !virt.DepAbsent["pgbouncer"] {
		t.Errorf("DepAbsent = %v, want pgbouncer: its entry exists only as a placeholder", virt.DepAbsent)
	}
	if _, ok := virt.DepGate["connectors"]; ok || virt.DepAbsent["connectors"] {
		t.Errorf("connectors is not gated, got gate=%v absent=%v", virt.DepGate, virt.DepAbsent)
	}
	if _, ok := virt.DepGate["hdfs"]; ok || virt.DepAbsent["hdfs"] {
		t.Errorf("an unresolved dependency is already warned about by enrich, got gate=%v absent=%v", virt.DepGate, virt.DepAbsent)
	}
	if len(psql.DepGate) != 0 || len(placeholder.DepGate) != 0 {
		t.Errorf("only a running entry gets proposals: psql=%v placeholder=%v", psql.DepGate, placeholder.DepGate)
	}
}

func TestDecider_Answers(t *testing.T) {
	newEntry := func() *Entry {
		return &Entry{Name: "genai", DepGate: map[string]DepGate{"discovery": {Name: "discovery", Adds: []string{"sets DISCOVERY_ENABLED"}}}}
	}
	cases := []struct {
		name     string
		decider  func() (Decider, *scriptedPrompter)
		want     DepState
		warnWant string // substring of the one warning, "" for none
	}{
		{"enable", func() (Decider, *scriptedPrompter) {
			p := &scriptedPrompter{answers: []int{answerEnable}}
			return Decider{Prompter: p}, p
		}, DepOn, ""},
		{"leave off", func() (Decider, *scriptedPrompter) {
			p := &scriptedPrompter{answers: []int{answerLeave}}
			return Decider{Prompter: p}, p
		}, DepOff, ""},
		{"no answer", func() (Decider, *scriptedPrompter) {
			p := &scriptedPrompter{answers: []int{answerNone}}
			return Decider{Prompter: p}, p
		}, DepPending, "not decided"},
		{"nobody to ask", func() (Decider, *scriptedPrompter) { return Decider{}, nil }, DepPending, "--yes"},
		{"--yes", func() (Decider, *scriptedPrompter) { return Decider{AssumeYes: true}, nil }, DepOn, "without asking (--yes)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, p := tc.decider()
			e := newEntry()
			var warned []string
			got, err := d.decide("components.genai[genai]", e, "discovery", warnings(&warned))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("state = %v, want %v", got, tc.want)
			}
			switch {
			case tc.warnWant == "" && len(warned) != 0:
				t.Errorf("warned %v, want nothing (the operator answered)", warned)
			case tc.warnWant != "" && !contains(warned, tc.warnWant):
				t.Errorf("warnings = %v, want one mentioning %q", warned, tc.warnWant)
			}
			if p != nil && (len(p.asked) != 1 || !strings.Contains(p.asked[0], "DISCOVERY_ENABLED")) {
				t.Errorf("asked %q, want one question carrying what the switch adds", p.asked)
			}
			// The answer sticks: the same proposal is never asked twice in a run.
			if again, _ := d.decide("components.genai[genai]", e, "discovery", warnings(&warned)); again != tc.want || (p != nil && len(p.asked) != 1) {
				t.Errorf("second decide = %v after %v question(s), want %v without asking again", again, len(p.asked), tc.want)
			}
		})
	}

	t.Run("a prompt failure is an error", func(t *testing.T) {
		boom := errors.New("terminal closed")
		_, err := Decider{Prompter: &scriptedPrompter{err: boom}}.decide("x", newEntry(), "discovery", func(string, ...any) {})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want the prompter's error", err)
		}
	})
}

func TestDecideEntry_AsksInKeyOrderAndWarnsAboutAbsentOnes(t *testing.T) {
	e := &Entry{
		Name: "rocket",
		DepGate: map[string]DepGate{
			"virtualizer": {Name: "virtualizer"},
			"genai":       {Name: "genai"},
		},
		DepAbsent: map[string]bool{"opensearch": true},
	}
	p := &scriptedPrompter{answers: []int{answerEnable, answerLeave}}
	var warned []string
	if err := (Decider{Prompter: p}).decideEntry("components.rocket[rocket]", e, warnings(&warned)); err != nil {
		t.Fatal(err)
	}
	if len(p.asked) != 2 || !strings.Contains(p.asked[0], `"genai"`) || !strings.Contains(p.asked[1], `"virtualizer"`) {
		t.Errorf("questions = %q, want genai then virtualizer (sorted)", p.asked)
	}
	if e.DepState["genai"] != DepOn || e.DepState["virtualizer"] != DepOff {
		t.Errorf("DepState = %v", e.DepState)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], `"opensearch"`) || !strings.Contains(warned[0], "not running on the cluster") {
		t.Errorf("warnings = %q, want only the absent opensearch", warned)
	}
}

// Every component goes through the same code: no type is special.
func TestDecideAll_OneMechanismForEveryComponent(t *testing.T) {
	gated := func(keys ...string) catalog.Schema {
		var s catalog.Schema
		for _, k := range keys {
			s.Dependencies = append(s.Dependencies, catalog.Dependency{Key: k, Gated: true})
		}
		return s
	}
	cat := &catalog.Catalog{Schemas: map[string]catalog.Schema{
		"rocket":       gated("genai", "virtualizer"),
		"intelligence": gated("genai", "opensearch"),
		"genai":        gated("discovery"),
		"discovery":    gated("virtualizer"),
		"newcomponent": gated("brandnew"), // a component nothing in this package has heard of
	}}
	mk := func(name string, deps map[string]string) *Entry { return &Entry{Name: name, Deps: deps} }
	comps := Components{
		"rocket":       {mk("rocket", map[string]string{"genai": "genai", "virtualizer": "virtualizer"})},
		"intelligence": {mk("intelligence", map[string]string{"genai": "genai", "opensearch": "opensearch1"})},
		"genai":        {mk("genai", map[string]string{"discovery": "discovery"})},
		"discovery":    {mk("discovery", map[string]string{"virtualizer": "virtualizer"})},
		"newcomponent": {mk("newcomponent", map[string]string{"brandnew": "brandnew"})},
		"virtualizer":  {mk("virtualizer", nil)},
		"brandnew":     {mk("brandnew", nil)},
		// opensearch1 does not run: intelligence's opensearch is absent.
	}
	running := map[*Entry]bool{}
	for _, entries := range comps {
		for _, e := range entries {
			running[e] = true
		}
	}
	comps["opensearch"] = []*Entry{mk("opensearch1", nil)}

	markGates(cat, comps, running)
	p := &scriptedPrompter{}
	for i := 0; i < 20; i++ {
		p.answers = append(p.answers, answerEnable)
	}
	var warned []string
	if err := (Decider{Prompter: p}).decideAll(comps, warnings(&warned)); err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{
		"rocket":       {"genai", "virtualizer"},
		"intelligence": {"genai"},
		"genai":        {"discovery"},
		"discovery":    {"virtualizer"},
		"newcomponent": {"brandnew"},
	}
	for key, deps := range want {
		e := comps[key][0]
		var on []string
		for dep, st := range e.DepState {
			if st == DepOn {
				on = append(on, dep)
			}
		}
		slices.Sort(on)
		if !slices.Equal(on, deps) {
			t.Errorf("%s: enabled %v, want %v", key, on, deps)
		}
	}
	if !contains(warned, `components.intelligence[intelligence]: dependency "opensearch" is not running`) {
		t.Errorf("warnings = %q, want intelligence's opensearch reported as not running", warned)
	}
	if len(p.asked) != 6 {
		t.Errorf("asked %d question(s), want 6 (every proposal, whatever the component)", len(p.asked))
	}
}

func TestDepConfig_WritesTheAnswer(t *testing.T) {
	e := &Entry{DepState: map[string]DepState{"on": DepOn, "off": DepOff, "pending": DepPending}}
	for dep, want := range map[string]any{"on": true, "off": false} {
		if got := e.depConfig(dep, "x")["enabled"]; got != want {
			t.Errorf("%s: enabled = %v, want %v", dep, got, want)
		}
	}
	if _, ok := e.depConfig("pending", "x")["enabled"]; ok {
		t.Error("a pending proposal wrote `enabled`")
	}
	if _, ok := e.depConfig("none", "x")["enabled"]; ok {
		t.Error("a dependency with no proposal wrote `enabled`")
	}
	if _, ok := e.depConfig("on", "")["enabled"]; ok {
		t.Error("an unresolved name wrote `enabled`")
	}
}

func TestRun_AsksAndWritesTheAnswer(t *testing.T) {
	run := func(t *testing.T, d Decider) (map[string]map[string]any, string) {
		t.Helper()
		c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
			pgCluster("psql", "stratio-datastores"),
			helmRelease("virtualizer", "stratio-apps", "virtualizer", nil),
		).Build()
		var logs bytes.Buffer
		out, err := Run(context.Background(), Options{
			TenantName: "stratio", Catalog: gatedCatalog(t, "virtualizer", nil, "postgres", "pgbouncer"),
			Client: c, Log: log.New(&logs, false), Decider: d,
		})
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(out, &doc); err != nil {
			t.Fatal(err)
		}
		comps := doc["spec"].(map[string]any)["defaultValues"].(map[string]any)["components"].(map[string]any)
		deps := comps["virtualizer"].([]any)[0].(map[string]any)["config"].(map[string]any)["dependencies"].(map[string]any)
		got := map[string]map[string]any{}
		for k, v := range deps {
			got[k] = v.(map[string]any)
		}
		return got, logs.String()
	}

	t.Run("enable", func(t *testing.T) {
		deps, _ := run(t, Decider{Prompter: &scriptedPrompter{answers: []int{answerEnable}}})
		if deps["postgres"]["enabled"] != true || deps["postgres"]["name"] != "psql" {
			t.Errorf("postgres = %v, want enabled: true, name: psql", deps["postgres"])
		}
	})
	t.Run("leave off is written, so it is not asked again", func(t *testing.T) {
		deps, _ := run(t, Decider{Prompter: &scriptedPrompter{answers: []int{answerLeave}}})
		if v, ok := deps["postgres"]["enabled"]; !ok || v != false {
			t.Errorf("postgres = %v, want an explicit enabled: false", deps["postgres"])
		}
	})
	t.Run("--yes", func(t *testing.T) {
		deps, logs := run(t, Decider{AssumeYes: true})
		if deps["postgres"]["enabled"] != true {
			t.Errorf("postgres = %v, want enabled: true", deps["postgres"])
		}
		if !strings.Contains(logs, "without asking (--yes)") {
			t.Errorf("log = %q, want each --yes enablement reported", logs)
		}
	})
	t.Run("nobody to ask writes nothing and says so", func(t *testing.T) {
		deps, logs := run(t, Decider{})
		if _, ok := deps["postgres"]["enabled"]; ok {
			t.Errorf("postgres = %v, want no enabled", deps["postgres"])
		}
		if !strings.Contains(logs, "not decided") || !strings.Contains(logs, "--yes") {
			t.Errorf("log = %q, want the pending proposal reported with the way to answer it", logs)
		}
	})
	t.Run("a dependency that does not run is reported, not asked", func(t *testing.T) {
		p := &scriptedPrompter{answers: []int{answerEnable, answerEnable}}
		deps, logs := run(t, Decider{Prompter: p})
		if _, ok := deps["pgbouncer"]["enabled"]; ok {
			t.Errorf("pgbouncer = %v, want no enabled", deps["pgbouncer"])
		}
		if len(p.asked) != 1 {
			t.Errorf("asked %d question(s), want only postgres' (pgbouncer is not on the cluster)", len(p.asked))
		}
		if !strings.Contains(logs, `"pgbouncer" is not running on the cluster`) {
			t.Errorf("log = %q, want pgbouncer reported as not running", logs)
		}
	})
}

const depsTenant = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  type: Static
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
      - name: virtualizer
        config:
          dependencies:
            postgres:
              name: psql
            connectors:
              name: connectors
          uiEnabled: false
`

// completeGated runs Complete against the fixture catalog with virtualizer's
// postgres gated, for a scanned virtualizer entry that has a proposal for it.
func completeGated(t *testing.T, src string, scanned Components, opts CompleteOptions) (Report, string) {
	t.Helper()
	if opts.TenantName == "" {
		opts.TenantName = "stratio"
	}
	doc := reload(t, []byte(src))
	r, err := Complete(doc, scanned, Live{}, gatedCatalog(t, "virtualizer", map[string][]string{"postgres": {"sets PG_ENABLED"}}, "postgres"), opts)
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

func proposed() Components {
	return Components{"virtualizer": {{Name: "virtualizer", DepGate: map[string]DepGate{"postgres": {Name: "psql", Adds: []string{"sets PG_ENABLED"}}}}}}
}

func TestComplete_AsksAboutADeclaredDependencyAndWritesTheAnswer(t *testing.T) {
	p := &scriptedPrompter{answers: []int{answerEnable}}
	r, out := completeGated(t, depsTenant, proposed(), CompleteOptions{Decider: Decider{Prompter: p}})
	want := strings.Replace(depsTenant, "              name: psql\n", "              name: psql\n              enabled: true\n", 1)
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	if !contains(r.Added, "config.dependencies.postgres.enabled: true") {
		t.Errorf("Added = %v", r.Added)
	}
	if len(p.asked) != 1 || !strings.Contains(p.asked[0], "PG_ENABLED") {
		t.Errorf("asked %q, want one question carrying what the switch adds", p.asked)
	}

	// The answer is in the file now: a second run asks nothing and changes nothing.
	r2, out2 := completeGated(t, out, proposed(), CompleteOptions{Decider: Decider{Prompter: failingPrompter{t}}})
	if out2 != out || len(r2.Added) != 0 {
		t.Errorf("second run added %v and changed the file:\n%s", r2.Added, out2)
	}
}

func TestComplete_LeavingItOffIsWrittenAndNotAskedAgain(t *testing.T) {
	_, out := completeGated(t, depsTenant, proposed(), CompleteOptions{Decider: Decider{Prompter: &scriptedPrompter{answers: []int{answerLeave}}}})
	if !strings.Contains(out, "              name: psql\n              enabled: false\n") {
		t.Fatalf("enabled: false not written:\n%s", out)
	}
	r, out2 := completeGated(t, out, proposed(), CompleteOptions{Decider: Decider{Prompter: failingPrompter{t}}})
	if out2 != out || len(r.Added) != 0 {
		t.Errorf("a declined proposal was asked again or the file changed: %v\n%s", r.Added, out2)
	}
}

func TestComplete_AHandWrittenValueIsNeverAskedOrChanged(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		src := strings.Replace(depsTenant, "              name: psql\n", "              name: psql\n              enabled: "+value+"\n", 1)
		r, out := completeGated(t, src, proposed(), CompleteOptions{Decider: Decider{Prompter: failingPrompter{t}, AssumeYes: false}})
		if out != src || len(r.Added) != 0 {
			t.Errorf("enabled: %s was touched: added %v\n%s", value, r.Added, out)
		}
	}
	// --yes does not overwrite it either.
	src := strings.Replace(depsTenant, "              name: psql\n", "              name: psql\n              enabled: false\n", 1)
	if _, out := completeGated(t, src, proposed(), CompleteOptions{Decider: Decider{AssumeYes: true}}); out != src {
		t.Errorf("--yes overwrote a hand-written false:\n%s", out)
	}
}

func TestComplete_NoAnswerWritesNothingAndReports(t *testing.T) {
	r, out := completeGated(t, depsTenant, proposed(), CompleteOptions{})
	if out != depsTenant || len(r.Added) != 0 {
		t.Errorf("an unanswered proposal changed the file: %v\n%s", r.Added, out)
	}
	if !contains(r.Warnings, `dependency "postgres" (psql) runs on the cluster`) || !contains(r.Warnings, "--yes") {
		t.Errorf("Warnings = %v, want the pending proposal reported", r.Warnings)
	}
}

func TestComplete_ADependencyThatDoesNotRunIsReportedNotAsked(t *testing.T) {
	scanned := Components{"virtualizer": {{Name: "virtualizer", DepAbsent: map[string]bool{"postgres": true}}}}
	r, out := completeGated(t, depsTenant, scanned, CompleteOptions{Decider: Decider{Prompter: failingPrompter{t}}})
	if out != depsTenant {
		t.Errorf("file changed:\n%s", out)
	}
	if !contains(r.Warnings, `dependency "postgres" is not running on the cluster`) {
		t.Errorf("Warnings = %v, want the absent dependency reported", r.Warnings)
	}
}

func TestComplete_FlowStyleDependencyWarnsInsteadOfEditing(t *testing.T) {
	src := strings.Replace(depsTenant, "            postgres:\n              name: psql\n", "            postgres: {name: psql}\n", 1)
	r, out := completeGated(t, src, proposed(), CompleteOptions{Decider: Decider{AssumeYes: true}})
	if out != src {
		t.Errorf("file changed:\n%s", out)
	}
	if !contains(r.Warnings, "not a block mapping") || contains(r.Added, "postgres.enabled") {
		t.Errorf("Warnings = %v, Added = %v, want a hand-edit warning and no addition", r.Warnings, r.Added)
	}
}

func TestComplete_ANewDependencyIsAddedWithTheAnswerInOneGo(t *testing.T) {
	src := strings.Replace(depsTenant, "            postgres:\n              name: psql\n", "", 1)
	for _, tc := range []struct {
		name    string
		answer  int
		want    string
		added   string
		wantSub string
	}{
		{"enable", answerEnable, "            postgres:\n              enabled: true\n              name: psql\n", "postgres.name: psql, enabled: true", ""},
		{"leave off", answerLeave, "            postgres:\n              enabled: false\n              name: psql\n", "postgres.name: psql, enabled: false (left off)", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scanned := proposed()
			scanned["postgres"] = []*Entry{{Name: "psql"}}
			r, out := completeGated(t, src, scanned, CompleteOptions{Decider: Decider{Prompter: &scriptedPrompter{answers: []int{tc.answer}}}})
			if !strings.Contains(out, tc.want) {
				t.Errorf("postgres not added as expected:\n%s", out)
			}
			if !contains(r.Added, tc.added) {
				t.Errorf("Added = %v, want %q", r.Added, tc.added)
			}
		})
	}
}

func TestComplete_ANewEntryAsksAboutItsDependencies(t *testing.T) {
	// virtualizer is not in the file at all: the scan found it live and it is appended.
	src := strings.Replace(depsTenant, depsTenant[strings.Index(depsTenant, "      virtualizer:"):], "      other: []\n", 1)
	scanned := proposed()
	scanned["virtualizer"][0].Deps = map[string]string{"postgres": "psql"}
	p := &scriptedPrompter{answers: []int{answerEnable}}
	_, out := completeGated(t, src, scanned, CompleteOptions{Decider: Decider{Prompter: p}})
	if !strings.Contains(out, "enabled: true") || len(p.asked) != 1 {
		t.Errorf("a new entry was not asked about its gated dependency (asked %q):\n%s", p.asked, out)
	}
}
