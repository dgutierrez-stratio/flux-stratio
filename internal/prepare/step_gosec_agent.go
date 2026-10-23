package prepare

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var stepGosecAgent = Step{
	Name:        "prepare-gosec-agent",
	Description: "scale the app's legacy gosec agent Deployment to 0 (kept, not deleted) so it stops fighting the new one",
	Automated:   true,
	Plan:        planGosecAgent,
}

// planGosecAgent scales every legacy gosec agent Deployment still running to
// 0 and waits for its pods to go. The GitOps chart renders a differently
// named Deployment (psql-gosec-agent, <entry>-gosec-agent), so the legacy one
// (psql-agent, <entry>-agent) is not replaced and would keep running: the two
// agents then overwrite each other's work (the OpenSearch ones delete each
// other's monitoring role on every pass, the Postgres ones race on the same
// REVOKE sweeps). Only the Deployment is scaled; it, its Service and its
// ServiceAccount stay, and nothing the legacy agent created in Vault, GoSec
// or the datastore is touched — the new agent adopts it.
//
// Flux-managed agents (the keos-core ones) carry no CCT label, so
// legacyObjects never returns them.
func planGosecAgent(ctx context.Context, opts Options) ([]Operation, error) {
	deps, err := legacyObjects(ctx, opts, gvkDeployment)
	if err != nil {
		return nil, err
	}
	var ops []Operation
	for _, dep := range deps {
		n, found, _ := unstructured.NestedInt64(dep.Object, "spec", "replicas")
		if !found {
			n = 1 // the API default when spec.replicas is unset
		}
		if n == 0 {
			continue
		}
		op := scaleToZeroOp(dep)
		// The old count is the rollback: kubectl scale deploy <name> --replicas=<n>.
		op.Action = fmt.Sprintf("scale to 0 replicas (was %d)", n)
		ops = append(ops, op)
	}
	return ops, nil
}
