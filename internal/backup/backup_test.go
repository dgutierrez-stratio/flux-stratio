package backup

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/discovery"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/reporequire"
)

var fixedClock = func() time.Time { return time.Date(2026, 3, 4, 10, 30, 0, 0, time.UTC) }

func fixtureBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	for _, dir := range reporequire.Dirs {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

func mustScheme(t *testing.T) *apiruntime.Scheme {
	t.Helper()
	s := apiruntime.NewScheme()
	for _, add := range []func(*apiruntime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func obj(apiVersion, kind, namespace, name string, extra map[string]any) *unstructured.Unstructured {
	m := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}
	for k, v := range extra {
		m[k] = v
	}
	return &unstructured.Unstructured{Object: m}
}

func scan(t *testing.T, l *log.Logger, objs ...*unstructured.Unstructured) *discovery.Index {
	t.Helper()
	anys := make([]client.Object, len(objs))
	for i, o := range objs {
		anys[i] = o
	}
	c := fake.NewClientBuilder().WithScheme(apiruntime.NewScheme()).WithObjects(anys...).Build()
	idx, err := discovery.Scan(context.Background(), c, l)
	if err != nil {
		t.Fatalf("discovery.Scan returned error: %v", err)
	}
	return idx
}

func TestRun_ManifestMode_WritesCRYAML(t *testing.T) {
	base := fixtureBase(t)
	backupsDir := t.TempDir()
	logger := log.New(io.Discard, false)

	live := obj("postgres.stratio.com/v1", "PgCluster", "stratio-datastores", "psql", map[string]any{
		"spec": map[string]any{"instances": int64(3)},
	})

	opts := Options{
		Repos: config.ReposUnder(base),
		App:   config.App{ID: "psql", Object: "psql"},
		Index: scan(t, logger, live),
		Dir:   backupsDir,
		Clock: fixedClock,
		Log:   logger,
	}

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	wantDir := filepath.Join(backupsDir, "psql", "2026-03-04T10-30-00Z")
	if result.Dir != wantDir {
		t.Errorf("Dir = %q, want %q", result.Dir, wantDir)
	}
	if len(result.Files) != 1 || result.Files[0] != "cr.yaml" {
		t.Errorf("Files = %v, want [cr.yaml]", result.Files)
	}

	data, err := os.ReadFile(filepath.Join(wantDir, "cr.yaml"))
	if err != nil {
		t.Fatalf("cr.yaml not written: %v", err)
	}
	if !strings.Contains(string(data), "instances: 3") {
		t.Errorf("cr.yaml content = %s, want it to contain the live instances count", data)
	}
}

func TestRun_ManifestMode_LiveObjectNotFoundErrors(t *testing.T) {
	backupsDir := t.TempDir()
	logger := log.New(io.Discard, false)

	opts := Options{
		Repos: config.ReposUnder(fixtureBase(t)),
		App:   config.App{ID: "psql", Object: "psql"},
		Index: scan(t, logger),
		Dir:   backupsDir,
		Clock: fixedClock,
		Log:   logger,
	}

	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("Run with no live object present: got nil error, want non-nil")
	}
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("backupsDir has %d entries, want 0 (no directory should be created on failure)", len(entries))
	}
}

func TestRun_ManifestMode_NoTenantFileDeclarationNeeded(t *testing.T) {
	// The regression this whole redesign fixes: a live object is found
	// purely by cluster-wide discovery, with no rset/Kustomization/tenant
	// file ever consulted — App carries only ID/Object, nothing else.
	backupsDir := t.TempDir()
	logger := log.New(io.Discard, false)
	live := obj("hdfs.stratio.com/v1", "HDFSCluster", "stratio-datastores", "hdfs1", nil)

	opts := Options{
		Repos: config.ReposUnder(fixtureBase(t)),
		App:   config.App{ID: "hdfs1", Object: "hdfs1"},
		Index: scan(t, logger, live),
		Dir:   backupsDir,
		Clock: fixedClock,
		Log:   logger,
	}

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0] != "cr.yaml" {
		t.Errorf("Files = %v, want [cr.yaml]", result.Files)
	}
}

func TestRun_ManifestMode_FoundOnlyAsWorkloadWarnsAndCaptures(t *testing.T) {
	backupsDir := t.TempDir()
	var logbuf bytes.Buffer
	logger := log.New(&logbuf, false)
	live := deploymentWithEnv("psql", "stratio-datastores", "DEBUG")

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(live).Build()
	idx, err := discovery.Scan(context.Background(), c, logger)
	if err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Repos: config.ReposUnder(fixtureBase(t)),
		App: config.App{
			ID: "psql", Type: "postgres", Rset: "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-psql", Object: "psql",
		},
		Index: idx,
		Dir:   backupsDir,
		Clock: fixedClock,
		Log:   logger,
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	wantFiles := []string{"deployment.yaml", "env-vars.env", "env-vars.deployment.psql.env"}
	if !equalStrings(result.Files, wantFiles) {
		t.Errorf("Files = %v, want %v", result.Files, wantFiles)
	}
	if !strings.Contains(logbuf.String(), "manifest-mode but was only found live as a workload") {
		t.Errorf("expected a mismatch warning, got: %s", logbuf.String())
	}
}

func TestRun_KustomizationOnly_SkipsWithoutError(t *testing.T) {
	backupsDir := t.TempDir()
	var logbuf bytes.Buffer
	logger := log.New(&logbuf, false)
	live := obj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "psql", nil)

	opts := Options{
		Repos: config.ReposUnder(fixtureBase(t)),
		App:   config.App{ID: "psql", Object: "psql"},
		Index: scan(t, logger, live),
		Dir:   backupsDir,
		Clock: fixedClock,
		Log:   logger,
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v, want nil (Kustomization-only is a skip, not a failure)", err)
	}
	if result.Dir != "" || len(result.Files) != 0 {
		t.Errorf("Result = %+v, want empty", result)
	}
	if !strings.Contains(logbuf.String(), "nothing to back up") {
		t.Errorf("expected a skip warning, got: %s", logbuf.String())
	}
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("backupsDir has %d entries, want 0", len(entries))
	}
}

func TestRun_UsesRealClockByDefault(t *testing.T) {
	backupsDir := t.TempDir()
	logger := log.New(io.Discard, false)
	live := obj("postgres.stratio.com/v1", "PgCluster", "stratio-datastores", "psql", nil)

	opts := Options{
		Repos: config.ReposUnder(fixtureBase(t)),
		App:   config.App{ID: "psql", Object: "psql"},
		Index: scan(t, logger, live),
		Dir:   backupsDir,
		// Clock deliberately left nil.
		Log: logger,
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	nowYear := time.Now().UTC().Format("2006")
	if !strings.Contains(result.Dir, nowYear) {
		t.Errorf("Dir = %q, want it to contain the current year %q (default clock should be time.Now)", result.Dir, nowYear)
	}
}
