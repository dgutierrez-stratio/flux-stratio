package prepare

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func runtimeInfoConfigMap(domain string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "keos-runtime-info", Namespace: "flux-system"},
		Data:       map[string]string{"CLUSTER_EXTERNAL_DOMAIN": domain},
	}
}

func TestDatarestSatisfied_NoIngressIsSatisfied(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(runtimeInfoConfigMap("eosdev.int")).Build()
	ok, err := datarestSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Satisfied = false, want true (no legacy ingress present)")
	}
}

func TestDatarestSatisfied_IngressPresentIsNotSatisfied(t *testing.T) {
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{
		Name: "dg-datarest-pgi-admin.eosdev.int", Namespace: "stratio-datastores",
	}}
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(runtimeInfoConfigMap("eosdev.int"), ing).Build()
	ok, err := datarestSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Satisfied = true, want false (legacy ingress still present)")
	}
}

func TestDatarestSatisfied_TenantAwareNamespace(t *testing.T) {
	// Ingress lives in the OTHER tenant's namespace: must not satisfy this one.
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{
		Name: "dg-datarest-pgi-admin.eosdev.int", Namespace: "other-datastores",
	}}
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(runtimeInfoConfigMap("eosdev.int"), ing).Build()
	ok, err := datarestSatisfied(context.Background(), Options{TenantName: "stratio", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Satisfied = false, want true (ingress belongs to a different tenant's namespace)")
	}
}

func TestDatarestSatisfied_MissingConfigMapErrors(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).Build()
	if _, err := datarestSatisfied(context.Background(), Options{TenantName: "stratio", Client: c}); err == nil {
		t.Fatal("datarestSatisfied with no keos-runtime-info ConfigMap: got nil error, want non-nil")
	}
}

func TestRunDatarest_DeletesIngressIdempotently(t *testing.T) {
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{
		Name: "dg-datarest-pgi-admin.eosdev.int", Namespace: "stratio-datastores",
	}}
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(runtimeInfoConfigMap("eosdev.int"), ing).Build()

	opts := Options{TenantName: "stratio", Client: c}
	if err := runDatarest(context.Background(), opts); err != nil {
		t.Fatalf("runDatarest returned error: %v", err)
	}
	ok, err := datarestSatisfied(context.Background(), opts)
	if err != nil || !ok {
		t.Errorf("Satisfied after Run = %v, err = %v, want true, nil", ok, err)
	}

	// Running again must be a no-op, not an error.
	if err := runDatarest(context.Background(), opts); err != nil {
		t.Errorf("second runDatarest returned error: %v", err)
	}
}
