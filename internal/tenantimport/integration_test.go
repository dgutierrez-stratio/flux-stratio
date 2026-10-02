package tenantimport

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/catalog"
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
