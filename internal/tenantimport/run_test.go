package tenantimport

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/Stratio/flux-stratio/internal/log"
)

func TestRun_EndToEnd(t *testing.T) {
	cat := loadFixtureCatalog(t)
	// A realistic pre-migration snapshot: a PgCluster, a PGBouncer
	// depending on it (via its own spec), and a Deployment for
	// connectors — pgbackuprepository is deliberately left undiscovered,
	// so the mandatory-expansion + enrichment path is exercised too.
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		pgCluster("psql", "stratio-datastores"),
		pgBouncer("pool-psql", "stratio-datastores", "psql"),
		deployment("connectors", "stratio-apps"),
	).Build()

	out, err := Run(context.Background(), Options{
		TenantName: "stratio",
		Size:       "M",
		Catalog:    cat,
		Client:     c,
		Log:        log.New(io.Discard, false),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("Run produced invalid YAML: %v\n%s", err, out)
	}

	meta := doc["metadata"].(map[string]any)
	labels := meta["labels"].(map[string]any)
	if labels["keos.stratio.com/resourceset-type"] != "tenant-config" {
		t.Errorf("labels = %+v, want the correct tenant-config label", labels)
	}

	dv := doc["spec"].(map[string]any)["defaultValues"].(map[string]any)
	if dv["size"] != "M" {
		t.Errorf("size = %v, want M", dv["size"])
	}
	comps := dv["components"].(map[string]any)

	for _, key := range []string{"postgres", "pgbouncer", "connectors", "pgbackuprepository"} {
		if _, ok := comps[key]; !ok {
			t.Errorf("components missing %q; got keys %v", key, keysOf(comps))
		}
	}

	pgbouncerEntry := comps["pgbouncer"].([]any)[0].(map[string]any)
	pgbouncerDeps := pgbouncerEntry["config"].(map[string]any)["dependencies"].(map[string]any)
	if pgbouncerDeps["postgres"].(map[string]any)["name"] != "psql" {
		t.Errorf("pgbouncer.dependencies.postgres.name = %v, want psql", pgbouncerDeps["postgres"])
	}

	// pgbackuprepository was never discovered live: it must appear as a
	// skeleton with its schema-derived kebab-case name.
	pgbackuprepoEntries := comps["pgbackuprepository"].([]any)
	if len(pgbackuprepoEntries) != 1 || pgbackuprepoEntries[0].(map[string]any)["name"] != "pgbackuprepository" {
		t.Errorf("pgbackuprepository entries = %v", pgbackuprepoEntries)
	}
}

func TestRun_EmptyTenantNameErrors(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).Build()
	_, err := Run(context.Background(), Options{TenantName: "", Catalog: cat, Client: c})
	if err == nil {
		t.Fatal("Run with an empty tenant name: got nil error, want non-nil")
	}
}

func TestRun_DefaultsSizeToS(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).Build()
	out, err := Run(context.Background(), Options{TenantName: "stratio", Catalog: cat, Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "size: S") {
		t.Errorf("output missing default size: S; got:\n%s", out)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestRun_UnlistableKindFails: only an uninstalled CRD means "none here".
// One the client can't list (Forbidden, a timeout) fails the import —
// treated as absent, the real postgres would be replaced by a placeholder
// entry its dependents get wired to, in a file that looks valid.
func TestRun_UnlistableKindFails(t *testing.T) {
	for _, kind := range []string{"PgCluster", "HelmRelease"} {
		t.Run(kind, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
				pgCluster("psql", "stratio-datastores"),
				deployment("connectors", "stratio-apps"),
			).WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if u, ok := list.(*unstructured.UnstructuredList); ok && u.GetKind() == kind {
						return apierrors.NewForbidden(schema.GroupResource{Resource: kind}, "", errors.New("rbac"))
					}
					return c.List(ctx, list, opts...)
				},
			}).Build()

			out, err := Run(context.Background(), Options{
				TenantName: "stratio", Catalog: loadFixtureCatalog(t), Client: c, Log: log.New(io.Discard, false),
			})
			if err == nil || !apierrors.IsForbidden(err) {
				t.Errorf("err = %v, want the Forbidden list", err)
			}
			if out != nil {
				t.Errorf("Run produced a tenant file anyway:\n%s", out)
			}
		})
	}
}
