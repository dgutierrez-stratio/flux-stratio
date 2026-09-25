// Package appmigrate migrates one application: it diffs the app (via
// internal/appdiff, the same computation `apps diff` uses, so the two
// commands can never disagree about what a migration would do) and, if
// there is a difference, splices the resulting patch into the tenant's
// ResourceSetInputProvider (via internal/tenantfile).
//
// The pipeline described in the project plan is prepare -> render -> diff
// -> patch -> splice; this package implements everything but the prepare
// stage, which internal/prepare adds from Phase 8 onward.
package appmigrate

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/appdiff"
	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

// Options configures migrating one app.
type Options struct {
	Base, Cluster, Tenant string
	// ChartsBase, if set, overrides Base for resolving a chart-mode app's
	// on-disk chart directory (see config.Config.ChartsBase).
	ChartsBase string
	App        config.App
	Catalog    *catalog.Catalog
	Runner     runner.Runner
	Client     client.Client
	Log        *log.Logger
}

// Result reports what a migrate run found and (for Apply) did.
type Result struct {
	// Migrated is true if a patch was found — for Plan, this means Apply
	// would write something; for Apply, that it did.
	Migrated bool
	// Before and After are the tenant file's content before and after the
	// edit, for a diff preview (apps migrate --dry-run) via
	// internal/ui.FileDiff. Equal when Migrated is false.
	Before, After string
}

// Plan computes what migrating opts.App would do, without writing
// anything — the basis for apps migrate --dry-run and for previewing a
// change before an interactive confirmation.
func Plan(ctx context.Context, opts Options) (*Result, error) {
	result, _, _, err := plan(ctx, opts)
	return result, err
}

// Apply computes the same plan as Plan and, if there is a difference,
// writes it to the tenant file.
func Apply(ctx context.Context, opts Options) (*Result, error) {
	result, doc, tenantPath, err := plan(ctx, opts)
	if err != nil {
		return nil, err
	}
	if !result.Migrated {
		return result, nil
	}
	if err := doc.Save(tenantPath); err != nil {
		return nil, err
	}
	return result, nil
}

func plan(ctx context.Context, opts Options) (*Result, *tenantfile.Doc, string, error) {
	tenantPath := tenantfile.Path(opts.Base, opts.Cluster, opts.Tenant)
	doc, err := tenantfile.Load(tenantPath)
	if err != nil {
		return nil, nil, "", err
	}
	before, err := doc.Bytes()
	if err != nil {
		return nil, nil, "", err
	}

	diffResult, err := appdiff.Diff(ctx, appdiff.Options{
		Base: opts.Base, Cluster: opts.Cluster, Tenant: opts.Tenant, ChartsBase: opts.ChartsBase,
		App: opts.App, Runner: opts.Runner, Client: opts.Client, Log: opts.Log,
	})
	if err != nil {
		return nil, nil, "", err
	}
	if diffResult.Patch == nil {
		return &Result{Migrated: false, Before: string(before), After: string(before)}, doc, tenantPath, nil
	}

	if err := tenantfile.Splice(doc, opts.Catalog, opts.App, *diffResult.Patch); err != nil {
		return nil, nil, "", err
	}
	after, err := doc.Bytes()
	if err != nil {
		return nil, nil, "", err
	}
	return &Result{Migrated: true, Before: string(before), After: string(after)}, doc, tenantPath, nil
}
