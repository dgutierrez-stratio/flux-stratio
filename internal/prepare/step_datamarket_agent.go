package prepare

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/kubeclient"
)

var stepDatamarketAgent = Step{
	Name:        "prepare-datamarket-agent",
	Description: "suspend and scale down the legacy datamarket-agent before cutover",
	Automated:   true,
	Plan:        planDatamarketAgent,
}

// datamarketAgentHelmRelease is the name of the legacy Ansible-managed
// HelmRelease some environments deploy datamarket-agent with; eosdev has
// none (CCT deploys it as a plain Deployment).
const datamarketAgentHelmRelease = "datamarket-agent"

// planDatamarketAgent suspends the legacy HelmRelease, if there is one and
// it isn't already suspended (a HelmRelease Flux itself applies is the
// GitOps side, never touched), then scales every legacy Deployment still
// running to 0 and waits for its pods to go. The Deployments are kept.
//
// A legacy Deployment is one CCT deployed (legacyObjects) or one the
// legacy HelmRelease rendered: helm-controller labels what it renders
// with helm.toolkit.fluxcd.io/name, the label legacyObjects reads as
// "Flux-managed", and needn't carry CCT's label at all — so without this
// second lookup, an environment with the legacy HelmRelease had nothing
// scaled down and the legacy agent kept running alongside the new one.
func planDatamarketAgent(ctx context.Context, opts Options) ([]Operation, error) {
	var ops []Operation
	var legacyHR *unstructured.Unstructured

	hr, err := kubeclient.GetUnstructured(ctx, opts.Client, gvkHelmRelease, opts.LiveNamespace, datamarketAgentHelmRelease)
	switch {
	case kubeclient.IsNotFound(err):
	case err != nil:
		return nil, fmt.Errorf("fetching HelmRelease %s/%s: %w", opts.LiveNamespace, datamarketAgentHelmRelease, err)
	default:
		suspended, _, _ := unstructured.NestedBool(hr.Object, "spec", "suspend")
		if !fluxManaged(hr) {
			legacyHR = hr
			if !suspended {
				ops = append(ops, patchOp("suspend", hr, map[string]any{"suspend": true}))
			}
		}
	}

	deps, err := legacyObjects(ctx, opts, gvkDeployment)
	if err != nil {
		return nil, err
	}
	if legacyHR != nil {
		rendered, err := renderedBy(ctx, opts, legacyHR, gvkDeployment)
		if err != nil {
			return nil, err
		}
		deps = appendNew(deps, rendered)
	}
	for _, dep := range deps {
		if n, found, _ := unstructured.NestedInt64(dep.Object, "spec", "replicas"); found && n == 0 {
			continue
		}
		ops = append(ops, scaleToZeroOp(dep))
	}
	return ops, nil
}

// scaleToZeroOp scales dep to 0 replicas and waits for the pods its own
// selector matches to terminate. The Python client waited on
// app.kubernetes.io/name=datamarket-agent, a label the CCT pods don't
// carry, so it never actually waited.
func scaleToZeroOp(dep *unstructured.Unstructured) Operation {
	op := patchOp("scale to 0 replicas", dep, map[string]any{"replicas": 0})
	patch := op.apply
	op.apply = func(ctx context.Context, c client.Client) error {
		// Checked before scaling: with no selector there's nothing to wait
		// on, and a scale-down that can't be waited for isn't attempted.
		selector, _, err := unstructured.NestedStringMap(dep.Object, "spec", "selector", "matchLabels")
		if err != nil {
			return fmt.Errorf("reading its pod selector: %w", err)
		}
		if len(selector) == 0 {
			return fmt.Errorf("it has no spec.selector.matchLabels to wait for its pods by")
		}
		if err := patch(ctx, c); err != nil {
			return err
		}
		return waitForNoPods(ctx, c, dep.GetNamespace(), selector, 2*time.Minute)
	}
	return op
}

// renderedBy lists the objects of kind gvk helm-controller rendered for
// hr, by the labels it stamps on each one.
func renderedBy(ctx context.Context, opts Options, hr *unstructured.Unstructured, gvk schema.GroupVersionKind) ([]*unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	labels := client.MatchingLabels{
		"helm.toolkit.fluxcd.io/name":      hr.GetName(),
		"helm.toolkit.fluxcd.io/namespace": hr.GetNamespace(),
	}
	if err := opts.Client.List(ctx, list, labels); err != nil {
		return nil, fmt.Errorf("listing the %ss HelmRelease %s/%s rendered: %w", gvk.Kind, hr.GetNamespace(), hr.GetName(), err)
	}
	out := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		obj := &list.Items[i]
		obj.SetGroupVersionKind(gvk)
		out = append(out, obj)
	}
	return out, nil
}

// appendNew appends to objs each of more not already in it (by UID, or
// namespace/name when there's none).
func appendNew(objs, more []*unstructured.Unstructured) []*unstructured.Unstructured {
	key := func(o *unstructured.Unstructured) string {
		if uid := o.GetUID(); uid != "" {
			return string(uid)
		}
		return o.GetNamespace() + "/" + o.GetName()
	}
	seen := map[string]bool{}
	for _, o := range objs {
		seen[key(o)] = true
	}
	for _, o := range more {
		if !seen[key(o)] {
			seen[key(o)] = true
			objs = append(objs, o)
		}
	}
	return objs
}
