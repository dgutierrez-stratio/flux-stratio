All notable changes to this project will be documented in this file.

## 0.1.0-SNAPSHOT

* Initial release: application migration ported from the Python `migrate.py` client's `tenant`,
  `backup`, `patch`/`patch --chart-path` and `migrate-app` commands, redesigned around
  `flux stratio tenant import`, `apps backup`, `apps diff` and `apps migrate`.
* Fix: the generated tenant `ResourceSetInputProvider` now carries the label
  `keos.stratio.com/resourceset-type: tenant-config`, matching what every current
  `keos-use-cases` template actually selects on. The ported client emitted
  `flux.stratio.com/tenant: "true"` instead, making every tenant it generated invisible to every
  ResourceSet.
* Fix: a postgres/opensearch gosec agent's patch is now spliced into its parent component's
  `config.agent.patches`, not a nonexistent top-level `gosecAgentPostgres`/`gosecAgentOpensearch`
  component entry — `keos-use-cases` dropped those component keys; the gosec agent is derived from
  `postgres`/`opensearch`'s own `config.agent` block instead.
* Fix: `pgbackupLogical`/`pgbackupPhysical` (both deployed from the shared `pgbackup` chart) are
  disambiguated explicitly via `internal/catalog.ChartMapping.Ambiguous`, rather than silently
  collapsing into the same sentinel value gosec-agent detection also used.
* Fix: `apps migrate` edits the tenant file's `patches:` sequence in place via a comment-preserving
  YAML node tree, instead of decoding into a plain map and re-dumping the whole file — every
  comment and commented-out component block in the tenant file survives.
* Fix: rendered (chart/`flux build`) and live (cluster-fetched) numeric values now decode through
  the same convention (`k8s.io/apimachinery`'s int64-for-whole-numbers, not the JSON-default
  float64), so an unchanged integer field never shows up as a spurious diff.
* `apps migrate --all` orders apps by the dependency edges already declared in the tenant file
  (a dependency migrates before its dependent) instead of config-file order.
* `apps migrate` runs an app's declared `prepare` step automatically, under the same
  `--dry-run`/`--yes` gate as the migration itself; `prepare-genai` (a manual Postgres data
  rewrite no Kubernetes API can verify) always asks its own separate confirmation instead, never
  skipped by `--yes`.
* `apps diff --baseline <dir>` diffs against a previous `apps backup` capture instead of the live
  cluster.
* `flux stratio doctor`: a single preflight pass over required binaries, the config file, the
  `--base` repo layout, cluster access and the tenant file.
