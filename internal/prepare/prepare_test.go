package prepare

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func mustScheme(t *testing.T) *apiruntime.Scheme {
	t.Helper()
	s := apiruntime.NewScheme()
	for _, add := range []func(*apiruntime.Scheme) error{
		corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func fakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(objs...).Build()
}

// object builds a live object; labels are key, value pairs.
func object(gvk schema.GroupVersionKind, namespace, name string, labels ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetUID(types.UID("uid-" + name))
	l := map[string]string{}
	for i := 0; i+1 < len(labels); i += 2 {
		l[labels[i]] = labels[i+1]
	}
	u.SetLabels(l)
	return u
}

// legacyDeployment is a CCT Deployment with n replicas whose pods its
// selector (the app id label, as CCT sets it) matches.
func legacyDeployment(namespace, name, appID string, n int64, extraLabels ...string) *unstructured.Unstructured {
	u := object(gvkDeployment, namespace, name, append([]string{cctAppIDLabel, appID}, extraLabels...)...)
	_ = unstructured.SetNestedField(u.Object, n, "spec", "replicas")
	_ = unstructured.SetNestedStringMap(u.Object, map[string]string{cctAppIDLabel: appID}, "spec", "selector", "matchLabels")
	_ = unstructured.SetNestedStringMap(u.Object, map[string]string{cctAppIDLabel: appID}, "spec", "template", "metadata", "labels")
	return u
}

func opStrings(ops []Operation) []string {
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = op.String()
	}
	return out
}

func assertPlan(t *testing.T, ops []Operation, err error, want ...string) {
	t.Helper()
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if got := opStrings(ops); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("plan:\n got  %q\n want %q", got, want)
	}
}

func applyAll(t *testing.T, c client.Client, ops []Operation) {
	t.Helper()
	for _, op := range ops {
		if err := op.Apply(context.Background(), c); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
}

func TestFind(t *testing.T) {
	for _, name := range []string{"prepare-datamarket-agent", "prepare-datarest", "prepare-dlc", "prepare-genai"} {
		if Find(name) == nil {
			t.Errorf("Find(%q) = nil, want a step", name)
		}
	}
	if Find("does-not-exist") != nil {
		t.Error("Find(\"does-not-exist\") = non-nil, want nil")
	}
}

func TestSteps_ExactlyOneShapePerStep(t *testing.T) {
	want := map[string]string{
		"prepare-datamarket-agent": "automated",
		"prepare-datarest":         "automated",
		"prepare-dlc":              "automated",
		"prepare-genai":            "query",
	}
	for name, wantShape := range want {
		step := Find(name)
		if step == nil {
			t.Fatalf("Find(%q) = nil", name)
		}
		shapes := map[string]bool{
			"automated": step.Automated && step.Plan != nil,
			"manual":    !step.Automated && step.Instructions != "",
			"query":     !step.Automated && step.Query != nil,
		}
		if !shapes[wantShape] {
			t.Errorf("%s: not shaped as %s (Automated=%v Plan=%v Instructions=%q Query=%v)",
				name, wantShape, step.Automated, step.Plan != nil, step.Instructions, step.Query)
		}
		n := 0
		for _, is := range shapes {
			if is {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: exactly one of automated/manual/query should hold, got %d", name, n)
		}
	}
}

func TestLegacyObjects_NeedsALiveObject(t *testing.T) {
	_, err := legacyObjects(context.Background(), Options{Client: fakeClient(t)}, gvkIngress)
	if err == nil {
		t.Error("legacyObjects without LiveName/LiveNamespace: got nil error")
	}
}

func TestOperation_ManifestDropsServerBookkeeping(t *testing.T) {
	obj := object(gvkIngress, "ns", "ing")
	obj.SetAnnotations(map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}", "keep": "me"})
	_ = unstructured.SetNestedSlice(obj.Object, []any{map[string]any{"manager": "x"}}, "metadata", "managedFields")
	_ = unstructured.SetNestedField(obj.Object, "Ready", "status", "phase")

	data, err := deleteOp(obj).Manifest()
	if err != nil {
		t.Fatal(err)
	}
	m := string(data)
	for _, gone := range []string{"managedFields", "last-applied-configuration", "status"} {
		if strings.Contains(m, gone) {
			t.Errorf("manifest still has %s:\n%s", gone, m)
		}
	}
	if !strings.Contains(m, "keep: me") || !strings.Contains(m, "kind: Ingress") {
		t.Errorf("manifest lost the object itself:\n%s", m)
	}
}

// TestOperations_PinnedToTheObjectPlanned: a delete carries a UID
// precondition and a patch the object's UID, so the API server refuses
// either if the object was replaced after it was planned (the fake client
// doesn't enforce them, so the requests themselves are checked).
func TestOperations_PinnedToTheObjectPlanned(t *testing.T) {
	obj := legacyDeployment("ns", "dep", "dep.ns", 1)
	var deleteUID, patchBody string
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(obj).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
			do := &client.DeleteOptions{}
			do.ApplyOptions(opts)
			if do.Preconditions != nil && do.Preconditions.UID != nil {
				deleteUID = string(*do.Preconditions.UID)
			}
			return c.Delete(ctx, o, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			data, _ := p.Data(o)
			patchBody = string(data)
			return c.Patch(ctx, o, p, opts...)
		},
	}).Build()

	if err := scaleToZeroOp(obj).Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patchBody, `"uid":"uid-dep"`) || !strings.Contains(patchBody, `"replicas":0`) {
		t.Errorf("patch = %s, want it pinned to uid-dep and scaling to 0", patchBody)
	}
	if err := deleteOp(obj).Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if deleteUID != "uid-dep" {
		t.Errorf("delete UID precondition = %q, want uid-dep", deleteUID)
	}
}

func TestWaitForNoPods(t *testing.T) {
	sel := map[string]string{"app": "x"}
	pod := &corev1.Pod{}
	pod.Namespace, pod.Name, pod.Labels = "ns", "x-1", sel

	if err := waitForNoPods(context.Background(), fakeClient(t), "ns", sel, time.Second); err != nil {
		t.Errorf("no pods: %v", err)
	}
	if err := waitForNoPods(context.Background(), fakeClient(t, pod), "ns", sel, time.Millisecond); err == nil {
		t.Error("a matching pod never terminating: got nil error, want a timeout")
	}
	if err := waitForNoPods(context.Background(), fakeClient(t), "ns", nil, time.Second); err == nil {
		t.Error("an empty selector: got nil error — it would match every pod in the namespace")
	}
}

func TestDeleteOp_AlreadyGoneIsDone(t *testing.T) {
	if err := deleteOp(object(gvkIngress, "ns", "gone")).Apply(context.Background(), fakeClient(t)); err != nil {
		t.Errorf("deleting an object that's already gone: %v, want nil", err)
	}
}

func TestOperation_ApplyErrorNamesTheOperation(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			return errors.New("forbidden")
		},
	}).Build()
	err := deleteOp(object(gvkIngress, "ns", "ing")).Apply(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "delete Ingress ns/ing") || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("err = %v, want it to name the operation and carry the cause", err)
	}
}

// failingReads is a client whose List and Get fail, for every step's
// error path: a plan that can't read live state must fail, never come back
// empty (which would read as "already satisfied").
func failingReads(t *testing.T) client.Client {
	boom := errors.New("apiserver unavailable")
	return fake.NewClientBuilder().WithScheme(mustScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error { return boom },
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return boom
		},
	}).Build()
}

func TestPlans_ReadFailuresAreErrorsNotSatisfied(t *testing.T) {
	for _, step := range Steps {
		if !step.Automated {
			continue
		}
		opts := Options{TenantName: "stratio", LiveName: "app", LiveNamespace: "ns", Client: failingReads(t)}
		ops, err := step.Plan(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "apiserver unavailable") {
			t.Errorf("%s: err = %v, want the read error", step.Name, err)
		}
		if len(ops) != 0 {
			t.Errorf("%s: planned %v despite failing reads", step.Name, opStrings(ops))
		}
	}
}

func TestWaitForNoPods_ListErrorFails(t *testing.T) {
	if err := waitForNoPods(context.Background(), failingReads(t), "ns", map[string]string{"app": "x"}, time.Second); err == nil {
		t.Error("waitForNoPods with a failing List: got nil error")
	}
}
