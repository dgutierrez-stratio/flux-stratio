package discovery

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/log"
)

func obj(apiVersion, kind, namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}}
}

func TestScan_FindsEachKind(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "apps-psql"),
		obj("helm.toolkit.fluxcd.io/v2", "HelmRelease", "stratio-datastores", "psql-gosec-agent"),
		obj("apps/v1", "Deployment", "stratio-apps", "governance-datamarket-agent"),
		obj("postgres.stratio.com/v1", "PgCluster", "stratio-datastores", "psql"),
	).Build()

	idx, err := Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}

	if _, ok := idx.FindKustomization("apps-psql", logger); !ok {
		t.Error(`FindKustomization("apps-psql") = not found`)
	}
	if _, ok := idx.FindHelmRelease("psql-gosec-agent", logger); !ok {
		t.Error(`FindHelmRelease("psql-gosec-agent") = not found`)
	}
	if _, ok := idx.FindWorkload("governance-datamarket-agent", logger); !ok {
		t.Error(`FindWorkload("governance-datamarket-agent") = not found`)
	}
	if _, ok := idx.FindCR("psql", logger); !ok {
		t.Error(`FindCR("psql") = not found`)
	}

	// Cross-bucket: a name present as one kind is absent from the others.
	if _, ok := idx.FindCR("apps-psql", logger); ok {
		t.Error(`FindCR("apps-psql") = found, want not found (it's a Kustomization, not a CR)`)
	}
}

func TestScan_NoNamespaceFilter(t *testing.T) {
	// Python's discovery.py never filters by namespace — an object in any
	// namespace at all must be found.
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("postgres.stratio.com/v1", "PgCluster", "some-legacy-namespace-nobody-expects", "psql"),
	).Build()

	idx, err := Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	live, ok := idx.FindCR("psql", logger)
	if !ok {
		t.Fatal(`FindCR("psql") = not found`)
	}
	if live.GetNamespace() != "some-legacy-namespace-nobody-expects" {
		t.Errorf("namespace = %q", live.GetNamespace())
	}
}

func TestFind_AmbiguousNameWarns(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("postgres.stratio.com/v1", "PgCluster", "ns-a", "dup"),
		obj("postgres.stratio.com/v1", "PgCluster", "ns-b", "dup"),
	).Build()

	idx, err := Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if _, ok := idx.FindCR("dup", logger); !ok {
		t.Fatal(`FindCR("dup") = not found`)
	}
	if !strings.Contains(buf.String(), "more than one live object named") {
		t.Errorf("expected an ambiguity warning, got log output: %s", buf.String())
	}
}

// TestFind_AmbiguousNameDeterministicallyPicksLowestNamespace pins the
// disambiguation itself: the pick must be the alphabetically-first
// namespace regardless of which order the objects were created/listed
// in, not whatever order the fake (or a real) List call happens to
// return — sort ordering must be doing the work, not incidental list
// order, otherwise this would be flaky against a real cluster.
func TestFind_AmbiguousNameDeterministicallyPicksLowestNamespace(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("postgres.stratio.com/v1", "PgCluster", "zzz-tenant", "dup"),
		obj("postgres.stratio.com/v1", "PgCluster", "aaa-tenant", "dup"),
	).Build()

	idx, err := Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	live, ok := idx.FindCR("dup", logger)
	if !ok {
		t.Fatal(`FindCR("dup") = not found`)
	}
	if live.GetNamespace() != "aaa-tenant" {
		t.Errorf("namespace = %q, want the deterministic (alphabetically first) match %q", live.GetNamespace(), "aaa-tenant")
	}
}

func TestFind_NotFound(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	idx, err := Scan(context.Background(), fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).Build(), logger)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if _, ok := idx.FindCR("nonexistent", logger); ok {
		t.Error(`FindCR("nonexistent") = found, want not found`)
	}
}

func TestTolerable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "kind not installed",
			err:  &apimeta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "hdfs.stratio.com", Kind: "HDFSCluster"}, SearchedVersions: []string{"v1"}},
			want: true,
		},
		{
			name: "forbidden",
			err:  apierrors.NewForbidden(schema.GroupResource{Group: "postgres.stratio.com", Resource: "pgclusters"}, "", fmt.Errorf("denied")),
			want: true,
		},
		{
			name: "anything else",
			err:  fmt.Errorf("connection refused"),
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := tolerable(c.err); got != c.want {
				t.Errorf("tolerable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestIndex_Names_UnionAcrossBucketsSorted(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "apps-psql"),
		obj("helm.toolkit.fluxcd.io/v2", "HelmRelease", "stratio-datastores", "psql-gosec-agent"),
		obj("apps/v1", "Deployment", "stratio-apps", "governance-datamarket-agent"),
		obj("postgres.stratio.com/v1", "PgCluster", "stratio-datastores", "psql"),
		// A name shared across two kinds must appear only once.
		obj("apps/v1", "Deployment", "stratio-datastores", "psql"),
	).Build()

	idx, err := Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}

	got := idx.Names()
	want := []string{"apps-psql", "governance-datamarket-agent", "psql", "psql-gosec-agent"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q (want sorted, deduplicated)", i, got[i], want[i])
		}
	}
}

func TestScan_ExtraKindsIndexedAndDeduplicated(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("apps/v1", "Deployment", "stratio-genai", "genai-api"),
		obj("example.stratio.com/v1", "Widget", "stratio-apps", "w1"),
	).Build()

	extra := []schema.GroupVersionKind{
		{Group: "apps", Version: "v1", Kind: "Deployment"}, // already a workload kind: listed once
		{Group: "example.stratio.com", Version: "v1", Kind: "Widget"},
	}
	idx, err := Scan(context.Background(), c, logger, extra...)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}

	objs := idx.Objects()
	var got []string
	for _, o := range objs {
		got = append(got, o.GetKind()+"/"+o.GetName())
	}
	if strings.Join(got, ",") != "Deployment/genai-api,Widget/w1" {
		t.Errorf("Objects() = %v, want each object exactly once, sorted by kind", got)
	}
}

func TestIndex_GetMatchesKindAndNamespace(t *testing.T) {
	logger := log.New(&bytes.Buffer{}, false)
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(
		obj("apps/v1", "Deployment", "stratio-genai", "genai"),
		obj("postgres.stratio.com/v1", "PgDatabase", "stratio-datastores", "genai"),
	).Build()
	idx, err := Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	deploy := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	if got, ok := idx.Get(deploy, "stratio-genai", "genai"); !ok || got.GetKind() != "Deployment" {
		t.Errorf("Get(Deployment stratio-genai/genai) = %v, %v", got, ok)
	}
	if _, ok := idx.Get(deploy, "stratio-datastores", "genai"); ok {
		t.Error("Get matched the wrong namespace")
	}
	pgdb := schema.GroupVersionKind{Group: "postgres.stratio.com", Version: "v1", Kind: "PgDatabase"}
	if got, ok := idx.Get(pgdb, "stratio-datastores", "genai"); !ok || got.GetKind() != "PgDatabase" {
		t.Errorf("Get(PgDatabase) = %v, %v", got, ok)
	}
}
