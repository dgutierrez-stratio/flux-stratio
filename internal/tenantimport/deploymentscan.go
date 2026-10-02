package tenantimport

import (
	"context"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/catalog"
)

// scanDeployments is the primary app-component discovery pass: for every
// Deployment in a tenant namespace, an exact name match against the
// catalog's chart map wins; failing that, for a tenant namespace that
// yielded no exact match, the namespace's own suffix after "<tenant>-" is
// checked against the chart map instead — how a component running
// several differently-named Deployments in one namespace (e.g. genai's
// genai-api/genai-gateway/genai-litellm, namespace "<tenant>-genai")
// still gets discovered as a single "genai" entry.
func scanDeployments(ctx context.Context, c client.Client, cat *catalog.Catalog, tenantName string, components Components) error {
	var deployments appsv1.DeploymentList
	if err := c.List(ctx, &deployments); err != nil {
		return err
	}

	nsMatched := map[string]bool{}
	nsVisited := map[string]bool{}
	for _, d := range deployments.Items {
		// An owned Deployment (an operator's, a PgBouncer's) is never a
		// component of its own — classification skips it the same way.
		if !tenantObject(&d, tenantName) || len(d.OwnerReferences) > 0 {
			continue
		}
		nsVisited[d.Namespace] = true

		compKey := chartOwner(cat, d.Name)
		if compKey == "" {
			continue
		}
		nsMatched[d.Namespace] = true
		if components.find(compKey, d.Name) != nil {
			continue // already discovered (e.g. by the CRD scan)
		}
		components[compKey] = append(components[compKey], &Entry{Name: d.Name, Deps: map[string]string{}})
	}

	namespaces := make([]string, 0, len(nsVisited))
	for ns := range nsVisited {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces) // deterministic entry order in the generated file
	for _, ns := range namespaces {
		if nsMatched[ns] || !strings.HasPrefix(ns, tenantName+"-") {
			continue
		}
		suffix := strings.TrimPrefix(ns, tenantName+"-")
		compKey := chartOwner(cat, suffix)
		if compKey == "" || components.find(compKey, suffix) != nil {
			continue
		}
		components[compKey] = append(components[compKey], &Entry{Name: suffix, Deps: map[string]string{}})
	}
	return nil
}

// chartOwner resolves a chart name to its owning component key, returning
// "" if unknown or ambiguous — an ambiguous chart (e.g. "pgbackup", shared
// by two component keys) can't be safely discovered this way and is
// skipped rather than guessed at (see internal/catalog.ChartMapping).
func chartOwner(cat *catalog.Catalog, chartName string) string {
	return cat.Charts[chartName].Key
}
