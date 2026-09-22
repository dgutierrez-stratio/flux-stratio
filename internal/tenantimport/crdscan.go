package tenantimport

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
)

// crdDepSpecPath names where, in a live CRD instance's own spec, a
// dependency's instance name lives.
type crdDepSpecPath struct {
	depKey   string
	specPath []string
}

// crdDepSpecPaths is a fixed mapping reflecting the legacy Ansible-cluster
// CRD schema (not derivable from keos-use-cases's templates, which
// describe the new GitOps schema instead) — the same static map the
// Python client carried for the same reason.
var crdDepSpecPaths = map[string][]crdDepSpecPath{
	"pgbouncers.postgres.stratio.com":       {{depKey: "postgres", specPath: []string{"pgcluster", "name"}}},
	"osdashboardses.opensearch.stratio.com": {{depKey: "opensearch", specPath: []string{"oscluster", "name"}}},
}

// scanCRDs lists every live instance of every CRD internal/catalog found a
// healthCheckExprs declaration for, in the tenant's namespaces, and turns
// each into a skeleton Entry with whatever dependency names its own spec
// directly supplies.
func scanCRDs(ctx context.Context, c client.Client, cat *catalog.Catalog, tenantName string) Components {
	components := Components{}
	for plural, info := range cat.CRDs {
		list, err := kubeclient.ListUnstructured(ctx, c, info.GVK, "")
		if err != nil {
			continue // this CRD isn't installed on the cluster — not every tenant has every component
		}
		for i := range list.Items {
			item := &list.Items[i]
			if !inTenantNamespace(item.GetNamespace(), tenantName) {
				continue
			}
			name := item.GetName()
			if name == "" {
				continue
			}
			entry := &Entry{Name: name, Deps: map[string]string{}}
			for _, dep := range crdDepSpecPaths[plural] {
				path := append([]string{"spec"}, dep.specPath...)
				if v, found, _ := unstructured.NestedString(item.Object, path...); found && v != "" {
					entry.Deps[dep.depKey] = v
				}
			}
			components[info.ComponentKey] = append(components[info.ComponentKey], entry)
		}
	}
	return components
}
