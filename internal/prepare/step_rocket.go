package prepare

import (
	"context"
	"fmt"
)

var stepRocket = Step{
	Name:        "prepare-rocket",
	Description: "remove the legacy rocket Deployment and the Deployments its server created (rocket-catalog, -debug, -externalservice, -validator): the chart changed rocket's immutable selector",
	Automated:   true,
	// The legacy Deployment is deleted, so the GitOps one is created anew.
	ReplacesWorkloads: true,
	Plan:              planRocket,
}

// planRocket deletes the legacy rocket Deployment, then the Deployments the
// legacy rocket server created at runtime (rocket-catalog, -debug,
// -externalservice and -validator on eosdev): they carry no CCT label, only
// an ownerReference to the rocket Deployment, so deleting the parent would
// garbage-collect them anyway — they are listed here so the plan shows
// exactly what goes, and so the new server, which recreates them, never
// meets the old ones.
//
// The parent is found like prepare-dlc's objects, by CCT's app id label,
// and Flux-managed ones are skipped. The children are derived only from the
// parent found in this run: a Deployment owned by that very UID. Never
// "every owned Deployment in the namespace" — after the cutover the new
// server's children carry no Flux label either, and are owned by the new
// rocket, which Flux manages and which is therefore never found here: the
// plan is then empty. rocket-catalog-standalone is a CCT app of its own
// (another app id, no owner), so it is never planned.
//
// Deliberately not touched: the Ingresses (rocket-rocket.<domain>, the
// oauth2redirect ones) and the Services.
func planRocket(ctx context.Context, opts Options) ([]Operation, error) {
	parents, err := legacyObjects(ctx, opts, gvkDeployment)
	if err != nil {
		return nil, err
	}
	if len(parents) == 0 {
		return nil, nil
	}
	deployments, err := listIn(ctx, opts, gvkDeployment)
	if err != nil {
		return nil, fmt.Errorf("listing the Deployments owned by the legacy rocket: %w", err)
	}
	var ops []Operation
	for _, parent := range parents {
		ops = append(ops, deleteOp(parent))
		for _, d := range deployments {
			if !fluxManaged(d) && ownedBy(d, parent.GetUID()) {
				ops = append(ops, deleteOp(d))
			}
		}
	}
	return ops, nil
}
