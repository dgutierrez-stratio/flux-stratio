package diff

import (
	"reflect"
	"testing"
)

func TestSettledByPatch(t *testing.T) {
	// Rendered again with the patched image tag: the composite image now
	// matches live, the other variable still differs.
	patched := singleWorkload(map[string]any{
		"ROCKET_API_DOCKER_IMAGE": "registry/rocket-api:4.1.0",
		"OTHER":                   "rendered",
	})
	unmapped := []UnmappedDiff{
		{Workload: "app", Name: "ROCKET_API_DOCKER_IMAGE", Rendered: "registry/rocket-api:4.0.1", Live: "registry/rocket-api:4.1.0", Reason: UnmappedNoPath},
		{Workload: "app", Name: "OTHER", Rendered: "rendered", Live: "live", Reason: UnmappedNoPath},
		// From a flat baseline: settled when every workload setting it
		// renders the live value, kept otherwise.
		{Name: "ROCKET_API_DOCKER_IMAGE", Rendered: "registry/rocket-api:4.0.1", Live: "registry/rocket-api:4.1.0", Reason: UnmappedAmbiguous},
		{Name: "OTHER", Rendered: "rendered", Live: "live", Reason: UnmappedAmbiguous},
		{Name: "NOT_RENDERED", Rendered: "a", Live: "b", Reason: UnmappedAmbiguous},
	}

	got := SettledByPatch(unmapped, patched)
	want := []UnmappedDiff{unmapped[1], unmapped[3], unmapped[4]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SettledByPatch = %+v, want %+v", got, want)
	}
}

func TestMergeValues(t *testing.T) {
	dst := map[string]any{
		"controllers": map[string]any{"rocket": map[string]any{"image": map[string]any{"tag": "4.0.1", "repository": "rocket"}}},
		"replicas":    1,
	}
	src := map[string]any{
		"controllers": map[string]any{"rocket": map[string]any{"image": map[string]any{"tag": "4.1.0"}}},
		"list":        []any{"a"},
	}
	got := MergeValues(dst, src)
	want := map[string]any{
		"controllers": map[string]any{"rocket": map[string]any{"image": map[string]any{"tag": "4.1.0", "repository": "rocket"}}},
		"replicas":    1,
		"list":        []any{"a"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MergeValues = %+v, want %+v", got, want)
	}
	if tag := dst["controllers"].(map[string]any)["rocket"].(map[string]any)["image"].(map[string]any)["tag"]; tag != "4.0.1" {
		t.Errorf("MergeValues modified dst: tag = %v", tag)
	}
}

func TestPatchValues(t *testing.T) {
	values := map[string]any{"a": map[string]any{"b": "c", "port": 13422, "enabled": true}}
	p := &PatchDoc{TargetKind: "HelmRelease", Patch: map[string]any{"spec": map[string]any{"values": values}}}
	if got := PatchValues(p); !reflect.DeepEqual(got, values) {
		t.Errorf("PatchValues = %+v, want %+v", got, values)
	}
	if got := PatchValues(nil); got != nil {
		t.Errorf("PatchValues(nil) = %+v, want nil", got)
	}
}
