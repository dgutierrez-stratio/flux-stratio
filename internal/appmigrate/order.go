package appmigrate

import (
	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

// OrderApps orders apps so that an app whose tenant-file entry declares
// another configured app as a dependency (via
// config.dependencies.<key>.name — internal/tenantfile.DependencyNames)
// comes after it: a topological sort over the dependency edges the
// tenant file already declares, so `apps migrate --all` migrates a
// dependency before its dependents rather than in arbitrary config-file
// order.
//
// An app not yet present in the tenant file, or with no discoverable
// dependency edges, keeps its original relative position. A dependency
// cycle (which would mean the tenant file itself is inconsistent) never
// causes an error — the cyclic apps simply keep their original relative
// order among themselves; ordering here is a safety aid, not a
// correctness requirement enforced on the input.
func OrderApps(doc *tenantfile.Doc, cat *catalog.Catalog, apps []config.App) []config.App {
	return topoSort(apps, dependencyGraph(doc, cat, apps))
}

// Dependencies maps each app's ID to the IDs of the apps among apps it
// depends on, by the same tenant-file edges OrderApps sorts by — so
// `apps migrate --all` can leave out an app whose dependency wasn't
// migrated.
func Dependencies(doc *tenantfile.Doc, cat *catalog.Catalog, apps []config.App) map[string][]string {
	graph := dependencyGraph(doc, cat, apps)
	out := make(map[string][]string, len(apps))
	for i, deps := range graph {
		for _, j := range deps {
			out[apps[i].ID] = append(out[apps[i].ID], apps[j].ID)
		}
	}
	return out
}

// dependencyGraph returns, for each app, the indices of the apps it
// depends on.
func dependencyGraph(doc *tenantfile.Doc, cat *catalog.Catalog, apps []config.App) [][]int {
	ownerToIndex := make(map[string]int, len(apps))
	for i, app := range apps {
		if anchor, err := cat.ResolveAnchor(app.Kustomization); err == nil {
			ownerToIndex[tenantfile.OwnerOf(app, anchor)] = i
		}
	}

	graph := make([][]int, len(apps)) // graph[i] = indices app i depends on
	for i, app := range apps {
		anchor, err := cat.ResolveAnchor(app.Kustomization)
		if err != nil {
			continue
		}
		entry, err := tenantfile.FindComponentEntry(doc, tenantfile.OwnerOf(app, anchor))
		if err != nil {
			continue // not present in the tenant file yet: no known deps
		}
		for _, depName := range tenantfile.DependencyNames(entry) {
			if j, ok := ownerToIndex[depName]; ok && j != i {
				graph[i] = append(graph[i], j)
			}
		}
	}
	return graph
}

// topoSort returns apps in DFS post-order over graph (edges point from an
// app to what it depends on), so a dependency is always appended before
// its dependent. Visiting apps in their original index order keeps
// independent apps in their original relative order, and a "visiting"
// marker makes a cycle a silent no-op (the back edge is skipped) rather
// than infinite recursion.
func topoSort(apps []config.App, graph [][]int) []config.App {
	const (
		unvisited = iota
		visiting
		done
	)
	state := make([]int, len(apps))
	order := make([]int, 0, len(apps))

	var visit func(i int)
	visit = func(i int) {
		if state[i] != unvisited {
			return
		}
		state[i] = visiting
		for _, dep := range graph[i] {
			visit(dep)
		}
		state[i] = done
		order = append(order, i)
	}
	for i := range apps {
		visit(i)
	}

	out := make([]config.App, len(order))
	for k, i := range order {
		out[k] = apps[i]
	}
	return out
}
