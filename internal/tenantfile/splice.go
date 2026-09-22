// Package tenantfile locates a tenant's ResourceSetInputProvider YAML file
// (Path) and edits it in place (Splice): decode as a yaml.Node tree,
// locate the app's anchor via internal/catalog, replace any existing
// patches sharing the same target.kind, re-encode, and write via a temp
// file and atomic rename — preserving every comment and commented-out
// block (design decision 3 in the project plan).
package tenantfile

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
)

// UpsertPatch replaces, within targetNode's own "patches" sequence, any
// existing entry whose target.kind equals patch.TargetKind, and appends
// patch. Idempotent per target.kind: running it twice with the same patch
// produces the same sequence both times, and a hand-written patch of a
// different kind at the same anchor is left untouched.
func UpsertPatch(targetNode *yaml.Node, patch diff.PatchDoc) error {
	entryNode, err := diff.PatchEntryNode(patch)
	if err != nil {
		return err
	}

	patches := mapGet(targetNode, "patches")
	if patches == nil || patches.Kind != yaml.SequenceNode {
		patches = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		mapSet(targetNode, "patches", patches)
	}

	kept := patches.Content[:0]
	for _, existing := range patches.Content {
		if patchTargetKind(existing) == patch.TargetKind {
			continue
		}
		kept = append(kept, existing)
	}
	patches.Content = append(kept, entryNode)
	return nil
}

// patchTargetKind reads target.kind out of one patches: sequence entry.
func patchTargetKind(entry *yaml.Node) string {
	target := mapGet(entry, "target")
	if target == nil {
		return ""
	}
	if kind := mapGet(target, "kind"); kind != nil {
		return kind.Value
	}
	return ""
}

// Splice locates the tenant YAML location app's Kustomization patches come
// from and upserts patch there. cat resolves the anchor from
// app.Kustomization's name (design decision 2); if app.Anchor is set, it
// is validated against what the catalog independently derives — a
// mismatch fails loudly (naming both values) rather than writing to the
// wrong place.
func Splice(d *Doc, cat *catalog.Catalog, app config.App, patch diff.PatchDoc) error {
	anchor, err := cat.ResolveAnchor(app.Kustomization)
	if err != nil {
		return fmt.Errorf("app %q: %w", app.ID, err)
	}
	if app.Anchor != "" && (anchor.Kind != catalog.AnchorNested || anchor.Field != app.Anchor) {
		return fmt.Errorf(
			"app %q declares anchor %q, but kustomization %q resolves to %s (field %q) in the templates — "+
				"update the config or investigate the mismatch before migrating",
			app.ID, app.Anchor, app.Kustomization, anchor.Kind, anchor.Field,
		)
	}

	entry, err := FindComponentEntry(d, OwnerName(app.Kustomization, anchor))
	if err != nil {
		return fmt.Errorf("app %q: %w", app.ID, err)
	}
	targetNode, err := ResolveAnchorNode(entry, anchor)
	if err != nil {
		return fmt.Errorf("app %q: %w", app.ID, err)
	}
	return UpsertPatch(targetNode, patch)
}

// OwnerName recovers the name of the components.<key>[] entry that owns
// this Kustomization, by stripping the "apps-" prefix and the matched
// anchor suffix from its name. For the default anchor (suffix "") this is
// the same as the app's own Object; for a nested anchor (e.g.
// "-gosec-agent") it is the parent component's name instead — a gosec
// agent's Object is its own HelmRelease's name (e.g.
// "psql-gosec-agent"), but the entry carrying its patches is the parent
// postgres/opensearch instance (e.g. "psql"), so Object is never the
// right name to search the tenant file for here. Exported for
// internal/appmigrate, which needs it to build a dependency graph for
// `apps migrate --all`'s topological ordering.
func OwnerName(kustomizationName string, anchor catalog.ResolvedAnchor) string {
	name := strings.TrimPrefix(kustomizationName, "apps-")
	return strings.TrimSuffix(name, anchor.Suffix)
}
