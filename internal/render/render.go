// Package render reproduces, for one application, exactly what Flux would
// reconcile onto the cluster: it runs `flux-operator build rset` against
// the tenant's ResourceSetInputProvider to render the app's Kustomization,
// resolves any postBuild.substituteFrom against the live cluster (which
// `flux build --dry-run` cannot reach itself), then runs `flux build
// kustomization --dry-run` to render that Kustomization's own objects and
// selects the one this app is diffed against.
//
// This mirrors the Python client's flux_renderer.py pipeline, with two
// differences: object selection is never interactive (the app's
// Kustomization and Object names always come from the config catalog, per
// design decision 6 in the project plan), and every subprocess call goes
// through internal/runner.Runner so this package's own tests need no
// binaries installed.
package render

import (
	"context"
	"fmt"
	"path/filepath"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/reporequire"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

// Options configures one Render call.
type Options struct {
	// Base is the parent directory holding keos-apps, keos-use-cases,
	// keos-fleet and keos-system-services as sibling checkouts.
	Base string
	// Cluster and Tenant locate the tenant's ResourceSetInputProvider
	// file: <Base>/keos-fleet/clusters/<Cluster>/tenants/config/<Tenant>.yaml.
	Cluster, Tenant string
	// Rset is the path, relative to keos-use-cases, of the ResourceSet
	// template that declares the app's Kustomization.
	Rset string
	// Kustomization is the exact rendered Kustomization name to select.
	Kustomization string
	// Object is the exact object name, inside that Kustomization, to
	// select and return.
	Object string

	Runner runner.Runner
	// Client is used only to resolve postBuild.substituteFrom against the
	// live cluster; a render whose selected Kustomization has no
	// substituteFrom never needs it.
	Client client.Client
	Log    *log.Logger
}

// Result is what a successful Render produces.
type Result struct {
	// Object is the selected object: the app's HelmRelease or custom
	// resource, as flux build kustomization --dry-run rendered it.
	Object *unstructured.Unstructured
	// Kustomization is the full rendered Kustomization object that
	// declared Object, substituteFrom already resolved.
	Kustomization *unstructured.Unstructured
	// AllDocs is every object flux build kustomization --dry-run rendered
	// for this Kustomization, Object among them.
	AllDocs []*unstructured.Unstructured
}

// Render runs the full two-stage pipeline described in the package doc and
// returns the app's desired-state object.
func Render(ctx context.Context, opts Options) (*Result, error) {
	if err := reporequire.Validate(opts.Base); err != nil {
		return nil, err
	}

	ks, err := buildKustomization(ctx, opts)
	if err != nil {
		return nil, err
	}

	ks, err = resolveSubstituteFrom(ctx, opts.Client, ks)
	if err != nil {
		return nil, fmt.Errorf("resolving postBuild.substituteFrom for kustomization %q: %w", opts.Kustomization, err)
	}

	docs, err := buildKustomizationObjects(ctx, opts, ks)
	if err != nil {
		return nil, err
	}

	obj := yamldocs.FindByName(docs, opts.Object)
	if obj == nil {
		return nil, fmt.Errorf(
			"object %q not found among the %d object(s) rendered by kustomization %q (rset %q)",
			opts.Object, len(docs), opts.Kustomization, opts.Rset,
		)
	}

	return &Result{Object: obj, Kustomization: ks, AllDocs: docs}, nil
}

func tenantFilePath(opts Options) string {
	return tenantfile.Path(opts.Base, opts.Cluster, opts.Tenant)
}

func rsetPath(opts Options) string {
	return filepath.Join(opts.Base, "keos-use-cases", opts.Rset)
}
