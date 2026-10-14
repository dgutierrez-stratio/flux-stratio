package prepare

import (
	"context"
	"fmt"
)

// replaceLegacyDeployment is the prepare step of an app whose chart gives
// its Deployment another (immutable) selector under the legacy one's name:
// Flux can't update the legacy Deployment in place, so it is deleted before
// the release is installed and the chart creates it anew. what names the
// workload in the step's description ("the legacy discovery Deployment").
//
// The legacy Deployment is found by CCT's app id label, and anything Flux
// manages is skipped, so a re-run after the cutover plans nothing. Only the
// Deployment goes: the app's legacy ingresses (their hosts differ from the
// chart's, so nothing collides), Services and anything else stay — an app
// whose ingress does collide with the chart's has its own step
// (prepare-dlc, prepare-datarest).
func replaceLegacyDeployment(name, what string) Step {
	return Step{
		Name:        name,
		Description: fmt.Sprintf("remove %s (the chart changed its immutable selector)", what),
		Automated:   true,
		// The legacy Deployment is deleted, so the GitOps one is created anew.
		ReplacesWorkloads: true,
		Plan: func(ctx context.Context, opts Options) ([]Operation, error) {
			return planDeleteLegacy(ctx, opts, gvkDeployment)
		},
	}
}

var (
	// dg-hdfs-agent and the other dg-agent flavors of the same chart.
	stepDgAgent   = replaceLegacyDeployment("prepare-dg-agent", "the legacy governance agent Deployment")
	stepDiscovery = replaceLegacyDeployment("prepare-discovery", "the legacy discovery Deployment")
	// Both found by CCT app id (eureka-agent.stratio-datastores,
	// intelligence.stratio-intelligence). intelligence's legacy Ingress
	// (intelligence-rocket.<domain>) stays: the chart's admin Ingress has
	// another host, and the legacy one is the only one that works today.
	stepEurekaAgent  = replaceLegacyDeployment("prepare-eureka-agent", "the legacy eureka agent Deployment")
	stepIntelligence = replaceLegacyDeployment("prepare-intelligence", "the legacy intelligence Deployment")
)
