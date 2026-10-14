package prepare

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const rocketNS = "stratio-rocket"

var rocketOpts = Options{TenantName: "stratio", LiveName: "rocket", LiveNamespace: rocketNS}

// ownedByDeployment sets obj's ownerReference to the Deployment parent (name, uid): as
// the rocket server makes its runtime children.
func ownedByDeployment(obj *unstructured.Unstructured, parent string, uid types.UID) *unstructured.Unstructured {
	obj.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: parent, UID: uid}})
	return obj
}

// rocketChild is a Deployment the rocket server created: no CCT label, no
// Flux label, an ownerReference to its parent.
func rocketChild(name, parent string, uid types.UID) *unstructured.Unstructured {
	return ownedByDeployment(object(gvkDeployment, rocketNS, name), parent, uid)
}

// TestPlanRocket_TheParentThenItsOwnChildren mirrors eosdev
// (W6/legacy-rocket-ns-deployments.yaml): rocket and the four children its
// server created go, in that order; rocket-catalog-standalone (a CCT app of
// its own) stays, and so do another namespace's Deployments, another
// parent's children, the Ingresses and the Services.
func TestPlanRocket_TheParentThenItsOwnChildren(t *testing.T) {
	rocket := legacyDeployment(rocketNS, "rocket", "rocket."+rocketNS, 1)
	uid := rocket.GetUID()
	c := fakeClient(t,
		rocket,
		rocketChild("rocket-validator", "rocket", uid),
		rocketChild("rocket-catalog", "rocket", uid),
		rocketChild("rocket-externalservice", "rocket", uid),
		rocketChild("rocket-debug", "rocket", uid),
		legacyDeployment(rocketNS, "rocket-catalog-standalone", "rocket-catalog-standalone."+rocketNS, 1),
		rocketChild("unrelated-child", "somebody-else", types.UID("uid-somebody-else")),
		legacyDeployment("other-ns", "rocket", "rocket.other-ns", 1),
		object(gvkIngress, rocketNS, "rocket-rocket.eosdev.int", cctAppIDLabel, "rocket."+rocketNS),
		object(gvkService, rocketNS, "rocket", cctAppIDLabel, "rocket."+rocketNS),
	)
	opts := rocketOpts
	opts.Client = c

	ops, err := planRocket(context.Background(), opts)
	assertPlan(t, ops, err,
		"delete Deployment stratio-rocket/rocket",
		"delete Deployment stratio-rocket/rocket-catalog",
		"delete Deployment stratio-rocket/rocket-debug",
		"delete Deployment stratio-rocket/rocket-externalservice",
		"delete Deployment stratio-rocket/rocket-validator")

	applyAll(t, c, ops)
	ops, err = planRocket(context.Background(), opts)
	assertPlan(t, ops, err) // satisfied; rocket-catalog-standalone is still there
}

// TestPlanRocket_AfterTheCutoverNothingIsPlanned: the new server is
// Flux-managed, its children carry no Flux label and are owned by the new
// rocket — and none of that is ever planned (never "every owned Deployment
// in the namespace").
func TestPlanRocket_AfterTheCutoverNothingIsPlanned(t *testing.T) {
	rocket := legacyDeployment(rocketNS, "rocket", "rocket."+rocketNS, 1, "helm.toolkit.fluxcd.io/name", "rocket")
	uid := rocket.GetUID()
	opts := rocketOpts
	opts.Client = fakeClient(t,
		rocket,
		rocketChild("rocket-catalog", "rocket", uid),
		rocketChild("rocket-validator", "rocket", uid),
		legacyDeployment(rocketNS, "rocket-catalog-standalone", "rocket-catalog-standalone."+rocketNS, 1),
	)
	ops, err := planRocket(context.Background(), opts)
	assertPlan(t, ops, err)
}

// TestPlanRocket_NeverAFluxManagedChild: a child that Flux
// manages is never planned, even when owned by the legacy parent.
func TestPlanRocket_NeverAFluxManagedChild(t *testing.T) {
	rocket := legacyDeployment(rocketNS, "rocket", "rocket."+rocketNS, 1)
	child := rocketChild("rocket-catalog", "rocket", rocket.GetUID())
	child.SetLabels(map[string]string{"helm.toolkit.fluxcd.io/name": "rocket"})
	opts := rocketOpts
	opts.Client = fakeClient(t, rocket, child)
	ops, err := planRocket(context.Background(), opts)
	assertPlan(t, ops, err, "delete Deployment stratio-rocket/rocket")
}
