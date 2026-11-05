package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

// buildKustomizationObjects writes ks to a temp file and runs `flux build
// kustomization --dry-run` against it, returning every object it renders.
func buildKustomizationObjects(ctx context.Context, opts Options, ks *unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	specPath, found, err := unstructured.NestedString(ks.Object, "spec", "path")
	if err != nil {
		return nil, fmt.Errorf("reading kustomization %q spec.path: %w", ks.GetName(), err)
	}
	if !found || specPath == "" {
		return nil, fmt.Errorf("kustomization %q has no spec.path", ks.GetName())
	}
	fullPath := filepath.Join(opts.Repos.Apps, specPath)
	if info, err := os.Stat(fullPath); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("kustomization %q's spec.path %q does not exist under keos-apps (%s)", ks.GetName(), specPath, fullPath)
	}

	tmpFile, err := writeTempKustomization(ks)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmpFile) }()

	stdout, stderr, err := opts.Runner.Run(ctx, "flux", "build", "kustomization", ks.GetName(),
		"-n", ks.GetNamespace(),
		"--path", fullPath,
		"--kustomization-file", tmpFile,
		"--dry-run",
	)
	if err != nil {
		return nil, fmt.Errorf("flux build kustomization %q: %w (stderr: %s)", ks.GetName(), err, string(stderr))
	}

	docs, err := yamldocs.Decode(stdout)
	if err != nil {
		return nil, fmt.Errorf("parsing flux build kustomization output: %w", err)
	}
	return docs, nil
}

// writeTempKustomization marshals ks to a fresh, exclusively-created temp
// file. Unlike the Python client, which wrote to a predictable path under
// os.TempDir() and never removed it (flux_renderer.py:206-208 — a
// symlink/collision hazard as well as a leak), the caller here always
// removes the file it gets back.
func writeTempKustomization(ks *unstructured.Unstructured) (string, error) {
	data, err := yaml.Marshal(ks.Object)
	if err != nil {
		return "", fmt.Errorf("marshaling kustomization %q: %w", ks.GetName(), err)
	}
	f, err := os.CreateTemp("", "flux-stratio-"+ks.GetName()+"-*.yaml")
	if err != nil {
		return "", fmt.Errorf("creating temp kustomization file: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(data); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("writing temp kustomization file: %w", err)
	}
	return f.Name(), nil
}
