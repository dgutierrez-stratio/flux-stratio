package diff

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDeepDiff_LiveWinsAndDirection(t *testing.T) {
	local := map[string]any{
		"onlyLocal": "x",
		"same":      "y",
		"different": "local-value",
		"nested":    map[string]any{"a": "1", "b": "2"},
	}
	live := map[string]any{
		"onlyLive":  "z",
		"same":      "y",
		"different": "live-value",
		"nested":    map[string]any{"a": "1", "b": "changed"},
	}
	got := DeepDiff(local, live)
	want := map[string]any{
		"onlyLive":  "z",
		"different": "live-value",
		"nested":    map[string]any{"b": "changed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeepDiff = %+v, want %+v", got, want)
	}
}

func TestDeepDiff_NoDifference(t *testing.T) {
	same := map[string]any{"a": "1", "b": map[string]any{"c": "2"}}
	if got := DeepDiff(same, same); len(got) != 0 {
		t.Errorf("DeepDiff of identical maps = %+v, want empty", got)
	}
}

func TestNeedsJSON6902_TopLevelListOnBothSides(t *testing.T) {
	local := map[string]any{"items": []any{"a"}}
	live := map[string]any{"items": []any{"a", "b"}}
	if !NeedsJSON6902(local, live) {
		t.Error("NeedsJSON6902 = false, want true (both sides have a list at the same key)")
	}
}

func TestNeedsJSON6902_NotRecursive(t *testing.T) {
	local := map[string]any{"values": map[string]any{"items": []any{"a"}}}
	live := map[string]any{"values": map[string]any{"items": []any{"a", "b"}}}
	if NeedsJSON6902(local, live) {
		t.Error("NeedsJSON6902 = true, want false (the list is nested, not top-level)")
	}
}

func TestNeedsJSON6902_ListOnOnlyOneSide(t *testing.T) {
	local := map[string]any{"items": []any{"a"}}
	live := map[string]any{"items": "not-a-list"}
	if NeedsJSON6902(local, live) {
		t.Error("NeedsJSON6902 = true, want false (list on only one side)")
	}
}

func TestToJSON6902Ops_AddMissingKey(t *testing.T) {
	local := map[string]any{}
	live := map[string]any{"replicas": float64(3)}
	ops := ToJSON6902Ops(local, live, "/spec")
	want := []JSONPatchOp{{Op: "add", Path: "/spec/replicas", Value: float64(3)}}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("ops = %+v, want %+v", ops, want)
	}
}

func TestToJSON6902Ops_ReplaceScalarMismatch(t *testing.T) {
	local := map[string]any{"replicas": float64(1)}
	live := map[string]any{"replicas": float64(3)}
	ops := ToJSON6902Ops(local, live, "/spec")
	want := []JSONPatchOp{{Op: "replace", Path: "/spec/replicas", Value: float64(3)}}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("ops = %+v, want %+v", ops, want)
	}
}

func TestToJSON6902Ops_LocalOnlyKeySkipped(t *testing.T) {
	local := map[string]any{"deprecated": "x"}
	live := map[string]any{}
	if ops := ToJSON6902Ops(local, live, "/spec"); len(ops) != 0 {
		t.Errorf("ops = %+v, want empty (never emit remove)", ops)
	}
}

func TestToJSON6902Ops_ListAddPastEnd(t *testing.T) {
	local := map[string]any{"nodes": []any{map[string]any{"name": "a"}}}
	live := map[string]any{"nodes": []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}}}
	ops := ToJSON6902Ops(local, live, "/spec")
	want := []JSONPatchOp{{Op: "add", Path: "/spec/nodes/1", Value: map[string]any{"name": "b"}}}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("ops = %+v, want %+v", ops, want)
	}
}

func TestToJSON6902Ops_ListRecursesIntoDictElements(t *testing.T) {
	local := map[string]any{"nodes": []any{map[string]any{"name": "a", "role": "replica"}}}
	live := map[string]any{"nodes": []any{map[string]any{"name": "a", "role": "leader"}}}
	ops := ToJSON6902Ops(local, live, "/spec")
	want := []JSONPatchOp{{Op: "replace", Path: "/spec/nodes/0/role", Value: "leader"}}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("ops = %+v, want %+v", ops, want)
	}
}

func TestEscapeJSONPointer(t *testing.T) {
	cases := map[string]string{
		"plain":       "plain",
		"a/b":         "a~1b",
		"a~b":         "a~0b",
		"a~/b":        "a~0~1b",
		"weird~1/lit": "weird~01~1lit",
	}
	for in, want := range cases {
		if got := escapeJSONPointer(in); got != want {
			t.Errorf("escapeJSONPointer(%q) = %q, want %q", in, got, want)
		}
	}
}

func newUnstructured(apiVersion, kind, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
}

func TestManifestDiff_StrategicMergeMode(t *testing.T) {
	local := newUnstructured("postgres.stratio.com/v1", "PgCluster", "psql", map[string]any{
		"instances": float64(1),
		"bootstrap": map[string]any{"pgBackup": "excluded-value"},
	})
	live := newUnstructured("postgres.stratio.com/v1", "PgCluster", "psql", map[string]any{
		"instances": float64(3),
		"bootstrap": map[string]any{"pgBackup": "live-excluded-value"},
	})

	patch, err := ManifestDiff(local, live, []string{"spec.bootstrap.pgBackup"})
	if err != nil {
		t.Fatalf("ManifestDiff returned error: %v", err)
	}
	if patch == nil {
		t.Fatal("patch = nil, want a patch for the changed instances field")
	}
	body, ok := patch.Patch.(map[string]any)
	if !ok {
		t.Fatalf("patch.Patch = %T, want map[string]any", patch.Patch)
	}
	spec, _ := body["spec"].(map[string]any)
	if spec["instances"] != float64(3) {
		t.Errorf("spec.instances = %v, want 3", spec["instances"])
	}
	if _, excluded := spec["bootstrap"]; excluded {
		t.Errorf("spec.bootstrap should have been excluded and pruned entirely, got %v", spec["bootstrap"])
	}
	if patch.TargetKind != "PgCluster" {
		t.Errorf("TargetKind = %q, want %q", patch.TargetKind, "PgCluster")
	}
}

func TestManifestDiff_NoDifferenceReturnsNil(t *testing.T) {
	local := newUnstructured("v1", "Foo", "x", map[string]any{"a": "1"})
	live := newUnstructured("v1", "Foo", "x", map[string]any{"a": "1"})
	patch, err := ManifestDiff(local, live, nil)
	if err != nil {
		t.Fatal(err)
	}
	if patch != nil {
		t.Errorf("patch = %+v, want nil", patch)
	}
}

func TestManifestDiff_EverythingExcludedReturnsNil(t *testing.T) {
	local := newUnstructured("v1", "Foo", "x", map[string]any{"a": "1"})
	live := newUnstructured("v1", "Foo", "x", map[string]any{"a": "2"})
	patch, err := ManifestDiff(local, live, []string{"spec.a"})
	if err != nil {
		t.Fatal(err)
	}
	if patch != nil {
		t.Errorf("patch = %+v, want nil (the only diff was excluded)", patch)
	}
}

func TestManifestDiff_JSON6902ModeForTopLevelLists(t *testing.T) {
	local := newUnstructured("postgres.stratio.com/v1", "PgCluster", "psql", map[string]any{
		"nodes": []any{map[string]any{"name": "a"}},
	})
	live := newUnstructured("postgres.stratio.com/v1", "PgCluster", "psql", map[string]any{
		"nodes": []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}},
	})

	patch, err := ManifestDiff(local, live, nil)
	if err != nil {
		t.Fatal(err)
	}
	if patch == nil {
		t.Fatal("patch = nil, want ops adding the new node")
	}
	ops, ok := patch.Patch.([]JSONPatchOp)
	if !ok {
		t.Fatalf("patch.Patch = %T, want []JSONPatchOp", patch.Patch)
	}
	if len(ops) != 1 || ops[0].Op != "add" || ops[0].Path != "/spec/nodes/1" {
		t.Errorf("ops = %+v, want a single add at /spec/nodes/1", ops)
	}
}

func TestManifestDiff_JSON6902ModeExcludePathsApplied(t *testing.T) {
	local := newUnstructured("v1", "Foo", "x", map[string]any{
		"items":   []any{"a"},
		"secret":  "local",
		"another": "same",
	})
	live := newUnstructured("v1", "Foo", "x", map[string]any{
		"items":   []any{"a", "b"},
		"secret":  "live-value",
		"another": "same",
	})

	patch, err := ManifestDiff(local, live, []string{"spec.secret"})
	if err != nil {
		t.Fatal(err)
	}
	ops, ok := patch.Patch.([]JSONPatchOp)
	if !ok {
		t.Fatalf("patch.Patch = %T, want []JSONPatchOp", patch.Patch)
	}
	for _, op := range ops {
		if op.Path == "/spec/secret" {
			t.Errorf("excluded path /spec/secret leaked into ops: %+v", ops)
		}
	}
	if len(ops) != 1 || ops[0].Path != "/spec/items/1" {
		t.Errorf("ops = %+v, want a single add at /spec/items/1", ops)
	}
}
