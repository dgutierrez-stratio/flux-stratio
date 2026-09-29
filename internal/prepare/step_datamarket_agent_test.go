package prepare

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const datamarketID = "datamarket-agent.stratio-datastores"

var datamarketOpts = Options{TenantName: "stratio", LiveName: "datamarket-agent", LiveNamespace: "stratio-datastores"}

func legacyHelmRelease(suspend bool, labels ...string) *unstructured.Unstructured {
	hr := object(gvkHelmRelease, "stratio-datastores", "datamarket-agent", labels...)
	_ = unstructured.SetNestedField(hr.Object, suspend, "spec", "suspend")
	return hr
}

func TestPlanDatamarketAgent_SuspendsAndScalesDown(t *testing.T) {
	c := fakeClient(t, legacyHelmRelease(false), legacyDeployment("stratio-datastores", "datamarket-agent", datamarketID, 2))
	opts := datamarketOpts
	opts.Client = c

	ops, err := planDatamarketAgent(context.Background(), opts)
	assertPlan(t, ops, err,
		"suspend HelmRelease stratio-datastores/datamarket-agent",
		"scale to 0 replicas Deployment stratio-datastores/datamarket-agent")

	applyAll(t, c, ops)
	ops, err = planDatamarketAgent(context.Background(), opts)
	assertPlan(t, ops, err) // suspended and scaled down: satisfied
}

// TestPlanDatamarketAgent_EosdevHasNoHelmRelease: eosdev's datamarket-agent
// is a plain CCT Deployment; only the scale-down is planned.
func TestPlanDatamarketAgent_EosdevHasNoHelmRelease(t *testing.T) {
	opts := datamarketOpts
	opts.Client = fakeClient(t, legacyDeployment("stratio-datastores", "datamarket-agent", datamarketID, 1))
	ops, err := planDatamarketAgent(context.Background(), opts)
	assertPlan(t, ops, err, "scale to 0 replicas Deployment stratio-datastores/datamarket-agent")
}

func TestPlanDatamarketAgent_NeverTheGitOpsSide(t *testing.T) {
	opts := datamarketOpts
	opts.Client = fakeClient(t,
		legacyHelmRelease(false, "kustomize.toolkit.fluxcd.io/name", "apps-governance-datamarket-agent"),
		legacyDeployment("stratio-datastores", "datamarket-agent", datamarketID, 1, "helm.toolkit.fluxcd.io/name", "datamarket-agent"))
	ops, err := planDatamarketAgent(context.Background(), opts)
	assertPlan(t, ops, err)
}

func TestPlanDatamarketAgent_OtherNamespaceIgnored(t *testing.T) {
	opts := datamarketOpts
	opts.Client = fakeClient(t, legacyDeployment("other-datastores", "datamarket-agent", "datamarket-agent.other-datastores", 3))
	ops, err := planDatamarketAgent(context.Background(), opts)
	assertPlan(t, ops, err)
}

// TestScaleToZeroOp_NoSelectorFailsBeforePatching: without a pod selector
// there's nothing to wait on, so the scale-down isn't attempted at all.
func TestScaleToZeroOp_NoSelectorFailsBeforePatching(t *testing.T) {
	dep := object(gvkDeployment, "stratio-datastores", "datamarket-agent", cctAppIDLabel, datamarketID)
	_ = unstructured.SetNestedField(dep.Object, int64(1), "spec", "replicas")
	patches := 0
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(dep).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			patches++
			return c.Patch(ctx, o, p, opts...)
		},
	}).Build()
	if err := scaleToZeroOp(dep).Apply(context.Background(), c); err == nil {
		t.Error("scaling a Deployment with no pod selector: got nil error")
	}
	if patches != 0 {
		t.Errorf("patched %d time(s) before failing, want 0", patches)
	}
}

func TestPlanDatamarketAgent_AlreadySuspendedHelmReleaseLeftAlone(t *testing.T) {
	opts := datamarketOpts
	opts.Client = fakeClient(t, legacyHelmRelease(true), legacyDeployment("stratio-datastores", "datamarket-agent", datamarketID, 1))
	ops, err := planDatamarketAgent(context.Background(), opts)
	assertPlan(t, ops, err, "scale to 0 replicas Deployment stratio-datastores/datamarket-agent")
}
