package tenantimport

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
)

var helmReleaseGVK = schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}

// scanHelmReleases is the secondary discovery pass: for every HelmRelease
// in a tenant namespace, if its chart's instance is already discovered
// (by scanDeployments) under the same name, this only enriches that
// entry's storage type and HelmReleaseSpec; if it isn't discovered at all
// (a HelmRelease with no backing Deployment — unusual, but possible), a
// new entry is added. A no-op, not an error, when the HelmRelease CRD
// isn't installed — a pre-migration cluster has none.
func scanHelmReleases(ctx context.Context, c client.Client, cat *catalog.Catalog, tenantName string, components Components) error {
	list, err := kubeclient.ListUnstructured(ctx, c, helmReleaseGVK, "")
	if err != nil {
		return nil //nolint:nilerr // absence of the CRD is expected pre-migration, not a failure
	}
	for i := range list.Items {
		hr := &list.Items[i]
		if !inTenantNamespace(hr.GetNamespace(), tenantName) {
			continue
		}
		chartName, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "chart")
		if chartName == "" {
			continue
		}
		compKey := chartOwner(cat, chartName)
		if compKey == "" {
			continue
		}
		spec, _, _ := unstructured.NestedMap(hr.Object, "spec")

		entry := components.find(compKey, hr.GetName())
		if entry == nil {
			entry = &Entry{Name: hr.GetName(), Deps: map[string]string{}}
			components[compKey] = append(components[compKey], entry)
		}
		entry.HelmReleaseSpec = spec
		if entry.Type == "" {
			entry.Type = inferStorageType(spec)
		}
	}
	return nil
}

// inferStorageType reads a HelmRelease's spec.values for a
// storageType/storage_type/type field, or infers "hdfs" from the presence
// of an hdfs/hdfsCluster/hdfsEnabled key. Ported unchanged from the Python
// client: it recognizes a live chart's own value shape, which isn't
// template-derivable the way everything in internal/catalog is.
func inferStorageType(spec map[string]any) string {
	values, _, _ := unstructured.NestedMap(spec, "values")
	if values == nil {
		return ""
	}
	for _, key := range []string{"storageType", "storage_type", "type"} {
		if v, ok := values[key].(string); ok && v != "" {
			return strings.ToLower(v)
		}
	}
	for _, key := range []string{"hdfs", "hdfsCluster", "hdfsEnabled"} {
		if _, ok := values[key]; ok {
			return "hdfs"
		}
	}
	return ""
}
