package prepare

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const osAgentID = "opensearch1-agent.stratio-datastores"

var osAgentOpts = Options{TenantName: "stratio", LiveName: "opensearch1-agent", LiveNamespace: "stratio-datastores"}

func TestPlanGosecAgent_ScalesTheLegacyAgentOnly(t *testing.T) {
	opts := osAgentOpts
	c := fakeClient(t, legacyDeployment("stratio-datastores", "opensearch1-agent", osAgentID, 1))
	opts.Client = c

	ops, err := planGosecAgent(context.Background(), opts)
	assertPlan(t, ops, err, "scale to 0 replicas (was 1) Deployment stratio-datastores/opensearch1-agent")

	applyAll(t, c, ops)
	ops, err = planGosecAgent(context.Background(), opts)
	assertPlan(t, ops, err) // scaled down: satisfied
}

// TestPlanGosecAgent_NeverTheKeosCoreAgent: the keos-core agent is applied by
// Flux, carries no CCT label, and must not be planned even when the tenant's
// legacy agent is.
func TestPlanGosecAgent_NeverTheKeosCoreAgent(t *testing.T) {
	opts := osAgentOpts
	opts.Client = fakeClient(t,
		legacyDeployment("stratio-datastores", "opensearch1-agent", osAgentID, 1),
		object(gvkDeployment, "keos-core", "opensearcher-gosec-agent", "helm.toolkit.fluxcd.io/name", "opensearcher-gosec-agent"),
		legacyDeployment("keos-core", "opensearcher-agent", "opensearcher-agent.keos-core", 1, "kustomize.toolkit.fluxcd.io/name", "keos-core"))
	ops, err := planGosecAgent(context.Background(), opts)
	assertPlan(t, ops, err, "scale to 0 replicas (was 1) Deployment stratio-datastores/opensearch1-agent")
}

func TestPlanGosecAgent_AlreadyAtZero(t *testing.T) {
	opts := osAgentOpts
	opts.Client = fakeClient(t, legacyDeployment("stratio-datastores", "opensearch1-agent", osAgentID, 0))
	ops, err := planGosecAgent(context.Background(), opts)
	assertPlan(t, ops, err)
}

// TestPlanGosecAgent_UnsetReplicasIsOne: spec.replicas defaults to 1, so a
// Deployment that doesn't set it is running and is planned.
func TestPlanGosecAgent_UnsetReplicasIsOne(t *testing.T) {
	dep := legacyDeployment("stratio-datastores", "opensearch1-agent", osAgentID, 1)
	unstructured.RemoveNestedField(dep.Object, "spec", "replicas")
	opts := osAgentOpts
	opts.Client = fakeClient(t, dep)
	ops, err := planGosecAgent(context.Background(), opts)
	assertPlan(t, ops, err, "scale to 0 replicas (was 1) Deployment stratio-datastores/opensearch1-agent")
}

func TestPlanGosecAgent_NoSelectorIsRefusedBeforeScaling(t *testing.T) {
	dep := legacyDeployment("stratio-datastores", "opensearch1-agent", osAgentID, 1)
	unstructured.RemoveNestedField(dep.Object, "spec", "selector")
	c := fakeClient(t, dep)
	opts := osAgentOpts
	opts.Client = c
	ops, err := planGosecAgent(context.Background(), opts)
	if err != nil || len(ops) != 1 {
		t.Fatalf("plan: %v, %d ops", err, len(ops))
	}
	if err := ops[0].Apply(context.Background(), c); err == nil {
		t.Error("Apply without a pod selector: got nil error, want a refusal")
	}
}
