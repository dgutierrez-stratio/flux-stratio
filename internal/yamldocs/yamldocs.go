// Package yamldocs decodes a multi-document YAML stream — the output of
// `flux-operator build rset`, `flux build kustomization --dry-run`, or
// `helm template` — into Kubernetes objects, and provides the small
// lookup helpers every caller of those needs.
package yamldocs

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// Decode splits data on YAML document boundaries — robust to a
// "---"-looking line inside a block scalar, unlike a naive string split —
// and decodes each non-empty document into an Unstructured object.
//
// Each document is unmarshaled into the *unstructured.Unstructured value
// itself, not into &obj.Object as a bare map — that difference matters:
// Unstructured.UnmarshalJSON decodes numbers the way a live cluster fetch
// does (k8s.io/apimachinery/pkg/util/json: a whole number becomes int64,
// only a fractional one becomes float64), while unmarshaling into a bare
// map falls back to encoding/json's default of float64 for every number.
// A rendered "instances: 3" must compare equal to a live "instances: 3"
// in internal/diff's reflect.DeepEqual checks; decoding both sides
// through the same numeric convention is what makes that true instead of
// every integer field looking like a spurious diff.
func Decode(data []byte) ([]*unstructured.Unstructured, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var docs []*unstructured.Unstructured
	for {
		raw, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(raw, obj); err != nil {
			return nil, err
		}
		if obj.Object == nil {
			continue
		}
		docs = append(docs, obj)
	}
	return docs, nil
}

// FindByName returns the first object in docs whose metadata.name equals
// name, or nil.
func FindByName(docs []*unstructured.Unstructured, name string) *unstructured.Unstructured {
	for _, d := range docs {
		if d.GetName() == name {
			return d
		}
	}
	return nil
}

// FindByKind returns every object in docs whose kind equals kind, in the
// same order they appear in docs.
func FindByKind(docs []*unstructured.Unstructured, kind string) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, d := range docs {
		if d.GetKind() == kind {
			out = append(out, d)
		}
	}
	return out
}
