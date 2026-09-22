package prepare

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func replicas(n int32) *int32 { return &n }

func deploymentWithReplicas(name, namespace string, n int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: replicas(n),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
		},
	}
}

func TestDatamarketAgentSatisfied_NoDeploymentIsSatisfied(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).Build()
	ok, err := datamarketAgentSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Satisfied = false, want true (nothing to suspend)")
	}
}

func TestDatamarketAgentSatisfied_ScaledToZeroIsSatisfied(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).
		WithObjects(deploymentWithReplicas("datamarket-agent", "stratio-datastores", 0)).Build()
	ok, err := datamarketAgentSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Satisfied = false, want true (already scaled to 0)")
	}
}

func TestDatamarketAgentSatisfied_StillRunningIsNotSatisfied(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).
		WithObjects(deploymentWithReplicas("datamarket-agent", "stratio-datastores", 2)).Build()
	ok, err := datamarketAgentSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Satisfied = true, want false (still running with 2 replicas)")
	}
}

func TestDatamarketAgentSatisfied_IsTenantAware(t *testing.T) {
	// A Deployment in a DIFFERENT tenant's namespace must not satisfy this
	// tenant's check — the Python client hardcoded "stratio-datastores".
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).
		WithObjects(deploymentWithReplicas("datamarket-agent", "other-datastores", 3)).Build()
	ok, err := datamarketAgentSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Satisfied = false, want true (the running deployment belongs to a different tenant)")
	}
}

func TestRunDatamarketAgent_SuspendsAndScalesDown(t *testing.T) {
	hr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "datamarket-agent", "namespace": "stratio-datastores"},
		"spec":     map[string]any{},
	}}
	dep := deploymentWithReplicas("datamarket-agent", "stratio-datastores", 2)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(hr, dep).Build()

	if err := runDatamarketAgent(context.Background(), Options{TenantName: "stratio", Client: c}); err != nil {
		t.Fatalf("runDatamarketAgent returned error: %v", err)
	}

	ok, err := datamarketAgentSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Satisfied = false after Run, want true")
	}
}

func TestRunDatamarketAgent_NothingToDoIsANoOp(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).Build()
	if err := runDatamarketAgent(context.Background(), Options{TenantName: "stratio", Client: c}); err != nil {
		t.Fatalf("runDatamarketAgent returned error: %v", err)
	}
}
