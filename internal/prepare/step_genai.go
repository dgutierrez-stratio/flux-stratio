package prepare

// genaiSQL is what an operator must run against genai's Postgres database
// (leader) by hand before migrating genai — ported from the Python
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

// stepGenAI is a change to live application data (a Postgres UPDATE),
// which no Kubernetes API call can verify or perform: it has no Plan, only
// Instructions. This plugin never claims to have confirmed it happened —
// the Python client's own version exited 0 unconditionally, marking the
// step migrated on faith whether or not the SQL was ever run. apps migrate
// gates on an explicit confirmation instead, never skipped by --yes.
var stepGenAI = Step{
	Name:         "prepare-genai",
	Description:  "rewrite genai's stored component references from dg-businessglossary-api to governance-businessglossary-api",
	Automated:    false,
	Instructions: genaiSQL,
}
