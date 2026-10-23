package tenantimport

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
)

func TestRun_RealCatalogWorstCase(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "keos-use-cases"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "apps", "components")); err != nil {
		t.Skipf("sibling keos-use-cases checkout not found: %v", err)
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).Build() // nothing discovered: worst case for expansion
	out, err := Run(context.Background(), Options{
		TenantName: "stratio",
		Catalog:    cat,
		Client:     c,
		Log:        log.New(io.Discard, false),
	})
	if err != nil {
		t.Fatalf("Run returned error against the real catalog: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("Run produced empty output")
	}
	t.Logf("generated %d bytes for %d schema keys", len(out), len(cat.Schemas))
}

// A seeded configFromEnv rule that names a config field or dependency its
// ResourceSet does not read would write something the render ignores. The
// checkout is FLUX_STRATIO_KEOS_USE_CASES, or the sibling one.
func TestSeed_ConfigFromEnvNamesWhatTheRealTemplatesRead(t *testing.T) {
	dir := os.Getenv("FLUX_STRATIO_KEOS_USE_CASES")
	if dir == "" {
		dir = filepath.Join("..", "..", "..", "keos-use-cases")
	}
	if _, err := os.Stat(filepath.Join(dir, "apps", "components")); err != nil {
		t.Skipf("keos-use-cases checkout not found at %s: %v", dir, err)
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	for _, typ := range config.SeedCatalog().Types {
		schema, ok := cat.Schemas[typ.Component]
		for _, rule := range typ.ConfigFromEnv {
			switch {
			case !ok:
				t.Errorf("type %q: component %q has no ResourceSet block", typ.Type, typ.Component)
			case rule.DependencyKey() != "":
				found := false
				for _, d := range schema.Dependencies {
					found = found || d.Key == rule.DependencyKey()
				}
				if !found {
					t.Errorf("type %q: %s is not a dependency of components.%s", typ.Type, rule.Key, typ.Component)
				}
			case rule.Field() != "":
				if !schemaReadsField(schema, rule.Field()) {
					t.Errorf("type %q: config field %q is not read by the %s ResourceSet", typ.Type, rule.Field(), typ.Component)
				}
			}
		}
	}
}

// Every dependency a ResourceSet switches on `enabled` must be one the import
// can decide: its component has to be something a legacy cluster can have
// (a catalog type produces it), or the proposal could never be made and the
// dependency would be reported as "not running" forever. A new `enabled` gate
// in keos-use-cases that this cannot meet fails here, not silently in a
// migration. The checkout is FLUX_STRATIO_KEOS_USE_CASES, or the sibling one.
func TestSeed_EveryGatedDependencyCanBeProposed(t *testing.T) {
	dir := os.Getenv("FLUX_STRATIO_KEOS_USE_CASES")
	if dir == "" {
		dir = filepath.Join("..", "..", "..", "keos-use-cases")
	}
	if _, err := os.Stat(filepath.Join(dir, "apps", "components")); err != nil {
		t.Skipf("keos-use-cases checkout not found at %s: %v", dir, err)
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	seed := config.SeedCatalog()

	gated := 0
	for key, schema := range cat.Schemas {
		for _, dep := range schema.Dependencies {
			if !dep.Gated {
				continue
			}
			gated++
			t.Logf("components.%s: dependency %q is gated; turning it on %s", key, dep.Key, addsText(dep.Adds))
			if _, known := cat.Schemas[dep.Key]; !known {
				t.Errorf("components.%s: gated dependency %q is not a component of any ResourceSet", key, dep.Key)
			}
			if !catalogProduces(&seed, dep.Key) {
				t.Errorf("components.%s: gated dependency %q has no catalog type producing it, so it could never be found running", key, dep.Key)
			}
			if !catalogProduces(&seed, key) {
				t.Errorf("components.%s has a gated dependency (%q) but no catalog type produces the component, so it would never be asked about", key, dep.Key)
			}
		}
	}
	if gated == 0 {
		t.Fatal("no gated dependency found in the real templates: the `\"enabled\"` read is no longer recognised")
	}
}

// The eosdev bug, with the real templates: genai and Discovery both run, and
// the import used to write genai's `discovery` with a name and no `enabled`, so
// the chart rendered DISCOVERY_DATABASE "" and the GenAI chat listed no domains.
// Now the operator is asked, the question carries what the real template says
// turning it on does, and the answer is written.
func TestRun_RealTemplates_GenaiIsAskedAboutDiscovery(t *testing.T) {
	dir := os.Getenv("FLUX_STRATIO_KEOS_USE_CASES")
	if dir == "" {
		dir = filepath.Join("..", "..", "..", "keos-use-cases")
	}
	if _, err := os.Stat(filepath.Join(dir, "apps", "components")); err != nil {
		t.Skipf("keos-use-cases checkout not found at %s: %v", dir, err)
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		helmRelease("genai", "stratio-genai", "genai", nil),
		helmRelease("discovery", "stratio-apps", "discovery", nil),
	).Build()

	p := &scriptedPrompter{}
	for i := 0; i < 20; i++ {
		p.answers = append(p.answers, answerEnable)
	}
	out, err := Run(context.Background(), Options{
		TenantName: "stratio", Catalog: cat, Client: c,
		Log: log.New(io.Discard, false), Decider: Decider{Prompter: p},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var doc struct {
		Spec struct {
			DefaultValues struct {
				Components map[string][]struct {
					Name   string `yaml:"name"`
					Config struct {
						Dependencies map[string]map[string]any `yaml:"dependencies"`
					} `yaml:"config"`
				} `yaml:"components"`
			} `yaml:"defaultValues"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	genai := doc.Spec.DefaultValues.Components["genai"]
	if len(genai) != 1 {
		t.Fatalf("genai entries = %+v, want the one from its HelmRelease", genai)
	}
	disc := genai[0].Config.Dependencies["discovery"]
	if disc["name"] != "discovery" || disc["enabled"] != true {
		t.Errorf("genai discovery = %v, want name: discovery, enabled: true", disc)
	}

	asked := false
	for _, q := range p.asked {
		if strings.Contains(q, `components.genai[genai]: dependency "discovery"`) {
			asked = true
			if !strings.Contains(q, "DISCOVERY_ENABLED") {
				t.Errorf("the question does not say what turning it on sets: %q", q)
			}
		}
		if strings.Contains(q, `components.genai[genai]: dependency "opensearch"`) {
			t.Errorf("asked about opensearch, which does not run: %q", q)
		}
	}
	if !asked {
		t.Errorf("genai was never asked about discovery; questions: %q", p.asked)
	}
}
