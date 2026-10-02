package config

import "k8s.io/apimachinery/pkg/runtime/schema"

// App is one resolved component instance: a ComponentType's static facts
// combined with the environment-specific ones internal/components derived
// from the live cluster and the tenant file — which live object(s) it is,
// and which tenant-file entry, Kustomization and object it migrates into.
// It's what every diff/backup/migrate package operates on, and is never
// read from or written to disk.
type App struct {
	// ID identifies this instance on the command line and names its
	// backup directory: the resolved Object for a catalog instance, the
	// live name for a synthetic (uncatalogued) one.
	ID string
	// Name is a human-readable label, shown in progress/log output.
	Name string
	// Type is the ComponentType this instance was classified as, or ""
	// for a synthetic App built for a live object no catalog type selects
	// (see internal/backup.DiscoveredApps).
	Type string
	// Rset is the path, relative to keos-use-cases, to the ResourceSet
	// template that declares this instance's Kustomization.
	Rset string
	// Entry is the name of this instance's components.<key> entry in the
	// tenant file.
	Entry string
	// Kustomization is the name of the rendered Kustomization to inspect.
	Kustomization string
	// Object is the name of the HelmRelease or custom resource inside that
	// Kustomization to diff against the live cluster.
	Object string
	// Anchor — see ComponentType.Anchor.
	Anchor string
	// ChartPath — see Chart.Path. Empty means manifest mode.
	ChartPath string
	// ValuesRoot — see Chart.ValuesRoot.
	ValuesRoot string
	// Prepare — see ComponentType.Prepare.
	Prepare string
	// Exclude — see ComponentType.Exclude.
	Exclude []string
	// Notes — see ComponentType.Notes.
	Notes string
	// Live are the live legacy objects classified as this instance, the
	// primary one first. Empty for an App that was never matched against
	// a live cluster.
	Live []ObjectRef
}

// ObjectRef identifies one live object.
type ObjectRef struct {
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
}

// LiveName is the name to look this app up as on the live cluster: its
// primary live object's name, which differs from Object when the GitOps
// redesign renamed it (e.g. a legacy "psql-agent" migrating into
// "psql-gosec-agent"), otherwise Object itself.
func (a App) LiveName() string {
	if len(a.Live) > 0 {
		return a.Live[0].Name
	}
	return a.Object
}

// LiveNamespace is the primary live object's namespace, or "" when the
// app carries no live reference — the namespace to retry a live lookup in
// when the object isn't where the rendered GitOps manifest expects it
// (e.g. a component the redesign moved to a different namespace).
func (a App) LiveNamespace() string {
	if len(a.Live) > 0 {
		return a.Live[0].Namespace
	}
	return ""
}
