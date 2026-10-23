package appdiff

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Stratio/flux-stratio/internal/diff"
)

// selectedDeployment is a Deployment selecting on the given labels.
func selectedDeployment(namespace, name string, replicas int64, labels map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"replicas": replicas,
			"selector": map[string]any{"matchLabels": labels},
		},
	}}
	if namespace != "" {
		u.SetNamespace(namespace)
	}
	return u
}

var (
	chartSelector  = map[string]any{"app.kubernetes.io/name": "x"}
	legacySelector = map[string]any{"cct.stratio.com/application_id": "x.ns"}
)

// TestSpecDiffs_SelectorOnlyAgainstTheSameObject: an immutable selector is an
// obstacle only when the release meets a live workload of the same kind,
// namespace and name as the one it renders. Another name or namespace (the
// rendered workload paired with a differently named legacy one) is a note
// that the legacy workload keeps running, never a selector difference.
func TestSpecDiffs_SelectorOnlyAgainstTheSameObject(t *testing.T) {
	for _, tt := range []struct {
		name         string
		rendered     *unstructured.Unstructured
		live         *unstructured.Unstructured
		wantSelector bool
		wantLiveRef  string
	}{
		{
			"same name and namespace (eureka-agent, rocket, genai, ...)",
			selectedDeployment("stratio-datastores", "eureka-agent", 1, chartSelector),
			selectedDeployment("stratio-datastores", "eureka-agent", 1, legacySelector),
			true, "Deployment stratio-datastores/eureka-agent",
		},
		{
			"the rendered object has no namespace: the release's",
			selectedDeployment("", "eureka-agent", 1, chartSelector),
			selectedDeployment("stratio-datastores", "eureka-agent", 1, legacySelector),
			true, "Deployment stratio-datastores/eureka-agent",
		},
		{
			"another namespace and name (governance-datamarket-agent)",
			selectedDeployment("stratio-apps", "governance-datamarket-agent", 1, chartSelector),
			selectedDeployment("stratio-datastores", "datamarket-agent", 1, legacySelector),
			false, "Deployment stratio-datastores/datamarket-agent",
		},
		{
			"another name (psql-gosec-agent, connectors)",
			selectedDeployment("stratio-datastores", "psql-gosec-agent", 1, chartSelector),
			selectedDeployment("stratio-datastores", "psql-agent", 2, legacySelector),
			false, "Deployment stratio-datastores/psql-agent",
		},
		{
			"another namespace, same name",
			selectedDeployment("stratio-apps", "connectors", 1, chartSelector),
			selectedDeployment("stratio-datastores", "connectors", 1, legacySelector),
			false, "Deployment stratio-datastores/connectors",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			live := []diff.LiveWorkloadEnv{{Rendered: tt.rendered, Live: tt.live}}
			got := specsByField(specDiffs(Options{}, "stratio-datastores", live, []*unstructured.Unstructured{tt.rendered}))

			sel, hasSelector := got[diff.SelectorField]
			note, hasNote := got[diff.LegacyRunsField]
			if hasSelector != tt.wantSelector || hasNote == tt.wantSelector {
				t.Fatalf("selector diff = %v, legacy note = %v; want selector %v and the opposite note", hasSelector, hasNote, tt.wantSelector)
			}
			d := note
			if tt.wantSelector {
				d = sel
				if d.Severity != diff.SpecRisk {
					t.Errorf("selector severity = %s, want risk", d.Severity)
				}
			} else if d.Severity != diff.SpecInfo {
				t.Errorf("legacy note = %+v, want an info", d)
			}
			if d.LiveRef != tt.wantLiveRef {
				t.Errorf("LiveRef = %q, want %q (the legacy object, not the rendered name)", d.LiveRef, tt.wantLiveRef)
			}
		})
	}
}

func TestSpecDiffs_LegacyNoteCarriesTheReplicas(t *testing.T) {
	rendered := selectedDeployment("stratio-datastores", "psql-gosec-agent", 1, chartSelector)
	live := selectedDeployment("stratio-datastores", "psql-agent", 2, legacySelector)
	got := specsByField(specDiffs(Options{}, "stratio-datastores", []diff.LiveWorkloadEnv{{Rendered: rendered, Live: live}}, []*unstructured.Unstructured{rendered}))
	n := got[diff.LegacyRunsField]
	if n.Live != "2" || n.Rendered != "Deployment stratio-datastores/psql-gosec-agent" {
		t.Errorf("note = %+v, want the legacy replicas (2) and the new workload", n)
	}
}
