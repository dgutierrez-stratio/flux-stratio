package diff

import (
	"reflect"
	"testing"
)

func TestRemoveDotPath(t *testing.T) {
	obj := map[string]any{
		"spec": map[string]any{
			"bootstrap": map[string]any{
				"pgBackup": "x",
				"other":    "y",
			},
		},
	}
	removeDotPath(obj, "spec.bootstrap.pgBackup")
	want := map[string]any{
		"spec": map[string]any{
			"bootstrap": map[string]any{"other": "y"},
		},
	}
	if !reflect.DeepEqual(obj, want) {
		t.Errorf("obj = %+v, want %+v", obj, want)
	}
}

func TestRemoveDotPath_NonexistentPathIsNoOp(t *testing.T) {
	obj := map[string]any{"spec": map[string]any{"a": "b"}}
	removeDotPath(obj, "spec.does.not.exist")
	want := map[string]any{"spec": map[string]any{"a": "b"}}
	if !reflect.DeepEqual(obj, want) {
		t.Errorf("obj = %+v, want unchanged %+v", obj, want)
	}
}

func TestExcludeJSONPatchOps(t *testing.T) {
	ops := []JSONPatchOp{
		{Op: "add", Path: "/spec/secret"},
		{Op: "add", Path: "/spec/secret/nested"},
		{Op: "add", Path: "/spec/keep"},
		{Op: "replace", Path: "/spec/secretOther"}, // must NOT be excluded: not a "/" boundary match
	}
	got := excludeJSONPatchOps(ops, []string{"spec.secret"})
	want := []JSONPatchOp{
		{Op: "add", Path: "/spec/keep"},
		{Op: "replace", Path: "/spec/secretOther"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("excludeJSONPatchOps = %+v, want %+v", got, want)
	}
}

func TestExcludeJSONPatchOps_NoExcludesReturnsSameSlice(t *testing.T) {
	ops := []JSONPatchOp{{Op: "add", Path: "/spec/x"}}
	got := excludeJSONPatchOps(ops, nil)
	if !reflect.DeepEqual(got, ops) {
		t.Errorf("excludeJSONPatchOps with no excludes = %+v, want unchanged %+v", got, ops)
	}
}
