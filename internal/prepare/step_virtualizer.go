package prepare

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var stepVirtualizer = Step{
	Name:        "prepare-virtualizer",
	Description: "remove the legacy virtualizer, virtualizer-monitor and virtualizer-ui Deployments (the chart changed their immutable selectors)",
	Automated:   true,
	// The legacy Deployments are deleted, so the GitOps ones are created anew.
	ReplacesWorkloads: true,
	Plan:              planDeleteFamily,
}

// planDeleteFamily deletes the Deployments of every legacy workload of the
// virtualizer family — the server and its monitor and ui siblings, which
// CCT deployed as three apps and the chart renders as one release. The
// chart gives each Deployment its own (immutable) selector under the same
// name, so none can be adopted in place, and an upgrade that meets one
// fails and then fails to roll back (the chart names its prerequisites Job
// after the release revision).
//
// Unlike prepare-dlc, the workloads are found by the app's family selectors
// (Options.IsLegacy), not by one CCT app id: after the server was migrated
// the app's live objects are the Helm-managed ones, and the monitor and ui
// are still legacy siblings nothing else points at. Anything Flux manages
// is skipped, so a re-run after the cutover plans nothing.
//
// Deliberately not touched: the legacy ingresses (their hosts differ from
// the chart's, so nothing collides, and a patch that carries the legacy
// monitorAPI/logoutAPI URLs depends on them — delete them by hand once the
// patch uses the chart's hosts), the Services (CCT's, once Helm adopts
// them) and the legacy virtualizer-loadbalancer Service, which carries no
// CCT label: the migration patch carries that one, so Helm takes it over on
// push (internal/diff serviceEdits). See docs/migration-runbook.md.
//
// prepare-genai shares it for genai-api, genai-ui and genai-developer-proxy
// (the same family shape: CCT apps of their own, one chart).
func planDeleteFamily(ctx context.Context, opts Options) ([]Operation, error) {
	if opts.LiveNamespace == "" {
		return nil, fmt.Errorf("no live object to identify the app's legacy objects by")
	}
	if opts.IsLegacy == nil {
		return nil, fmt.Errorf("no selector for the app's legacy workloads")
	}

	deployments, err := listIn(ctx, opts, gvkDeployment)
	if err != nil {
		return nil, err
	}
	var ops []Operation
	for _, d := range deployments {
		if fluxManaged(d) || len(d.GetOwnerReferences()) > 0 || !opts.IsLegacy(d) {
			continue
		}
		ops = append(ops, deleteOp(d))
	}
	return ops, nil
}

// listIn lists the objects of kind gvk in the app's live namespace, sorted
// by name so a plan is the same on every run.
func listIn(ctx context.Context, opts Options, gvk schema.GroupVersionKind) ([]*unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err := opts.Client.List(ctx, list, client.InNamespace(opts.LiveNamespace)); err != nil {
		return nil, fmt.Errorf("listing %ss in %s: %w", gvk.Kind, opts.LiveNamespace, err)
	}
	out := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		obj := &list.Items[i]
		obj.SetGroupVersionKind(gvk)
		out = append(out, obj)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].GetName() < out[b].GetName() })
	return out, nil
}
