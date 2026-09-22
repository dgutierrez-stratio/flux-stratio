package tenantimport

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/log"
)

// Options configures a tenant import run.
type Options struct {
	TenantName string
	// Size is the default component size (S, M or L); defaults to S.
	Size    string
	Catalog *catalog.Catalog
	Client  client.Client
	Log     *log.Logger
}

// Run scans a live, not-yet-migrated cluster for TenantName's components
// and renders a ResourceSetInputProvider skeleton.
func Run(ctx context.Context, opts Options) ([]byte, error) {
	if opts.TenantName == "" {
		return nil, fmt.Errorf("tenant name is required")
	}
	warn := func(format string, a ...any) {
		if opts.Log != nil {
			opts.Log.Warningf(format, a...)
		}
	}

	components := scanCRDs(ctx, opts.Client, opts.Catalog, opts.TenantName)
	if err := scanDeployments(ctx, opts.Client, opts.Catalog, opts.TenantName, components); err != nil {
		return nil, fmt.Errorf("scanning deployments: %w", err)
	}
	if err := scanHelmReleases(ctx, opts.Client, opts.Catalog, opts.TenantName, components); err != nil {
		return nil, fmt.Errorf("scanning helmreleases: %w", err)
	}
	expandMandatoryComponents(opts.Catalog, components, warn)
	enrich(opts.Catalog, components, warn)

	size := opts.Size
	if size == "" {
		size = "S"
	}
	return Render(opts.TenantName, size, components)
}
