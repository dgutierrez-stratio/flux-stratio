package prepare

import "context"

var stepDatarest = Step{
	Name:        "prepare-datarest",
	Description: "remove the legacy DataRest ingress that would collide with the GitOps-managed one",
	Automated:   true,
	Plan:        planDatarest,
}

// planDatarest deletes every legacy Ingress CCT deployed for the DataRest
// instance (its admin ingress, dg-datarest-pgi-admin.<domain> on eosdev).
// The Python client deleted one hardcoded name in a hardcoded namespace,
// so any other instance was never handled.
func planDatarest(ctx context.Context, opts Options) ([]Operation, error) {
	ings, err := legacyObjects(ctx, opts, gvkIngress)
	if err != nil {
		return nil, err
	}
	ops := make([]Operation, 0, len(ings))
	for _, ing := range ings {
		ops = append(ops, deleteOp(ing))
	}
	return ops, nil
}
