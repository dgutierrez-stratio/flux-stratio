package prepare

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const virtualizerNS = "stratio-apps"

// isVirtualizerFamily stands in for config.App.Family: CCT's annotations
// on the Deployments of the virtualizer service.
func isVirtualizerFamily(obj *unstructured.Unstructured) bool {
	return obj.GetAnnotations()["cct.stratio.com/application_service"] == "virtualizer"
}

func virtualizerDeployment(name string, extraLabels ...string) *unstructured.Unstructured {
	u := legacyDeployment(virtualizerNS, name, name+"."+virtualizerNS, 1, extraLabels...)
	u.SetAnnotations(map[string]string{"cct.stratio.com/application_service": "virtualizer"})
	return u
}

func virtualizerIngress(name, appID string) *unstructured.Unstructured {
	return object(gvkIngress, virtualizerNS, name, cctAppIDLabel, appID)
}

// TestPlanVirtualizer_FirstMigrationDeletesTheWholeFamily: the server and
// its two siblings are all legacy; their Deployments go, in name order, and
// their ingresses stay.
func TestPlanVirtualizer_FirstMigrationDeletesTheWholeFamily(t *testing.T) {
	c := fakeClient(t,
		virtualizerDeployment("virtualizer"),
		virtualizerDeployment("virtualizer-monitor"),
		virtualizerDeployment("virtualizer-ui"),
		virtualizerIngress("virtualizer-monitor-virtualizer.stratio.eosdev", "virtualizer-monitor.stratio-apps"),
		virtualizerIngress("virtualizer-ui-virtualizer.stratio.eosdev", "virtualizer-ui.stratio-apps"),
	)
	opts := Options{TenantName: "stratio", LiveName: "virtualizer", LiveNamespace: virtualizerNS, IsLegacy: isVirtualizerFamily, Client: c}

	ops, err := planVirtualizer(context.Background(), opts)
	assertPlan(t, ops, err,
		"delete Deployment stratio-apps/virtualizer",
		"delete Deployment stratio-apps/virtualizer-monitor",
		"delete Deployment stratio-apps/virtualizer-ui")

	applyAll(t, c, ops)
	ops, err = planVirtualizer(context.Background(), opts)
	assertPlan(t, ops, err)
}

// TestPlanVirtualizer_ServerAlreadyMigrated is eosdev after the server's
// cutover: the app's live object is the Helm-managed Deployment, and only
// the monitor and ui siblings are still legacy. Only those two go; the
// Helm-managed Deployment, every ingress and everything of another app stay.
func TestPlanVirtualizer_ServerAlreadyMigrated(t *testing.T) {
	c := fakeClient(t,
		virtualizerDeployment("virtualizer", "helm.toolkit.fluxcd.io/name", "virtualizer"),
		virtualizerDeployment("virtualizer-monitor"),
		virtualizerDeployment("virtualizer-ui"),
		virtualizerIngress("virtualizer-monitor-virtualizer.stratio.eosdev", "virtualizer-monitor.stratio-apps"),
		virtualizerIngress("virtualizer-ui-virtualizer.stratio.eosdev", "virtualizer-ui.stratio-apps"),
		// The GitOps ingresses of the chart: Helm's labels, no CCT id.
		object(gvkIngress, virtualizerNS, "virtualizer-monitor", "helm.toolkit.fluxcd.io/name", "virtualizer"),
		object(gvkIngress, virtualizerNS, "virtualizer-ui", "helm.toolkit.fluxcd.io/name", "virtualizer"),
		// Another app's legacy Deployment and ingress in the same namespace.
		legacyDeployment(virtualizerNS, "discovery", "discovery.stratio-apps", 1),
		virtualizerIngress("discovery-discovery.stratio.eosdev", "discovery.stratio-apps"),
	)
	opts := Options{TenantName: "stratio", LiveName: "virtualizer", LiveNamespace: virtualizerNS, IsLegacy: isVirtualizerFamily, Client: c}

	ops, err := planVirtualizer(context.Background(), opts)
	assertPlan(t, ops, err,
		"delete Deployment stratio-apps/virtualizer-monitor",
		"delete Deployment stratio-apps/virtualizer-ui")
}

// TestPlanVirtualizer_AfterTheCutoverPlansNothing: once Helm owns every
// workload, a re-run has nothing to delete — even a Deployment that still
// carries CCT's annotations.
func TestPlanVirtualizer_AfterTheCutoverPlansNothing(t *testing.T) {
	c := fakeClient(t,
		virtualizerDeployment("virtualizer", "helm.toolkit.fluxcd.io/name", "virtualizer"),
		virtualizerDeployment("virtualizer-monitor", "helm.toolkit.fluxcd.io/name", "virtualizer"),
		virtualizerDeployment("virtualizer-ui", "helm.toolkit.fluxcd.io/name", "virtualizer"),
	)
	opts := Options{TenantName: "stratio", LiveName: "virtualizer", LiveNamespace: virtualizerNS, IsLegacy: isVirtualizerFamily, Client: c}
	ops, err := planVirtualizer(context.Background(), opts)
	assertPlan(t, ops, err)
}

// TestPlanVirtualizer_OnlyTheFamilyInTheAppsNamespace: a Deployment the
// selector doesn't match is never planned, nor is one owned by another
// object, nor one in another namespace.
func TestPlanVirtualizer_OnlyTheFamilyInTheAppsNamespace(t *testing.T) {
	owned := virtualizerDeployment("virtualizer-owned")
	owned.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Foo", Name: "x", UID: "u"}})
	other := virtualizerDeployment("virtualizer-monitor")
	other.SetNamespace("other-ns")
	c := fakeClient(t,
		virtualizerDeployment("virtualizer-monitor"),
		legacyDeployment(virtualizerNS, "rocket", "rocket.stratio-apps", 1),
		owned, other,
	)
	opts := Options{TenantName: "stratio", LiveName: "virtualizer", LiveNamespace: virtualizerNS, IsLegacy: isVirtualizerFamily, Client: c}
	ops, err := planVirtualizer(context.Background(), opts)
	assertPlan(t, ops, err, "delete Deployment stratio-apps/virtualizer-monitor")
}

func TestPlanVirtualizer_NeedsASelectorAndANamespace(t *testing.T) {
	c := fakeClient(t)
	for name, opts := range map[string]Options{
		"no selector":  {LiveNamespace: virtualizerNS, Client: c},
		"no namespace": {IsLegacy: isVirtualizerFamily, Client: c},
	} {
		if _, err := planVirtualizer(context.Background(), opts); err == nil {
			t.Errorf("%s: got nil error, want one rather than a guess", name)
		}
	}
}

func TestPrepareVirtualizer_ReplacesWorkloads(t *testing.T) {
	step := Find("prepare-virtualizer")
	if step == nil || !step.Automated || !step.ReplacesWorkloads || step.Plan == nil {
		t.Fatalf("prepare-virtualizer = %+v, want an automated step that replaces workloads", step)
	}
	if !strings.Contains(step.Description, "virtualizer-monitor") {
		t.Errorf("description %q doesn't name the siblings it removes", step.Description)
	}
}
