package yamldocs

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const sampleDocs = `
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: psql
  namespace: stratio-datastores
spec:
  values:
    foo: bar
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: unrelated-configmap
  namespace: stratio-datastores
data:
  x: "1"
`

func TestDecode(t *testing.T) {
	docs, err := Decode([]byte(sampleDocs))
	if err != nil {
		t.Fatalf("Decode returned error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("len(docs) = %d, want 2", len(docs))
	}
	if docs[0].GetKind() != "HelmRelease" || docs[1].GetKind() != "ConfigMap" {
		t.Errorf("kinds = %s, %s", docs[0].GetKind(), docs[1].GetKind())
	}
}

func TestDecode_SkipsEmptyDocuments(t *testing.T) {
	input := []byte("---\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n---\n")
	docs, err := Decode(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("len(docs) = %d, want 1 (empty documents skipped)", len(docs))
	}
}

func TestDecode_Empty(t *testing.T) {
	docs, err := Decode([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Errorf("len(docs) = %d, want 0", len(docs))
	}
}

func TestFindByName(t *testing.T) {
	docs, err := Decode([]byte(sampleDocs))
	if err != nil {
		t.Fatal(err)
	}
	if got := FindByName(docs, "psql"); got == nil || got.GetKind() != "HelmRelease" {
		t.Errorf("FindByName(psql) = %v", got)
	}
	if got := FindByName(docs, "nope"); got != nil {
		t.Errorf("FindByName(nope) = %v, want nil", got)
	}
}

func TestFindByKind(t *testing.T) {
	docs, err := Decode([]byte(sampleDocs))
	if err != nil {
		t.Fatal(err)
	}
	got := FindByKind(docs, "ConfigMap")
	if len(got) != 1 || got[0].GetName() != "unrelated-configmap" {
		t.Errorf("FindByKind(ConfigMap) = %v", got)
	}
	if got := FindByKind(docs, "Nonexistent"); len(got) != 0 {
		t.Errorf("FindByKind(Nonexistent) = %v, want empty", got)
	}
}

func TestDecode_WholeNumbersBecomeInt64NotFloat64(t *testing.T) {
	// A live cluster fetch (via controller-runtime/apimachinery) decodes a
	// whole JSON number as int64. If Decode produced float64 instead,
	// every integer field would look like a spurious diff against a live
	// object in internal/diff's reflect.DeepEqual comparisons — this
	// pins the fix, not just documents it.
	docs, err := Decode([]byte("apiVersion: v1\nkind: Foo\nmetadata:\n  name: x\nspec:\n  instances: 3\n  ratio: 1.5\n"))
	if err != nil {
		t.Fatal(err)
	}
	spec, _, err := unstructured.NestedMap(docs[0].Object, "spec")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := spec["instances"].(int64); !ok || v != 3 {
		t.Errorf("instances = %v (%T), want int64(3)", spec["instances"], spec["instances"])
	}
	if v, ok := spec["ratio"].(float64); !ok || v != 1.5 {
		t.Errorf("ratio = %v (%T), want float64(1.5) (a fractional number must stay float64)", spec["ratio"], spec["ratio"])
	}
}
