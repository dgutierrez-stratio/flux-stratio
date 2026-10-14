package prepare

import "context"

var stepDatarest = Step{
	Name:        "prepare-datarest",
	Description: "remove the legacy DataRest ingress and Deployment (the ingress would collide with the GitOps-managed one, and the chart changed the Deployment's immutable selector)",
	Automated:   true,
	// The legacy Deployment is deleted, so the GitOps one is created anew.
	ReplacesWorkloads: true,
	Plan:              planDatarest,
}

// planDatarest deletes every legacy Ingress CCT deployed for the DataRest
// instance (its admin ingress, dg-datarest-pgi-admin.<domain> on eosdev:
// the chart's own has the same host, path and class), then its Deployment
// (the chart keeps the name and namespace but changed the immutable
// selector). Same objects as prepare-dlc's, by CCT's app id, skipping
// anything Flux manages; the Service and everything else stay. The Python
// client deleted one hardcoded name in a hardcoded namespace, so any other
// instance was never handled.
func planDatarest(ctx context.Context, opts Options) ([]Operation, error) {
	return planDeleteLegacy(ctx, opts, gvkIngress, gvkDeployment)
}
