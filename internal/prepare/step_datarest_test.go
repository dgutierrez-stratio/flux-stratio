package prepare

import (
	"context"
	"testing"
)

var datarestOpts = Options{TenantName: "stratio", LiveName: "dg-datarest-pgi", LiveNamespace: "stratio-datastores"}

// TestPlanDatarest_OnlyTheAppsLegacyIngresses mirrors eosdev: the admin
// Ingress CCT deployed for dg-datarest-pgi is deleted; another app's, the
// same app id in another namespace, and a Flux-managed one aren't.
func TestPlanDatarest_OnlyTheAppsLegacyIngresses(t *testing.T) {
	const id = "dg-datarest-pgi.stratio-datastores"
	c := fakeClient(t,
		object(gvkIngress, "stratio-datastores", "dg-datarest-pgi-admin.eosdev.int", cctAppIDLabel, id),
		object(gvkIngress, "stratio-datastores", "other-app", cctAppIDLabel, "other.stratio-datastores"),
		object(gvkIngress, "other-datastores", "dg-datarest-pgi-admin.eosdev.int", cctAppIDLabel, id),
		object(gvkIngress, "stratio-datastores", "dg-datarest-pgi", cctAppIDLabel, id, "helm.toolkit.fluxcd.io/name", "dg-datarest-pgi"),
		object(gvkDeployment, "stratio-datastores", "dg-datarest-pgi", cctAppIDLabel, id),
	)
	opts := datarestOpts
	opts.Client = c

	ops, err := planDatarest(context.Background(), opts)
	assertPlan(t, ops, err, "delete Ingress stratio-datastores/dg-datarest-pgi-admin.eosdev.int")

	applyAll(t, c, ops)
	ops, err = planDatarest(context.Background(), opts)
	assertPlan(t, ops, err) // satisfied: nothing left to do
}
