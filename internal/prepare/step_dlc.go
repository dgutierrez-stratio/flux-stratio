package prepare

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var stepDLC = Step{
	Name:        "prepare-dlc",
	Description: "remove the legacy DLC ingress and deployment (the chart changed an immutable selector label)",
	Automated:   true,
	Satisfied:   dlcSatisfied,
	Run:         runDLC,
}

// dlcNamespace and dlcIngressName are tenant-aware — the Python client
// hardcoded the namespace to "stratio-dlc" regardless of --tenant.
func dlcNamespace(tenantName string) string { return tenantName + "-dlc" }

func dlcIngressName(ctx context.Context, c client.Client) (string, error) {
	domain, err := clusterExternalDomain(ctx, c)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("dlc-entity-dlc.stratio.%s", domain), nil
}

func dlcSatisfied(ctx context.Context, opts Options) (bool, error) {
	ns := dlcNamespace(opts.TenantName)

	name, err := dlcIngressName(ctx, opts.Client)
	if err != nil {
		return false, err
	}
	var ing networkingv1.Ingress
	err = opts.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &ing)
	if err == nil {
		return false, nil
	}
	if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("fetching Ingress %s/%s: %w", ns, name, err)
	}

	var dep appsv1.Deployment
	err = opts.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: "dlc-entity"}, &dep)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("fetching Deployment %s/dlc-entity: %w", ns, err)
	}
	return false, nil
}

func runDLC(ctx context.Context, opts Options) error {
	ns := dlcNamespace(opts.TenantName)

	name, err := dlcIngressName(ctx, opts.Client)
	if err != nil {
		return err
	}
	ing := &networkingv1.Ingress{}
	ing.Namespace, ing.Name = ns, name
	if err := opts.Client.Delete(ctx, ing); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting Ingress %s/%s: %w", ns, name, err)
	}

	dep := &appsv1.Deployment{}
	dep.Namespace, dep.Name = ns, "dlc-entity"
	if err := opts.Client.Delete(ctx, dep); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting Deployment %s/dlc-entity: %w", ns, err)
	}
	return nil
}
