package prepare

import (
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var stepDatarest = Step{
	Name:        "prepare-datarest",
	Description: "remove the legacy DataRest ingress that would collide with the GitOps-managed one",
	Automated:   true,
	Satisfied:   datarestSatisfied,
	Run:         runDatarest,
}

// datarestIngress resolves the namespace and name of the legacy Ingress
// this step removes, tenant-aware — the Python client hardcoded the
// namespace to "stratio-datastores" regardless of --tenant.
func datarestIngress(ctx context.Context, opts Options) (namespace, name string, err error) {
	domain, err := clusterExternalDomain(ctx, opts.Client)
	if err != nil {
		return "", "", err
	}
	return opts.TenantName + "-datastores", fmt.Sprintf("dg-datarest-pgi-admin.%s", domain), nil
}

func datarestSatisfied(ctx context.Context, opts Options) (bool, error) {
	ns, name, err := datarestIngress(ctx, opts)
	if err != nil {
		return false, err
	}
	var ing networkingv1.Ingress
	err = opts.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &ing)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("fetching Ingress %s/%s: %w", ns, name, err)
	}
	return false, nil
}

func runDatarest(ctx context.Context, opts Options) error {
	ns, name, err := datarestIngress(ctx, opts)
	if err != nil {
		return err
	}
	ing := &networkingv1.Ingress{}
	ing.Namespace, ing.Name = ns, name
	if err := opts.Client.Delete(ctx, ing); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting Ingress %s/%s: %w", ns, name, err)
	}
	return nil
}
