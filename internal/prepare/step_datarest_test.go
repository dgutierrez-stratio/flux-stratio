package prepare

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

var gvkService = schema.GroupVersionKind{Version: "v1", Kind: "Service"}

var datarestOpts = Options{TenantName: "stratio", LiveName: "dg-datarest-pgi", LiveNamespace: "stratio-datastores"}

// TestPlanDatarest_TheAppsLegacyIngressThenItsDeployment mirrors eosdev: the
// admin Ingress CCT deployed for dg-datarest-pgi goes, then its Deployment
// (the chart changed the immutable selector under the same name); another
// app's objects, the same app id in another namespace, a Flux-managed
// Ingress or Deployment and the Service aren't touched.
func TestPlanDatarest_TheAppsLegacyIngressThenItsDeployment(t *testing.T) {
	const id = "dg-datarest-pgi.stratio-datastores"
	c := fakeClient(t,
		object(gvkIngress, "stratio-datastores", "dg-datarest-pgi-admin.eosdev.int", cctAppIDLabel, id),
		object(gvkIngress, "stratio-datastores", "other-app", cctAppIDLabel, "other.stratio-datastores"),
		object(gvkIngress, "other-datastores", "dg-datarest-pgi-admin.eosdev.int", cctAppIDLabel, id),
		object(gvkIngress, "stratio-datastores", "dg-datarest-pgi", cctAppIDLabel, id, "helm.toolkit.fluxcd.io/name", "dg-datarest-pgi"),
		legacyDeployment("stratio-datastores", "dg-datarest-pgi", id, 1),
		legacyDeployment("stratio-datastores", "other-app", "other.stratio-datastores", 1),
		legacyDeployment("other-datastores", "dg-datarest-pgi", id, 1),
		object(gvkService, "stratio-datastores", "dg-datarest-pgi", cctAppIDLabel, id),
	)
	opts := datarestOpts
	opts.Client = c

	ops, err := planDatarest(context.Background(), opts)
	assertPlan(t, ops, err,
		"delete Ingress stratio-datastores/dg-datarest-pgi-admin.eosdev.int",
		"delete Deployment stratio-datastores/dg-datarest-pgi")

	applyAll(t, c, ops)
	ops, err = planDatarest(context.Background(), opts)
	assertPlan(t, ops, err) // satisfied: nothing left to do
}

// TestPlanDatarest_NeverTheGitOpsDeployment: after the cutover the GitOps
// Deployment has the legacy one's name; a re-run must not delete it.
func TestPlanDatarest_NeverTheGitOpsDeployment(t *testing.T) {
	const id = "dg-datarest-pgi.stratio-datastores"
	opts := datarestOpts
	opts.Client = fakeClient(t, legacyDeployment("stratio-datastores", "dg-datarest-pgi", id, 1, "helm.toolkit.fluxcd.io/name", "dg-datarest-pgi"))
	ops, err := planDatarest(context.Background(), opts)
	assertPlan(t, ops, err)
}
