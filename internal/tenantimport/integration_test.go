package tenantimport

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

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
