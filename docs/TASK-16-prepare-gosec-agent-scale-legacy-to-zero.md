# TASK-16 — the legacy gosec agents (`psql-agent`, `opensearch1-agent`) keep running next to their replacements and interfere with them; add a component-scoped `prepare-gosec-agent` step that scales them to 0

**Status: implemented 2026-10-23 on `feat/application-migration` (`internal/prepare/step_gosec_agent.go`). No cluster change made for this finding yet: run the step on eosdev only with the user's yes naming the app.**

Decisions taken 2026-10-23: (1) timing: scale **during `apps migrate`**, before the push (gap of about a minute, like `prepare-datamarket-agent`), the user's choice over the recommended precondition variant; (2) the dg-postgresql-internal agents stay out of scope. Root cause confirmed in the agent source (`Stratio/dorus`, `opensearch-agent` Scala): the consumer's whitelist holds only its own `metricsRole` (default `metrics`, overridden by `OPENSEARCH_METRICS_USER`) and it deletes every other role, so two agents with different names delete each other's. The chart's `prometheus` is the target (it is also the Go rewrite's default); nothing in operators, keos-apps, keos-system-services, keos-installer or gosec references a role named `metrics`. Side finding for the Dorus owners: the producer-config block of `application.conf` reads the typo'd variable `OPENSEARCH_OPENSEARCH_METRICS_USERMETRICS_USER`.

Found in the eosdev legacy->GitOps re-run of 2026-10-15 (units 1 and 3 of that run; see `docs/MIGRATION-HISTORY.md` for the earlier run and the finding numbers cited here: C3, C8 and P5 of the re-run report).
Plugin to change: `flux-stratio` (this repo). Branch to work on: `feat/application-migration`.
Related, do not duplicate: `TASK-15` (which apps get a prepare step; its table row "gosec agents" says "keep manual, an optional `prepare-gosec-agent` stays open"; this task is that step), `TASK-01` (prepare-step backup completeness and the undeclared-dependency guard).

## Requirement for this task (user constraint, read first)

The user declined a **generic** "delete the legacy workload" step (see `TASK-15`, "Requirement for this task"). A **component-scoped** step is accepted **when requested**, and this one is requested.
Scope of the request: only the legacy **gosec agents** of the two datastore types the catalog knows (`postgres-gosec-agent`, `opensearch-gosec-agent`), and only **scale to 0**, never delete (same shape as `prepare-datamarket-agent`: the Deployment stays, so it is reversible by scaling it back).

Do **not** add a catalog-wide default, a `--scale-legacy` flag, or a rule that triggers for other types. State in the step's `Description`, in `config-reference.md` and in the catalog `Notes` exactly what it scales and what it leaves.

## What happened

The GitOps chart renders a differently named Deployment (`psql-gosec-agent`, `opensearch1-gosec-agent`) than the legacy one (`psql-agent`, `opensearch1-agent`), so Helm does not clash with the legacy Deployment and **both run**. Nothing in `apps migrate` stops the legacy one. The migration worker was told to leave legacy workloads alone, so on 2026-10-15 the legacy agents ran next to the new ones for hours (units 1 and 3):

| Pair | What both agents did | Evidence |
|---|---|---|
| `opensearch1-agent` (legacy) and `opensearch1-gosec-agent` (new) | They **swap the OpenSearch metrics role every cycle**: the legacy agent wants the role `metrics`, the new one `prometheus`, both map the same identity `prometheus`; each deletes the other's role and mapping as an orphan and recreates its own. 80 swaps in 10 minutes at first, **240 in 30 minutes** by the analysis, still 8 per minute four hours later. Security-index writes rose from about 7 to about 48 per minute; the monitoring identity has no working role about 2.4% of the time; the OpenSearch coordinators log about 950 security-config reloads per 30 minutes | `components/opensearch1-gosec-agent/after/flapping-counts.txt`, `components/opensearch1-gosec-agent/analysis.txt` of the session scratchpad; sweep P3(b) |
| `psql-agent` (legacy) and `psql-gosec-agent` (new) | Both run the same `REVOKE ALL ON schema public` / `REVOKE ALL ON DATABASE ... FROM public` sweeps on 12 databases. The new agent **lost two lock races** to the legacy sweep (`tuple concurrently updated`, 07:23:52Z and 07:24:06Z), its first failed iterations. The legacy agent was already dead before the migration (0 of 196 iterations succeeded, it calls the renamed `dg-businessglossary-api`), so today this is latent: it becomes a real two-writer problem if governance returns. Both share `SERVICE_ID psql.stratio-datastores` | `components/psql-gosec-agent/analysis.txt` |

No visible outage resulted (nobody consumes the `metrics` role; the legacy psql agent never gets past its governance call), but the plugin gave **no warning** that it was leaving a same-purpose agent running: the selector `[ACTION]` has been downgraded to a `[NOTE]` ("the legacy workload keeps running next to the new one", commit `9846c9a`), which is accurate but easy to miss, and nothing says the two will fight.
On the 2026-10-13/14 run the orchestrator scaled both to 0 by hand after the replacements were Ready (`MIGRATION-HISTORY.md` section 5.5, W1) and the flapping stopped within one 15 s cycle.

## Evidence (code, verified 2026-10-15 at `9504eb6`)

- Catalog types: `postgres-gosec-agent` (`internal/config/seed.go:75-112`, `Match: cctMatch(kindDeployment, "pg-gosec-agent", "")`, `Entry: gosecAgentEntry`, `Object: "{{ .Entry }}-gosec-agent"`) and `opensearch-gosec-agent` (`:113`-). Neither has a `Prepare:` field today.
- Model to copy: `prepare-datamarket-agent` (`internal/prepare/step_datamarket_agent.go`): `Automated: true`, `Plan` lists the legacy Deployments (`legacyObjects`: found by `cct.stratio.com/application_id=<LiveName>.<LiveNamespace>`, anything with a Flux label dropped) and returns `scaleToZeroOp` for each one still running; `scaleToZeroOp` (`:81-100`) merge-patches `spec.replicas: 0` and waits up to 2 minutes for the pods its own `spec.selector.matchLabels` selects; `ReplacesWorkloads` is false (nothing clashes, so the selector diff stays a note).
- Idempotent: an empty `Plan` (already at 0) means "already satisfied" (`internal/cli/apps_migrate.go`, `ensurePrepared`).
- **The step must never touch the keos-core agents** (`opensearcher-gosec-agent`, `postgreskeos` agents of the system layer): they are Flux/Helm managed and carry no CCT label, which `legacyObjects`/`fluxManaged` already excludes. Add a test with such an agent present.
- Backup: the same `backup.Run` of the app before the operations (the legacy Deployment manifest, from which the replica count can be restored). A related defect of the plugin: the backup folder for the OpenSearch agent was found under `backups/opensearch1-agent`, which is the **keos-core** agent, not the tenant's (the tenant's is `backups/opensearch1-gosec-agent/<ts>`): the analysis worker nearly used the wrong one (report P18). Make the step's output print the exact backup directory it wrote.

## Design

`prepare-gosec-agent`, `Automated: true`, shared by both catalog types (`Prepare: "prepare-gosec-agent"` on `postgres-gosec-agent` and `opensearch-gosec-agent`).

- **Find:** the app's legacy Deployments by CCT app id (`legacyObjects(ctx, opts, gvkDeployment)`); only those still with `spec.replicas > 0`.
- **Do:** `scaleToZeroOp` on each; keep the Deployment, its Service, ServiceAccount and Role. Record the replica count before scaling in the operation text (`scale <name> from 1 to 0`), so the rollback is `kubectl scale deploy <name> --replicas=<n>`; put that line in the step's `[ACTION]` hint and in the runbook.
- **Leave alone:** everything else, including Vault entries and GoSec objects the legacy agent created (the new agent adopts them; do not delete roles or policies).
- **Timing, the one design decision for the user** (see "Decisions"): `apps migrate` runs prepare steps **before** the push, so the legacy agent would be at 0 from then until Flux has installed the replacement (about one minute on eosdev). For a gosec agent that is harmless (its cycles are 15 to 30 s and a gap of a minute changes nothing), and it is what `prepare-datamarket-agent` already does. The alternative is a precondition: plan the scale-down **only when the replacement HelmRelease already exists and is Ready**, and otherwise print "the replacement is not Ready yet: run `apps migrate <app>` again after Flux has reconciled to scale the legacy agent to 0". After cutover the plugin resolves these agents to the legacy workload anyway (finding P2: `apps diff psql-gosec-agent` resolves `psql-agent`), so the later run finds it.
- **Message:** the existing `[NOTE]` should say, for these two types, that the step exists and which command removes the interference; list the step among those in `apps migrate --help`.

## Decisions (ask before coding)

1. **Timing:** scale during `apps migrate` (before the push, gap of about a minute, like datamarket-agent) or only when the replacement is Ready (second run)? Recommended: the precondition variant, because it never leaves the datastore without any agent.
2. **dg-postgresql-internal-agent / -op** (type `POSTGRESQL`, no GitOps component, never migrated) are **out of scope**: they are not gosec agents of these two types and have no replacement.

## Acceptance

- `flux stratio apps migrate psql-gosec-agent --dry-run` and `opensearch1-gosec-agent --dry-run` list exactly one operation: scale the legacy Deployment `stratio-datastores/psql-agent` (resp. `opensearch1-agent`) to 0; nothing else.
- A real run takes the backup first, scales, waits for the pods to terminate, and a second run prints that the step is already satisfied (promote the `Debugf` to a normal message, as TASK-15 recommends).
- With a keos-core Flux-managed agent and a tenant legacy agent both present, only the tenant one is planned (unit test with `runner.Fake`/fake client, like `TestPlanDatarest_OnlyTheAppsLegacyIngresses`).
- On a cluster where both agents run, scaling the legacy one to 0 ends the role swaps within one cycle (measure: count role create/delete lines per minute in the new agent's log before and after: 8 per minute to 0) and the psql agents stop racing.
- Docs: `config-reference.md` prepare table, `README.md`/`help.go` step list, `docs/migration-runbook.md` section 4 (a paragraph on the agents and the rollback line), catalog `Notes` for the two types, `CHANGELOG.md`, and a line in `TASK-15`'s table moving the gosec agents from "keep manual" to "implemented by TASK-16".
