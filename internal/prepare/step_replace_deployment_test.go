package prepare

import (
	"context"
	"testing"
)

// The Deployment-replacing steps (dg-agent, discovery, eureka-agent,
// intelligence; prepare-litellm does
// the same after its Vault moves) behave the same: the legacy Deployment
// goes, found by its CCT app id; the GitOps one that replaces it under the
// same name, another app's Deployment and every Ingress stay, and a re-run
// plans nothing.
func TestReplaceLegacyDeploymentSteps(t *testing.T) {
	for _, tt := range []struct {
		step, name, namespace string
	}{
		{"prepare-dg-agent", "dg-hdfs-agent", "stratio-datastores"},
		{"prepare-discovery", "discovery", "stratio-apps"},
		{"prepare-eureka-agent", "eureka-agent", "stratio-datastores"},
		{"prepare-intelligence", "intelligence", "stratio-intelligence"},
	} {
		t.Run(tt.step, func(t *testing.T) {
			step := Find(tt.step)
			if step == nil || !step.Automated || !step.ReplacesWorkloads || step.Plan == nil {
				t.Fatalf("%s = %+v, want an automated step that replaces workloads", tt.step, step)
			}
			id := tt.name + "." + tt.namespace
			c := fakeClient(t,
				legacyDeployment(tt.namespace, tt.name, id, 1),
				legacyDeployment(tt.namespace, "someone-else", "someone-else."+tt.namespace, 1),
				object(gvkIngress, tt.namespace, tt.name+"-ingress", cctAppIDLabel, id),
			)
			opts := Options{TenantName: "stratio", LiveName: tt.name, LiveNamespace: tt.namespace, Client: c}

			ops, err := step.Plan(context.Background(), opts)
			assertPlan(t, ops, err, "delete Deployment "+tt.namespace+"/"+tt.name)

			applyAll(t, c, ops)
			ops, err = step.Plan(context.Background(), opts)
			assertPlan(t, ops, err)
		})
	}
}

// After the cutover the GitOps Deployment has the legacy one's name; a
// re-run must not delete it.
func TestReplaceLegacyDeploymentSteps_NeverTheGitOpsDeployment(t *testing.T) {
	for _, name := range []string{"prepare-dg-agent", "prepare-discovery", "prepare-eureka-agent", "prepare-intelligence"} {
		c := fakeClient(t, legacyDeployment("ns", "app", "app.ns", 1, "helm.toolkit.fluxcd.io/name", "app"))
		ops, err := Find(name).Plan(context.Background(), Options{TenantName: "stratio", LiveName: "app", LiveNamespace: "ns", Client: c})
		assertPlan(t, ops, err)
	}
}
