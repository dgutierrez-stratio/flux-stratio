package prepare

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func dlcIngress(namespace string) *networkingv1.Ingress {
	return &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "dlc-entity-dlc.stratio.eosdev.int", Namespace: namespace}}
}

func dlcDeployment(namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "dlc-entity", Namespace: namespace}}
}

func TestDLCSatisfied_NothingPresentIsSatisfied(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(runtimeInfoConfigMap("eosdev.int")).Build()
	ok, err := dlcSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Satisfied = false, want true")
	}
}

func TestDLCSatisfied_IngressPresentIsNotSatisfied(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).
		WithObjects(runtimeInfoConfigMap("eosdev.int"), dlcIngress("stratio-dlc")).Build()
	ok, err := dlcSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Satisfied = true, want false (ingress still present)")
	}
}

func TestDLCSatisfied_DeploymentPresentIsNotSatisfied(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).
		WithObjects(runtimeInfoConfigMap("eosdev.int"), dlcDeployment("stratio-dlc")).Build()
	ok, err := dlcSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Satisfied = true, want false (deployment still present)")
	}
}

func TestDLCSatisfied_TenantAware(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).
		WithObjects(runtimeInfoConfigMap("eosdev.int"), dlcIngress("other-dlc"), dlcDeployment("other-dlc")).Build()
	ok, err := dlcSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Satisfied = false, want true (resources belong to a different tenant's namespace)")
	}
}

func TestRunDLC_DeletesBothIdempotently(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).
		WithObjects(runtimeInfoConfigMap("eosdev.int"), dlcIngress("stratio-dlc"), dlcDeployment("stratio-dlc")).Build()
	opts := Options{TenantName: "stratio", Client: c}

	if err := runDLC(context.Background(), opts); err != nil {
		t.Fatalf("runDLC returned error: %v", err)
	}
	ok, err := dlcSatisfied(context.Background(), opts)
	if err != nil || !ok {
		t.Errorf("Satisfied after Run = %v, err = %v, want true, nil", ok, err)
	}

	if err := runDLC(context.Background(), opts); err != nil {
		t.Errorf("second runDLC returned error: %v", err)
	}
}
