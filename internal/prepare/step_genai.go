package prepare

// genaiChainWhere and genaiEndpointWhere pick the rows the statements
// change; Touches reuses them to count what is left to rewrite and to copy
// the rows before the SQL runs.
const (
	genaiChainTable = `"genai-api.<tenant>-genai".chain`
	genaiChainWhere = `chain_params::text LIKE '%dg-businessglossary-api%'
   OR worker_config::text LIKE '%dg-businessglossary-api%'
   OR invoke_schema::text LIKE '%dg-businessglossary-api%'`
	genaiEndpointTable = `"genai-gateway.<tenant>-genai".endpoint`
	genaiEndpointWhere = `id = 'llm-model'`
)

// genaiSQL rewrites genai's stored component references, run against the
// "genai" PgDatabase hosted on the tenant's shared PgCluster ("psql") —
// ported from the Python client's own generated SQL, which it wrote to a
// file and opened a GUI terminal to run interactively. genai-gateway is a
// deprecated component superseded by litellm: a migration whose genai
// instance has already moved to litellm (or never had genai-gateway) has
// no "genai-gateway.<tenant>-genai" schema at all, so its DELETE is
// guarded rather than assumed to still apply. The UPDATE keeps the Python
// script's WHERE, so it only rewrites rows that hold the old name — a
// re-run touches nothing, and no row is rewritten into itself.
const genaiSQL = `UPDATE ` + genaiChainTable + `
SET chain_params = replace(chain_params::text, 'dg-businessglossary-api', 'governance-businessglossary-api')::jsonb,
    worker_config = replace(worker_config::text, 'dg-businessglossary-api', 'governance-businessglossary-api')::jsonb,
    invoke_schema = replace(invoke_schema::text, 'dg-businessglossary-api', 'governance-businessglossary-api')::jsonb
WHERE ` + genaiChainWhere + `;
DO $$
BEGIN
  IF to_regclass('` + genaiEndpointTable + `') IS NOT NULL THEN
    DELETE FROM ` + genaiEndpointTable + ` WHERE ` + genaiEndpointWhere + `;
    RAISE NOTICE 'genai-gateway.<tenant>-genai.endpoint: deleted the llm-model row (if it existed)';
  ELSE
    RAISE NOTICE 'genai-gateway.<tenant>-genai.endpoint: schema not found, nothing to delete (genai-gateway is deprecated/superseded by litellm here)';
  END IF;
END $$;
`

// stepGenAI is a change to live application data (a Postgres UPDATE/DELETE),
// which no Kubernetes API call can verify: apps migrate runs it via pod
// exec against the tenant's PgCluster primary, only after the operator
// has seen the SQL and the target pod and said yes, then shows the real
// captured output; this plugin never claims on its own to have
// confirmed it worked — the Python client's own version exited 0
// unconditionally, marking the step migrated on faith whether or not the
// SQL was ever run. apps migrate gates on an explicit confirmation of the
// real output instead, never skipped by --yes.
//
// Before the SQL, apps migrate backs up the app's workloads and copies the
// rows the SQL changes (DumpRows); a re-run whose SELECT finds nothing left
// to rewrite and no legacy Deployment left to delete asks nothing. After it,
// the step deletes the family's legacy Deployments (genai-api, genai-ui,
// genai-developer-proxy): the chart gives each another immutable selector
// under the same name. Only those Deployments: the legacy Ingresses
// (genai.<domain>) and Services stay.
var stepGenAI = Step{
	Name:        "prepare-genai",
	Description: "rewrite genai's stored component references from dg-businessglossary-api to governance-businessglossary-api, then remove the legacy genai Deployments (the chart changed their immutable selectors)",
	// The legacy Deployments are deleted, so the GitOps ones are created anew.
	ReplacesWorkloads: true,
	Plan:              planDeleteFamily,
	Query: &DBQuery{
		Namespace: func(tenant string) string { return tenant + "-datastores" },
		PodSelector: map[string]string{
			"pgcluster.stratio.com/pgcluster-name": "psql",
			"pgcluster.stratio.com/pgcluster-role": "master",
		},
		Container: "postgresql",
		// ON_ERROR_STOP makes psql exit non-zero on a failed statement
		// (otherwise it carries on and exits 0), and --single-transaction
		// rolls the UPDATE back if the DELETE after it fails, so a run
		// either rewrites everything or nothing.
		Command: []string{"env", "PGPASSFILE=/etc/stratio/config/pgpass", "psql", "-v", "ON_ERROR_STOP=1", "--single-transaction", "genai"},
		SQL:     genaiSQL,
		// -tA: bare values, so a count is a number and COPY's rows are as
		// Postgres writes them.
		ReadCommand: []string{"env", "PGPASSFILE=/etc/stratio/config/pgpass", "psql", "-v", "ON_ERROR_STOP=1", "-tA", "genai"},
		Touches: []TouchedTable{
			{Table: genaiChainTable, Where: genaiChainWhere, File: "genai-<tenant>-chain.sql"},
			{Table: genaiEndpointTable, Where: genaiEndpointWhere, Optional: true, File: "genai-<tenant>-endpoint.sql"},
		},
	},
}
