package prepare

import "context"

var stepDLC = Step{
	Name:        "prepare-dlc",
	Description: "remove the legacy DLC ingress and deployment (the chart changed an immutable selector label)",
	Automated:   true,
	// The legacy Deployment is deleted, so the GitOps one is created anew.
	ReplacesWorkloads: true,
	Plan:              planDLC,
}

// planDLC deletes the legacy DLC Ingresses, then its Deployments — the
// GitOps chart changed the Deployment's (immutable) selector, so it can't
// be adopted in place. The Python client looked the Ingress up as
// dlc-entity-dlc.stratio.<domain>, but CCT names it
// dlc-entity-dlc-entity.stratio.<domain>: it deleted only the Deployment,
// left the colliding Ingress behind, and then reported the step done.
// Selecting by CCT's app id label finds both, and skipping Flux-managed
// objects keeps a re-run from deleting the GitOps dlc-entity Deployment
// that replaced the legacy one under the same name.
func planDLC(ctx context.Context, opts Options) ([]Operation, error) {
	return planDeleteLegacy(ctx, opts, gvkIngress, gvkDeployment)
}
