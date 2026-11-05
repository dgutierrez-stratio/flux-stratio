package render

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

// buildKustomization runs `flux-operator build rset` and selects the
// Kustomization named opts.Kustomization from its output.
func buildKustomization(ctx context.Context, opts Options) (*unstructured.Unstructured, error) {
	stdout, stderr, err := opts.Runner.Run(ctx, "flux-operator", "build", "rset",
		"-f", rsetPath(opts),
		"--inputs-from-provider", tenantFilePath(opts),
	)
	if err != nil {
		return nil, fmt.Errorf("flux-operator build rset: %w (stderr: %s)", err, string(stderr))
	}

	docs, err := yamldocs.Decode(stdout)
	if err != nil {
		return nil, fmt.Errorf("parsing flux-operator build rset output: %w", err)
	}

	kustomizations := yamldocs.FindByKind(docs, "Kustomization")
	if len(kustomizations) == 0 {
		return nil, fmt.Errorf("flux-operator build rset produced no Kustomization objects for rset %q", opts.Rset)
	}

	ks := yamldocs.FindByName(kustomizations, opts.Kustomization)
	if ks == nil {
		available := make([]string, 0, len(kustomizations))
		for _, k := range kustomizations {
			available = append(available, k.GetName())
		}
		return nil, fmt.Errorf(
			"kustomization %q not found among the %d rendered by rset %q; available: %v "+
				"(if this app's component is missing or commented out under `components:` in %s, add or uncomment it there and re-run)",
			opts.Kustomization, len(kustomizations), opts.Rset, available, tenantFilePath(opts))
	}
	return ks, nil
}
