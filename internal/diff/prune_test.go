package diff

import (
	"reflect"
	"testing"
)

func TestPruneEmptyMaps_RemovesEmptyMapsOnly(t *testing.T) {
	in := map[string]any{
		"keepFalse": false,
		"keepZero":  0,
		"keepEmpty": "",
		"keepList":  []any{},
		"emptyMap":  map[string]any{},
		"nestedGoesEmpty": map[string]any{
			"onlyChildWasEmptyMap": map[string]any{},
		},
		"nestedStays": map[string]any{
			"a":          "b",
			"emptyChild": map[string]any{},
		},
	}
	got := pruneEmptyMaps(in).(map[string]any)

	want := map[string]any{
		"keepFalse":   false,
		"keepZero":    0,
		"keepEmpty":   "",
		"keepList":    []any{},
		"nestedStays": map[string]any{"a": "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pruneEmptyMaps = %+v, want %+v", got, want)
	}
}

func TestPruneEmptyMaps_NonMapPassthrough(t *testing.T) {
	if got := pruneEmptyMaps("scalar"); got != "scalar" {
		t.Errorf("pruneEmptyMaps(scalar) = %v, want unchanged", got)
	}
	if got := pruneEmptyMaps([]any{1, 2}); !reflect.DeepEqual(got, []any{1, 2}) {
		t.Errorf("pruneEmptyMaps(list) = %v, want unchanged", got)
	}
}
