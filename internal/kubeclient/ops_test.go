package kubeclient

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestIgnoreNotFound(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "x")
	if err := IgnoreNotFound(notFound); err != nil {
		t.Errorf("IgnoreNotFound(NotFound) = %v, want nil", err)
	}

	other := apierrors.NewBadRequest("boom")
	if err := IgnoreNotFound(other); err != other {
		t.Errorf("IgnoreNotFound(other) = %v, want %v unchanged", err, other)
	}
}

func TestGetUnstructured_CustomResource(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "postgres.stratio.com", Version: "v1", Kind: "PgCluster"}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetName("psql")
	obj.SetNamespace("stratio-datastores")
	_ = unstructured.SetNestedField(obj.Object, "leader-1", "status", "leader")

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(obj).Build()

	got, err := GetUnstructured(context.Background(), c, gvk, "stratio-datastores", "psql")
	if err != nil {
		t.Fatalf("GetUnstructured returned error: %v", err)
	}
	leader, _, _ := unstructured.NestedString(got.Object, "status", "leader")
	if leader != "leader-1" {
		t.Errorf("status.leader = %q, want %q", leader, "leader-1")
	}
}

func TestGetUnstructured_NotFound(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "postgres.stratio.com", Version: "v1", Kind: "PgCluster"}
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).Build()

	_, err := GetUnstructured(context.Background(), c, gvk, "ns", "missing")
	if !IsNotFound(err) {
		t.Errorf("GetUnstructured for a missing object: err = %v, want a NotFound error", err)
	}
}

func TestListUnstructured_AllNamespaces(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "postgres.stratio.com", Version: "v1", Kind: "PgCluster"}

	a := &unstructured.Unstructured{}
	a.SetGroupVersionKind(gvk)
	a.SetNamespace("stratio-datastores")
	a.SetName("psql")

	b := &unstructured.Unstructured{}
	b.SetGroupVersionKind(gvk)
	b.SetNamespace("other-datastores")
	b.SetName("otherpsql")

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(a, b).Build()

	list, err := ListUnstructured(context.Background(), c, gvk, "")
	if err != nil {
		t.Fatalf("ListUnstructured returned error: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(list.Items))
	}
}

func TestListUnstructured_SingleNamespace(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "postgres.stratio.com", Version: "v1", Kind: "PgCluster"}

	a := &unstructured.Unstructured{}
	a.SetGroupVersionKind(gvk)
	a.SetNamespace("stratio-datastores")
	a.SetName("psql")

	b := &unstructured.Unstructured{}
	b.SetGroupVersionKind(gvk)
	b.SetNamespace("other-datastores")
	b.SetName("otherpsql")

	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(a, b).Build()

	list, err := ListUnstructured(context.Background(), c, gvk, "stratio-datastores")
	if err != nil {
		t.Fatalf("ListUnstructured returned error: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].GetName() != "psql" {
		t.Errorf("filtered list = %+v, want just psql in stratio-datastores", list.Items)
	}
}

func TestGetUnstructured_TypedScopeStillWorks(t *testing.T) {
	// Sanity check that registering corev1 alongside unstructured GVKs
	// doesn't break typed access to core types.
	pod := &corev1.Pod{}
	pod.Name = "p"
	pod.Namespace = "ns"
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(pod).Build()

	var got corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "p"}, &got); err != nil {
		t.Fatalf("typed Get returned error: %v", err)
	}
}

func mustScheme(t *testing.T) *apiruntime.Scheme {
	t.Helper()
	s, err := NewScheme()
	if err != nil {
		t.Fatalf("NewScheme returned error: %v", err)
	}
	return s
}

func TestMergePatch(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "ns"},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "d"}}},
	}
	c := fake.NewClientBuilder().WithScheme(mustAppsScheme(t)).WithObjects(dep).Build()

	if err := MergePatch(context.Background(), c, dep, map[string]any{"spec": map[string]any{"paused": true}}); err != nil {
		t.Fatalf("MergePatch returned error: %v", err)
	}

	var got appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "d"}, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Spec.Paused {
		t.Error("Spec.Paused = false, want true after the merge patch")
	}
}

func mustAppsScheme(t *testing.T) *apiruntime.Scheme {
	t.Helper()
	s := apiruntime.NewScheme()
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}
