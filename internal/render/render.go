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

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/reporequire"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

// Options configures one Render call.
type Options struct {
	// Repos is where keos-apps, keos-use-cases, keos-fleet and
	// keos-system-services are checked out.
	Repos config.RepoPaths
	// Cluster and Tenant locate the tenant's ResourceSetInputProvider
	// file: <keos-fleet>/clusters/<Cluster>/tenants/config/<Tenant>.yaml.
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
	// ReplacedPatches are the patch bodies the tenant file already applies
	// to Object's own kind (spec.patches entries whose target.kind is
	// Object's kind). Object, Kustomization and AllDocs are rendered
	// *without* them — see Render.
	ReplacedPatches []string
}

// Render runs the full two-stage pipeline described in the package doc and
// returns the app's desired-state object.
func Render(ctx context.Context, opts Options) (*Result, error) {
	if err := reporequire.Validate(opts.Repos); err != nil {
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

	obj, err := findObject(docs, opts)
	if err != nil {
		return nil, err
	}

	// Render the pristine base, without the patches the tenant file already
	// applies to this object's kind: those are exactly what
	// tenantfile.Splice replaces (by target.kind), so the patch computed
	// against this render is always the *whole* patch, never just what's
	// left over after the existing one — which, spliced in as a
	// replacement, would silently drop everything the existing patch
	// already carried. Every keos-use-cases template takes its patches only
	// from the tenant entry, so nothing template-authored is dropped here.
	replaced, stripped, err := withoutPatchesFor(ks, obj.GetKind())
	if err != nil {
		return nil, err
	}
	if len(replaced) > 0 {
		opts.Log.Debugf("rendering %q without the tenant file's %d existing %s patch(es)", opts.Object, len(replaced), obj.GetKind())
		ks = stripped
		if docs, err = buildKustomizationObjects(ctx, opts, ks); err != nil {
			return nil, err
		}
		if obj, err = findObject(docs, opts); err != nil {
			return nil, err
		}
	}
	return &Result{Object: obj, Kustomization: ks, AllDocs: docs, ReplacedPatches: replaced}, nil
}

func findObject(docs []*unstructured.Unstructured, opts Options) (*unstructured.Unstructured, error) {
	obj := yamldocs.FindByName(docs, opts.Object)
	if obj == nil {
		return nil, fmt.Errorf(
			"object %q not found among the %d object(s) rendered by kustomization %q (rset %q)",
			opts.Object, len(docs), opts.Kustomization, opts.Rset,
		)
	}
	return obj, nil
}

// withoutPatchesFor returns the patch bodies of ks's spec.patches entries
// targeting kind, and a copy of ks with those entries removed. ks is
// returned unchanged (and no copy made) when none target kind.
func withoutPatchesFor(ks *unstructured.Unstructured, kind string) ([]string, *unstructured.Unstructured, error) {
	patches, found, err := unstructured.NestedSlice(ks.Object, "spec", "patches")
	if err != nil {
		return nil, nil, fmt.Errorf("reading kustomization %q spec.patches: %w", ks.GetName(), err)
	}
	if !found {
		return nil, ks, nil
	}
	var replaced []string
	kept := make([]any, 0, len(patches))
	for _, p := range patches {
		entry, _ := p.(map[string]any)
		targetKind, _, _ := unstructured.NestedString(entry, "target", "kind")
		if targetKind != kind {
			kept = append(kept, p)
			continue
		}
		body, _, _ := unstructured.NestedString(entry, "patch")
		replaced = append(replaced, body)
	}
	if len(replaced) == 0 {
		return nil, ks, nil
	}
	stripped := ks.DeepCopy()
	if err := unstructured.SetNestedSlice(stripped.Object, kept, "spec", "patches"); err != nil {
		return nil, nil, fmt.Errorf("stripping kustomization %q spec.patches: %w", ks.GetName(), err)
	}
	return replaced, stripped, nil
}

func tenantFilePath(opts Options) string {
	return tenantfile.Path(opts.Repos.Fleet, opts.Cluster, opts.Tenant)
}

func rsetPath(opts Options) string {
	return filepath.Join(opts.Repos.UseCases, opts.Rset)
}
