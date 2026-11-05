package diff

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stratio/flux-stratio/internal/runner"
)

const helmTemplateOutput = `
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: rendered-config
data:
  FOO: bar
`

func TestHelmTemplate_Success(t *testing.T) {
	chartPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(chartPath, "charts"), 0o755); err != nil {
		t.Fatal(err)
	}

	fake := &runner.Fake{Responses: map[string]runner.FakeResponse{
		"helm": {Stdout: []byte(helmTemplateOutput)},
	}}

	docs, err := HelmTemplate(context.Background(), fake, chartPath, "my-release", "my-ns", map[string]any{"a": "b"})
	if err != nil {
		t.Fatalf("HelmTemplate returned error: %v", err)
	}
	if len(docs) != 1 || docs[0].GetKind() != "ConfigMap" {
		t.Errorf("docs = %+v", docs)
	}

	// helm dependency build must NOT have been called: charts/ already exists.
	for _, c := range fake.Calls {
		if len(c.Args) > 0 && c.Args[0] == "dependency" {
			t.Error("helm dependency build was called even though charts/ already exists")
		}
	}
}

func TestHelmTemplate_RunsDependencyBuildWhenChartsDirMissing(t *testing.T) {
	chartPath := t.TempDir() // no charts/ subdirectory

	fake := &runner.Fake{Responses: map[string]runner.FakeResponse{
		"helm": {Stdout: []byte(helmTemplateOutput)},
	}}

	if _, err := HelmTemplate(context.Background(), fake, chartPath, "my-release", "my-ns", nil); err != nil {
		t.Fatalf("HelmTemplate returned error: %v", err)
	}

	var sawDependencyBuild bool
	for _, c := range fake.Calls {
		if len(c.Args) > 0 && c.Args[0] == "dependency" {
			sawDependencyBuild = true
		}
	}
	if !sawDependencyBuild {
		t.Error("expected helm dependency build to run when charts/ is missing")
	}
}

func TestHelmTemplate_DependencyBuildFailure(t *testing.T) {
	chartPath := t.TempDir()
	fake := &runner.Fake{} // "helm" unconfigured -> every call errors
	if _, err := HelmTemplate(context.Background(), fake, chartPath, "r", "ns", nil); err == nil {
		t.Fatal("HelmTemplate when helm dependency build fails: got nil error, want non-nil")
	}
}

func TestHelmTemplate_TemplateFailure(t *testing.T) {
	chartPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(chartPath, "charts"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &runner.Fake{} // "helm" unconfigured -> errors
	if _, err := HelmTemplate(context.Background(), fake, chartPath, "r", "ns", nil); err == nil {
		t.Fatal("HelmTemplate when helm template fails: got nil error, want non-nil")
	}
}

func TestWriteTempValues_CreatesAndCallerRemoves(t *testing.T) {
	path, err := writeTempValues("my-release", map[string]any{"foo": "bar"})
	if err != nil {
		t.Fatalf("writeTempValues returned error: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "foo: bar") {
		t.Errorf("temp values file content = %s", data)
	}
}
