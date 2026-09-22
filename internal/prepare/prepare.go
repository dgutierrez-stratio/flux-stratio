// Package prepare implements the four one-time, per-tenant preconditions
// some apps require before they can be migrated (config.App.Prepare):
// suspending the legacy datamarket-agent, removing ingresses that would
// collide with their GitOps-managed replacements, and (the one step no
// Kubernetes API can verify) a manual Postgres data rewrite for genai.
//
// Every automated step re-checks live cluster state — Satisfied — before
// acting, never a persisted flag, matching flux-keos's own
// internal/migrate idempotency model ("every step re-checks live cluster
// state before acting, so running it more than once is always safe").
// internal/appmigrate's caller (apps migrate) checks Satisfied first and
// only calls Run when it returns false, under the same --dry-run/--yes
// confirmation gate as the migration itself (design decision 5 in the
// project plan) — except prepare-genai, whose confirmation is never
// skipped by --yes, since silently proceeding against unrewritten data
// risks real data corruption.
package prepare

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/log"
)

// Options carries what a prepare step needs.
type Options struct {
	TenantName string
	Client     client.Client
	Log        *log.Logger
}

// Step is one precondition an app can require before migration
// (config.App.Prepare names it by Name).
type Step struct {
	Name        string
	Description string
	// Automated is true when Run performs the precondition itself; false
	// for a step no automation can verify or safely perform
	// (prepare-genai), whose Run only explains what the operator must do
	// and never mutates anything — the actual gating decision belongs to
	// the caller.
	Automated bool
	// Satisfied reports whether this precondition already holds, without
	// changing anything.
	Satisfied func(ctx context.Context, opts Options) (bool, error)
	// Run satisfies the precondition (if Automated) or explains what the
	// operator must do (if not). Called only after Satisfied returns
	// false.
	Run func(ctx context.Context, opts Options) error
}

// Steps are the four ported prepare preconditions, in no particular
// order — an app names the one it needs via config.App.Prepare.
var Steps = []Step{
	stepDatamarketAgent,
	stepDatarest,
	stepDLC,
	stepGenAI,
}

// Find returns the step named name, or nil if none matches.
func Find(name string) *Step {
	for i := range Steps {
		if Steps[i].Name == name {
			return &Steps[i]
		}
	}
	return nil
}
