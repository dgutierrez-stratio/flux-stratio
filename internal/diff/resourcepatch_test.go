package diff

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func labelled(w *unstructured.Unstructured, labels map[string]any) *unstructured.Unstructured {
	_ = unstructured.SetNestedMap(w.Object, labels, "spec", "template", "metadata", "labels")
	return w
}

func controllersValues(controllers ...string) map[string]any {
	out := map[string]any{}
	for _, c := range controllers {
		out[c] = map[string]any{"containers": map[string]any{"app": map[string]any{}}}
	}
	return map[string]any{"controllers": out}
}

func TestResourceEdits(t *testing.T) {
	rendered := func(ctrl string) *unstructured.Unstructured {
		return labelled(workload("a", nil, container("app", "r/i:1",
			map[string]any{"cpu": "500m", "memory": "2Gi"}, map[string]any{"memory": "2Gi"})),
			map[string]any{"app.kubernetes.io/controller": ctrl})
	}
	live := workload("a", nil, container("legacy", "r/i:1", map[string]any{"cpu": "4"}, map[string]any{"cpu": "8", "memory": "4.096E9"}))

	t.Run("writes the live requests and limits, null for what live lacks", func(t *testing.T) {
		patch, edits := resourceEdits(controllersValues("main"), ChartDiffInput{Live: []LiveWorkloadEnv{{Rendered: rendered("main"), Live: live}}})
		want := map[string]any{"controllers": map[string]any{"main": map[string]any{"containers": map[string]any{"app": map[string]any{"resources": map[string]any{
			"requests": map[string]any{"cpu": "4", "memory": nil},
			"limits":   map[string]any{"cpu": "8", "memory": "4096M"},
		}}}}}}
		if !reflect.DeepEqual(patch, want) {
			t.Errorf("patch = %#v, want %#v", patch, want)
		}
		if len(edits) != 1 || edits[0].Path != "controllers.main.containers.app.resources" || edits[0].Workload != "a" || edits[0].Container != "app" {
			t.Errorf("edits = %+v", edits)
		}
	})

	t.Run("two controllers declaring the container are told apart by the pod labels", func(t *testing.T) {
		_, edits := resourceEdits(controllersValues("main", "other"), ChartDiffInput{Live: []LiveWorkloadEnv{{Rendered: rendered("other"), Live: live}}})
		if len(edits) != 1 || edits[0].Path != "controllers.other.containers.app.resources" {
			t.Errorf("edits = %+v, want controllers.other", edits)
		}
	})

	t.Run("an ambiguous controller, no controllers, or an excluded path carry nothing", func(t *testing.T) {
		for name, in := range map[string]struct {
			values  map[string]any
			exclude []string
			wl      *unstructured.Unstructured
		}{
			"ambiguous":   {controllersValues("main", "other"), nil, rendered("unknown")},
			"none":        {map[string]any{"x": 1}, nil, rendered("main")},
			"excluded":    {controllersValues("main"), []string{"spec.values.controllers.main.containers.app.resources"}, rendered("main")},
			"no live pod": {controllersValues("main"), nil, rendered("main")},
		} {
			l := live
			if name == "no live pod" {
				l = nil
			}
			patch, edits := resourceEdits(in.values, ChartDiffInput{Exclude: in.exclude, Live: []LiveWorkloadEnv{{Rendered: in.wl, Live: l}}})
			if len(patch) != 0 || len(edits) != 0 {
				t.Errorf("%s: patch = %v, edits = %+v, want nothing", name, patch, edits)
			}
		}
	})

	t.Run("equal resources carry nothing", func(t *testing.T) {
		same := workload("a", nil, container("app", "r/i:1", map[string]any{"memory": "2Gi"}, nil))
		patch, edits := resourceEdits(controllersValues("main"), ChartDiffInput{Live: []LiveWorkloadEnv{{Rendered: labelled(same, nil), Live: same}}})
		if len(patch) != 0 || len(edits) != 0 {
			t.Errorf("patch = %v, edits = %+v, want nothing", patch, edits)
		}
	})
}

func TestVerifyResources_TakesBackWhatTheRenderDoesNotReproduce(t *testing.T) {
	rendered := workload("a", nil, container("app", "r/i:1", nil, map[string]any{"memory": "2Gi"}))
	live := workload("a", nil, container("app", "r/i:1", nil, map[string]any{"memory": "4Gi"}))
	liveSide := []LiveWorkloadEnv{{Rendered: rendered, Live: live}}
	newResult := func() *ChartDiffResult {
		patch, edits := resourceEdits(controllersValues("main"), ChartDiffInput{Live: liveSide})
		return &ChartDiffResult{
			Patch:         &PatchDoc{TargetKind: "HelmRelease", Patch: map[string]any{"spec": map[string]any{"values": patch}}},
			ResourceEdits: edits,
		}
	}

	held := newResult()
	if failed := VerifyResources(held, []*unstructured.Unstructured{live}, liveSide); len(failed) != 0 || held.Patch == nil || len(held.ResourceEdits) != 1 {
		t.Errorf("a render equal to live: failed = %+v, patch = %v, edits = %+v, want the edit kept", failed, held.Patch, held.ResourceEdits)
	}

	dropped := newResult()
	failed := VerifyResources(dropped, []*unstructured.Unstructured{rendered}, liveSide)
	if len(failed) != 1 || dropped.Patch != nil || len(dropped.ResourceEdits) != 0 {
		t.Errorf("a render still at 2Gi: failed = %+v, patch = %v, edits = %+v, want the edit and the empty patch gone", failed, dropped.Patch, dropped.ResourceEdits)
	}
}

func TestMergePatch_NullDeletesAndMapsMerge(t *testing.T) {
	values := map[string]any{"a": map[string]any{"keep": 1, "drop": 2, "nested": map[string]any{"x": 1}}, "list": []any{1}}
	patch := map[string]any{"a": map[string]any{"drop": nil, "nested": map[string]any{"y": 2}}, "list": []any{2}}
	want := map[string]any{"a": map[string]any{"keep": 1, "nested": map[string]any{"x": 1, "y": 2}}, "list": []any{2}}
	if got := MergePatch(values, patch); !reflect.DeepEqual(got, want) {
		t.Errorf("MergePatch = %v, want %v", got, want)
	}
	added := MergePatch(map[string]any{}, map[string]any{"limits": map[string]any{"cpu": "1", "memory": nil}})
	if want := map[string]any{"limits": map[string]any{"cpu": "1"}}; !reflect.DeepEqual(added, want) {
		t.Errorf("a null inside a new map: MergePatch = %v, want %v (kustomize drops it)", added, want)
	}
	if _, ok := values["a"].(map[string]any)["drop"]; !ok {
		t.Error("MergePatch modified its input")
	}
}
