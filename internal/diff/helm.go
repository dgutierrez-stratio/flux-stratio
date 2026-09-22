package diff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

// HelmTemplate renders chartPath the way Flux's helm-controller would for
// this HelmRelease: hrValues (the rendered HelmRelease's own spec.values)
// becomes the release's values file, and `helm dependency build` runs
// first if the chart's charts/ subdirectory is missing.
func HelmTemplate(ctx context.Context, r runner.Runner, chartPath, releaseName, namespace string, hrValues map[string]any) ([]*unstructured.Unstructured, error) {
	if info, err := os.Stat(filepath.Join(chartPath, "charts")); err != nil || !info.IsDir() {
		if _, stderr, err := r.Run(ctx, "helm", "dependency", "build", chartPath); err != nil {
			return nil, fmt.Errorf("helm dependency build %s: %w (stderr: %s)", chartPath, err, string(stderr))
		}
	}

	valuesFile, err := writeTempValues(releaseName, hrValues)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(valuesFile) }()

	stdout, stderr, err := r.Run(ctx, "helm", "template", releaseName, chartPath,
		"-f", valuesFile,
		"-n", namespace,
	)
	if err != nil {
		return nil, fmt.Errorf("helm template %s: %w (stderr: %s)", releaseName, err, string(stderr))
	}

	docs, err := yamldocs.Decode(stdout)
	if err != nil {
		return nil, fmt.Errorf("parsing helm template output: %w", err)
	}
	return docs, nil
}

func writeTempValues(releaseName string, values map[string]any) (string, error) {
	data, err := yaml.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("marshaling values for %s: %w", releaseName, err)
	}
	f, err := os.CreateTemp("", "flux-stratio-"+releaseName+"-values-*.yaml")
	if err != nil {
		return "", fmt.Errorf("creating temp values file: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(data); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("writing temp values file: %w", err)
	}
	return f.Name(), nil
}
