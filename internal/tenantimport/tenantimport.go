package tenantimport

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
)

// Options configures a tenant import run.
type Options struct {
	TenantName string
	// Size is the default component size (S, M or L); defaults to S.
	Size    string
	Catalog *catalog.Catalog
	// Config is the migration catalog, read for Chart.EnabledBy to derive
	// config fields from the live siblings (virtualizer's uiEnabled) and
	// Chart.Uses to derive dependencies' `enabled` from the live
	// HelmRelease values. Nil derives nothing.
	Config *config.Catalog
	Client client.Client
	Log    *log.Logger
}

// Scan is the cluster half of an import: the tenant's components with their
// dependencies resolved, plus what else the cluster says about them (Live).
// Create mode (Run) also adds skeleton entries for the components a
// discovered one requires but the scan did not find (expandSkeleton);
// Complete mode must not, since such a component may live in the file under
// another name or be commented out.
func Scan(ctx context.Context, opts Options, expandSkeleton bool) (Components, Live, error) {
	if opts.TenantName == "" {
		return nil, Live{}, fmt.Errorf("tenant name is required")
	}
	warn := opts.warn

	components, err := scanCRDs(ctx, opts.Client, opts.Catalog, opts.TenantName)
	if err != nil {
		return nil, Live{}, fmt.Errorf("scanning custom resources: %w", err)
	}
	if err := scanDeployments(ctx, opts.Client, opts.Catalog, opts.TenantName, components); err != nil {
		return nil, Live{}, fmt.Errorf("scanning deployments: %w", err)
	}
	if err := scanHelmReleases(ctx, opts.Client, opts.Catalog, opts.TenantName, components); err != nil {
		return nil, Live{}, fmt.Errorf("scanning helmreleases: %w", err)
	}
	scanUses(opts, components)
	live, err := scanEnabledBy(ctx, opts.Client, opts.Config, opts.TenantName)
	if err != nil {
		return nil, Live{}, fmt.Errorf("scanning sibling workloads: %w", err)
	}
	if expandSkeleton {
		expandMandatoryComponents(opts.Catalog, components, warn)
	}
	for key, values := range live.Values {
		for _, entry := range components[key] {
			if entry.ExtraConfig == nil {
				entry.ExtraConfig = map[string]any{}
			}
			for field, v := range values {
				entry.ExtraConfig[field] = v
			}
		}
	}
	enrich(opts.Catalog, components, requiredConfig(opts.Config), warn)
	return components, live, nil
}

func (o Options) warn(format string, a ...any) {
	if o.Log != nil {
		o.Log.Warningf(format, a...)
	}
}

// note reports something the run decided from the cluster, as opposed to a
// warning about something it could not decide.
func (o Options) note(format string, a ...any) {
	if o.Log != nil {
		o.Log.Generatef(format, a...)
	}
}

// Run scans a live, not-yet-migrated cluster for TenantName's components
// and renders a ResourceSetInputProvider skeleton (Create mode).
func Run(ctx context.Context, opts Options) ([]byte, error) {
	components, _, err := Scan(ctx, opts, true)
	if err != nil {
		return nil, err
	}
	size := opts.Size
	if size == "" {
		size = "S"
	}
	return Render(opts.TenantName, size, components)
}
