package prepare

// genaiSQL rewrites genai's stored component references, run against the
// "genai" PgDatabase hosted on the tenant's shared PgCluster ("psql") —
// ported from the Python client's own generated SQL, which it wrote to a
// file and opened a GUI terminal to run interactively. genai-gateway is a
// deprecated component superseded by litellm: a migration whose genai
// instance has already moved to litellm (or never had genai-gateway) has
// no "genai-gateway.<tenant>-genai" schema at all, so its DELETE is
// guarded rather than assumed to still apply.
const genaiSQL = `UPDATE "genai-api.<tenant>-genai".chain
SET chain_params = replace(chain_params::text, 'dg-businessglossary-api', 'governance-businessglossary-api')::jsonb,
    worker_config = replace(worker_config::text, 'dg-businessglossary-api', 'governance-businessglossary-api')::jsonb,
    invoke_schema = replace(invoke_schema::text, 'dg-businessglossary-api', 'governance-businessglossary-api')::jsonb;
DO $$
BEGIN
  IF to_regclass('"genai-gateway.<tenant>-genai".endpoint') IS NOT NULL THEN
    DELETE FROM "genai-gateway.<tenant>-genai".endpoint WHERE id = 'llm-model';
    RAISE NOTICE 'genai-gateway.<tenant>-genai.endpoint: deleted the llm-model row (if it existed)';
  ELSE
    RAISE NOTICE 'genai-gateway.<tenant>-genai.endpoint: schema not found, nothing to delete (genai-gateway is deprecated/superseded by litellm here)';
  END IF;
END $$;
`

// stepGenAI is a change to live application data (a Postgres UPDATE/DELETE),
// which no Kubernetes API call can verify: apps migrate runs it via pod
// exec against the tenant's PgCluster primary and shows the operator the
// real captured output, but this plugin never claims on its own to have
// confirmed it worked — the Python client's own version exited 0
// unconditionally, marking the step migrated on faith whether or not the
// SQL was ever run. apps migrate gates on an explicit confirmation of the
// real output instead, never skipped by --yes.
var stepGenAI = Step{
	Name:        "prepare-genai",
	Description: "rewrite genai's stored component references from dg-businessglossary-api to governance-businessglossary-api",
	Query: &DBQuery{
		Namespace: func(tenant string) string { return tenant + "-datastores" },
		PodSelector: map[string]string{
			"pgcluster.stratio.com/pgcluster-name": "psql",
			"pgcluster.stratio.com/pgcluster-role": "master",
		},
		Container: "postgresql",
		Command:   []string{"env", "PGPASSFILE=/etc/stratio/config/pgpass", "psql", "genai"},
		SQL:       genaiSQL,
	},
}
