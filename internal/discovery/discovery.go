// Package discovery finds a live object by exact identity name, cluster
// wide, with no namespace filter and no catalog involved — the same
// unscoped search the legacy Python migration client's discovery.py does
// (list every Kustomization/HelmRelease/Deployment-family/known-CRD
// instance cluster-wide, merge by identity, match by name), so
// internal/backup can find an app's live legacy state wherever it still
// sits, without needing a GitOps render or the tenant file to declare it.
//
// This is deliberately not internal/tenantimport's scan code
// (crdscan.go/deploymentscan.go/helmreleasescan.go), which solves a
// different problem: inferring which catalog component a live object
// belongs to, scoped to a tenant's own namespaces and matched by chart
// name via internal/catalog. Backup already knows exactly which name it's
// looking for; it just doesn't know which kind, or which namespace, the
// object currently lives under.
package discovery

import (
	"context"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
)

// bucket indexes a set of same-kind live objects by name, across every
// namespace.
type bucket map[string][]*unstructured.Unstructured

// Index is the result of one cluster-wide Scan: every Kustomization,
// HelmRelease, workload (Deployment/StatefulSet/DaemonSet) and known CRD
// instance found, indexed by name.
type Index struct {
	crs            bucket
	workloads      bucket
	helmReleases   bucket
	kustomizations bucket
}

// Scan lists every kind this package knows about, cluster-wide (no
// namespace filter — matching Python's discovery.py exactly), and indexes
// the results by name. A kind that isn't installed on this cluster
// (apimeta.IsNoMatchError) or that RBAC forbids listing
// (apierrors.IsForbidden) is logged and treated as empty, mirroring
// Python's bare "except Exception: items = []" for this one expected-absence
// case; any other list error is fatal.
func Scan(ctx context.Context, c client.Client, l *log.Logger) (*Index, error) {
	idx := &Index{
		crs:            bucket{},
		workloads:      bucket{},
		helmReleases:   bucket{},
		kustomizations: bucket{},
	}

	if err := scanInto(ctx, c, l, kustomizationGVK, idx.kustomizations); err != nil {
		return nil, err
	}
	if err := scanInto(ctx, c, l, helmReleaseGVK, idx.helmReleases); err != nil {
		return nil, err
	}
	for _, gvk := range workloadGVKs {
		if err := scanInto(ctx, c, l, gvk, idx.workloads); err != nil {
			return nil, err
		}
	}
	for _, gvk := range crdGVKs {
		if err := scanInto(ctx, c, l, gvk, idx.crs); err != nil {
			return nil, err
		}
	}
	return idx, nil
}

func scanInto(ctx context.Context, c client.Client, l *log.Logger, gvk schema.GroupVersionKind, into bucket) error {
	list, err := kubeclient.ListUnstructured(ctx, c, gvk, "")
	if err != nil {
		if tolerable(err) {
			l.Debugf("%s isn't installed or accessible on this cluster, skipping: %v", gvk.Kind, err)
			return nil
		}
		return err
	}
	for i := range list.Items {
		item := &list.Items[i]
		into[item.GetName()] = append(into[item.GetName()], item)
	}
	return nil
}

// tolerable reports whether err is the expected-absence case Scan should
// silently skip: the kind isn't installed on this cluster, or RBAC
// forbids listing it.
func tolerable(err error) bool {
	return apimeta.IsNoMatchError(err) || apierrors.IsForbidden(err)
}

// FindCR returns the named custom resource instance, among the 14 known
// Stratio operator CRDs.
func (idx *Index) FindCR(name string, l *log.Logger) (*unstructured.Unstructured, bool) {
	return find(idx.crs, name, l)
}

// FindWorkload returns the named Deployment, StatefulSet or DaemonSet.
func (idx *Index) FindWorkload(name string, l *log.Logger) (*unstructured.Unstructured, bool) {
	return find(idx.workloads, name, l)
}

// FindHelmRelease returns the named HelmRelease.
func (idx *Index) FindHelmRelease(name string, l *log.Logger) (*unstructured.Unstructured, bool) {
	return find(idx.helmReleases, name, l)
}

// FindKustomization returns the named Kustomization.
func (idx *Index) FindKustomization(name string, l *log.Logger) (*unstructured.Unstructured, bool) {
	return find(idx.kustomizations, name, l)
}

// Names returns every distinct identity name Scan found, across all four
// kinds, sorted for a deterministic `apps backup --all` run. Used to back
// up every live object the cluster scan finds, not just the config
// catalog.
func (idx *Index) Names() []string {
	seen := map[string]bool{}
	var names []string
	for _, b := range []bucket{idx.crs, idx.workloads, idx.helmReleases, idx.kustomizations} {
		for name := range b {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// find returns a match for name in b, warning if more than one namespace
// has an object with that name — Python's discovery has no such check,
// but a single Scan here now covers far more of the cluster in one pass
// than Python's single-target invocation ever did. On ambiguity, matches
// is sorted by namespace first, so the pick is deterministic (the
// alphabetically-first namespace) rather than dependent on the List
// API's own, unspecified return order — this doesn't know which
// namespace is "correct" (this package deliberately has no tenant/
// namespace scoping — see the package doc comment), it only removes
// run-to-run nondeterminism from an already-ambiguous situation the
// caller is warned about either way.
func find(b bucket, name string, l *log.Logger) (*unstructured.Unstructured, bool) {
	matches := b[name]
	if len(matches) == 0 {
		return nil, false
	}
	if len(matches) > 1 {
		sort.Slice(matches, func(i, j int) bool {
			return matches[i].GetNamespace() < matches[j].GetNamespace()
		})
		l.Warningf("more than one live object named %q found (namespaces: %s); using %s/%s",
			name, namespacesOf(matches), matches[0].GetNamespace(), matches[0].GetName())
	}
	return matches[0], true
}

func namespacesOf(objs []*unstructured.Unstructured) string {
	out := ""
	for i, o := range objs {
		if i > 0 {
			out += ", "
		}
		out += o.GetNamespace()
	}
	return out
}
