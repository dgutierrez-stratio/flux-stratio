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
	// Config is the migration catalog: it classifies the live workloads into
	// components (nil uses the seed's), and is read for Chart.EnabledBy to
	// derive config fields from the live siblings (virtualizer's uiEnabled).
	Config *config.Catalog
	Client client.Client
	Log    *log.Logger
	// Decider answers the proposals to enable a gated dependency that runs on
	// the cluster (create mode, Run). The zero value asks nobody: each stays
	// pending and is reported. Complete takes its own, in CompleteOptions.
	Decider Decider
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
	var scanWarnings []string
	classifyWarn := func(format string, a ...any) {
		warn(format, a...)
		scanWarnings = append(scanWarnings, fmt.Sprintf(format, a...))
	}
	if err := scanClassified(ctx, opts.Client, opts.classifyConfig(), opts.Catalog, opts.TenantName, components, classifyWarn); err != nil {
		return nil, Live{}, fmt.Errorf("scanning workloads: %w", err)
	}
	if err := scanHelmReleases(ctx, opts.Client, opts.Catalog, opts.TenantName, components); err != nil {
		return nil, Live{}, fmt.Errorf("scanning helmreleases: %w", err)
	}
	running := map[*Entry]bool{}
	for _, entries := range components {
		for _, e := range entries {
			running[e] = true
		}
	}
	live, err := scanEnabledBy(ctx, opts.Client, opts.Config, opts.TenantName)
	if err != nil {
		return nil, Live{}, fmt.Errorf("scanning sibling workloads: %w", err)
	}
	live.Warnings = append(live.Warnings, scanWarnings...)
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
	markGates(opts.Catalog, components, running)
	return components, live, nil
}

// classifyConfig is the catalog the live objects are classified against: the
// configured one, or the seed's when none was given.
func (o Options) classifyConfig() *config.Catalog {
	if o.Config != nil {
		return o.Config
	}
	seed := config.SeedCatalog()
	return &seed
}

func (o Options) warn(format string, a ...any) {
	if o.Log != nil {
		o.Log.Warningf(format, a...)
	}
}

// Run scans a live, not-yet-migrated cluster for TenantName's components
// and renders a ResourceSetInputProvider skeleton (Create mode).
func Run(ctx context.Context, opts Options) ([]byte, error) {
	components, _, err := Scan(ctx, opts, true)
	if err != nil {
		return nil, err
	}
	if err := opts.Decider.decideAll(components, opts.warn); err != nil {
		return nil, err
	}
	size := opts.Size
	if size == "" {
		size = "S"
	}
	return Render(opts.TenantName, size, components)
}
