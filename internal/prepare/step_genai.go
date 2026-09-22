package prepare

import "context"

// genaiSQL is what an operator must run against genai's Postgres database
// (leader) by hand before this step is satisfied — ported from the Python
// client's own generated SQL, which it wrote to a file and opened a GUI
// terminal to run interactively.
const genaiSQL = `-- Run against genai's Postgres database (leader pod), e.g.:
--   kubectl exec -it <pgcluster-leader-pod> -n <tenant>-datastores -- \
--     env PGPASSFILE=/etc/stratio/config/pgpass psql psql
UPDATE "genai-api.<tenant>-genai".chain
SET chain_params = replace(chain_params::text, 'dg-businessglossary-api', 'governance-businessglossary-api')::jsonb,
    worker_config = replace(worker_config::text, 'dg-businessglossary-api', 'governance-businessglossary-api')::jsonb,
    invoke_schema = replace(invoke_schema::text, 'dg-businessglossary-api', 'governance-businessglossary-api')::jsonb;
DELETE FROM "genai-gateway.<tenant>-genai".endpoint WHERE id = 'llm-model';
`

var stepGenAI = Step{
	Name:        "prepare-genai",
	Description: "rewrite genai's stored component references from dg-businessglossary-api to governance-businessglossary-api",
	Automated:   false,
	Satisfied:   genaiSatisfied,
	Run:         runGenai,
}

// genaiSatisfied always reports false: this precondition is a change to
// live application data (a Postgres UPDATE), which no Kubernetes API call
// can verify. Unlike the other three steps, this plugin never claims to
// have confirmed it happened — the Python client's own version exited 0
// unconditionally here, marking the step migrated on faith regardless of
// whether the operator ever ran the SQL (see design decision 5 in the
// project plan). The caller must gate on an explicit, separate
// confirmation instead.
func genaiSatisfied(context.Context, Options) (bool, error) {
	return false, nil
}

// runGenai never mutates anything: it only explains, via opts.Log, the
// exact SQL an operator must already have run. Whether to trust that it
// was actually done is entirely the caller's decision (apps migrate gates
// on it with its own confirmation, never skipped by --yes) — Run has no
// way to check.
func runGenai(_ context.Context, opts Options) error {
	if opts.Log != nil {
		opts.Log.Warningf("prepare-genai requires a manual Postgres data rewrite:\n%s", genaiSQL)
	}
	return nil
}
