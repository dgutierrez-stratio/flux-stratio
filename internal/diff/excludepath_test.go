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

// TestExcludeJSONPatchOps_AncestorOpLosesTheExcludedField: a missing
// subtree is added in one op on its root; the excluded field inside it
// must still never reach the patch.
func TestExcludeJSONPatchOps_AncestorOpLosesTheExcludedField(t *testing.T) {
	value := map[string]any{
		"pgBackup":  map[string]any{"enabled": true},
		"initdb":    map[string]any{"owner": "app"},
		"emptyLive": map[string]any{},
	}
	ops := []JSONPatchOp{
		{Op: "add", Path: "/spec/bootstrap", Value: value},
		{Op: "add", Path: "/spec/only", Value: map[string]any{"pgBackup": map[string]any{"enabled": true}}},
	}
	got := excludeJSONPatchOps(ops, []string{"spec.bootstrap.pgBackup", "spec.only.pgBackup"})
	want := []JSONPatchOp{{Op: "add", Path: "/spec/bootstrap", Value: map[string]any{
		"initdb":    map[string]any{"owner": "app"},
		"emptyLive": map[string]any{},
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("excludeJSONPatchOps = %+v, want %+v", got, want)
	}
	if _, ok := value["pgBackup"]; !ok {
		t.Error("the op's original value was modified")
	}
}

// TestExcludeJSONPatchOps_EscapedKeys: a key holding "/" or "~" is
// matched through its JSON Pointer escape.
func TestExcludeJSONPatchOps_EscapedKeys(t *testing.T) {
	ops := []JSONPatchOp{
		{Op: "add", Path: "/metadata/annotations/team~1secret"},
		{Op: "add", Path: "/metadata/annotations/a~0b"},
		{Op: "add", Path: "/metadata/annotations/keep"},
	}
	got := excludeJSONPatchOps(ops, []string{"metadata.annotations.team/secret", "metadata.annotations.a~b"})
	want := []JSONPatchOp{{Op: "add", Path: "/metadata/annotations/keep"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("excludeJSONPatchOps = %+v, want %+v", got, want)
	}
}
