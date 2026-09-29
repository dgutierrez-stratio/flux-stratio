package prepare

import (
	"context"
	"testing"
)

// TestPlanDLC_FindsTheIngressByLabel: eosdev's legacy Ingress is
// dlc-entity-dlc-entity.stratio.<domain>, not the dlc-entity-dlc.stratio.<domain>
// the Python client looked for; both it and the Deployment are planned.
func TestPlanDLC_FindsTheIngressByLabel(t *testing.T) {
	const id = "dlc-entity.stratio-dlc"
	c := fakeClient(t,
		object(gvkIngress, "stratio-dlc", "dlc-entity-dlc-entity.stratio.eosdev.int", cctAppIDLabel, id),
		legacyDeployment("stratio-dlc", "dlc-entity", id, 1),
	)
	opts := Options{TenantName: "stratio", LiveName: "dlc-entity", LiveNamespace: "stratio-dlc", Client: c}

	ops, err := planDLC(context.Background(), opts)
	assertPlan(t, ops, err,
		"delete Ingress stratio-dlc/dlc-entity-dlc-entity.stratio.eosdev.int",
		"delete Deployment stratio-dlc/dlc-entity")

	applyAll(t, c, ops)
	ops, err = planDLC(context.Background(), opts)
	assertPlan(t, ops, err)
}

// TestPlanDLC_NeverTheGitOpsDeployment: after cutover the GitOps
// dlc-entity Deployment has the legacy one's name; a re-run must not
// delete it.
func TestPlanDLC_NeverTheGitOpsDeployment(t *testing.T) {
	c := fakeClient(t, legacyDeployment("stratio-dlc", "dlc-entity", "dlc-entity.stratio-dlc", 1,
		"helm.toolkit.fluxcd.io/name", "dlc-entity"))
	opts := Options{TenantName: "stratio", LiveName: "dlc-entity", LiveNamespace: "stratio-dlc", Client: c}
	ops, err := planDLC(context.Background(), opts)
	assertPlan(t, ops, err)
}
