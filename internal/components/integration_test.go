package components

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Stratio/flux-stratio/internal/catalog"
)

// realKeosUseCasesDir locates a keos-use-cases checkout: the sibling
// checkout internal/catalog's own integration test uses, or
// $FLUX_STRATIO_KEOS_USE_CASES (e.g. when this repo is a git worktree
// outside the usual --base layout). Skipped when neither exists.
func realKeosUseCasesDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("FLUX_STRATIO_KEOS_USE_CASES")
	if dir == "" {
		var err error
		if dir, err = filepath.Abs(filepath.Join("..", "..", "..", "keos-use-cases")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "apps", "components")); err != nil {
		t.Skipf("keos-use-cases checkout not found at %s (set FLUX_STRATIO_KEOS_USE_CASES): %v", dir, err)
	}
	return dir
}

// TestSeededCatalog_AgainstRealTemplates checks every seeded type against
// the real keos-use-cases templates: its component key must be declared,
// and every instance the real legacy fixture resolves to must name a
// Kustomization whose patch anchor the templates can resolve — and resolve
// to the anchor kind the type implies (nested for a gosec agent).
func TestSeededCatalog_AgainstRealTemplates(t *testing.T) {
	templates, err := catalog.Load(realKeosUseCasesDir(t))
	if err != nil {
		t.Fatalf("catalog.Load returned error: %v", err)
	}

	cat := seededCatalog()
	for _, typ := range cat.Types {
		if _, ok := templates.Schemas[typ.Component]; !ok {
			t.Errorf("%s: component %q isn't declared by any keos-use-cases template", typ.Type, typ.Component)
		}
	}

	opts := baseOptions(t)
	opts.Doc = nil
	apps, unresolved, err := ResolveAll(opts)
	if err != nil || len(unresolved) > 0 {
		t.Fatalf("ResolveAll returned error %v, unresolved %v", err, unresolved)
	}
	for _, app := range apps {
		anchor, err := templates.ResolveAnchor(app.Kustomization)
		if err != nil {
			t.Errorf("%s: kustomization %q: %v", app.ID, app.Kustomization, err)
			continue
		}
		nested := app.Type == "postgres-gosec-agent" || app.Type == "opensearch-gosec-agent"
		if nested != (anchor.Kind == catalog.AnchorNested) {
			t.Errorf("%s: kustomization %q resolved to anchor %s, want nested=%v", app.ID, app.Kustomization, anchor.Kind, nested)
		}
	}
}
