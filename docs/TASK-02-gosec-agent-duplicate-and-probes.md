# TASK-02 — migrated gosec agents run next to their legacy twins (OpenSearch role flapping), post-migration diff reads the legacy object, probe settings not carried

**Status: open (plugin work). Cluster side: legacy agents scaled to 0 on 2026-10-09; deletion pending.**
Found in the eosdev batch-migration audit of 2026-10-09 (see `TASK-00-eosdev-audit-index.md`).
Plugin to change: `flux-stratio` (this repo). Branch: `feat/application-migration`.

## What happened

`apps migrate --all` migrated two GoSec agents to the GitOps chart `gosec-agent`:

| GitOps app (Flux HelmRelease) | Legacy CCT app it replaces | Namespace |
|---|---|---|
| `psql-gosec-agent` | `psql-agent` | `stratio-datastores` |
| `opensearch1-gosec-agent` | `opensearch1-agent` | `stratio-datastores` |

The catalog maps the GitOps name to the legacy live name (`App.LiveName()` = `psql-agent`). Because the
names differ, Helm installed the new Deployments without any conflict and **nothing removes the legacy
ones**. Both generations ran side by side for ~75 minutes (new ones created 10:56-10:57Z).

Evidence (all read-only, collected by an audit subagent):

- **OpenSearch: active conflict.** The legacy agent deletes role `prometheus` and recreates `metrics`;
  the new agent (`OPENSEARCH_METRICS_USER=prometheus`) deletes `metrics` and recreates `prometheus`.
  Each agent did this ~40 times in 10 minutes
  (`kubectl logs deploy/<agent> --since=10m | grep 'deleted successfully'`), so the `prometheus` role
  kept disappearing and metrics scraping broke intermittently.
- **Postgres: legacy agent was already broken.** `psql-agent` still pointed at
  `dg-businessglossary-api.keos-core:60000` (renamed to `governance-...`; the old Deployment is 0/0):
  279 "iteration did NOT finish", 0 successes in 70 min. New `psql-gosec-agent` works
  ("resources=165, users=14").
- **Registration names differ:** legacy `AGENT_SERVICE_NAME=psql-agent.stratio-datastores` /
  `SERVICE_NAME_AGENT=psql-agent`; new `psql-gosec-agent.…` / `psql-gosec-agent` (catalog `exclude`
  keeps the GitOps value on purpose).
- Each legacy agent owns: Deployment, Service (8080/9187), ServiceAccount, Role, RoleBinding,
  ServiceMonitor (so metrics were scraped twice). No ConfigMaps, Secrets or Ingress. Find them with
  label `cct.stratio.com/application_id=<name>.stratio-datastores`.
- `~/.fluxcd/flux-stratio/catalog.yaml` has no `prepare:` for `postgres-gosec-agent` or
  `opensearch-gosec-agent`; `internal/prepare` has no gosec step (only dg-agent, datamarket-agent,
  datarest, dlc, virtualizer, genai, litellm).

## Plugin defects

1. **Misleading `[ACTION]`.** Migrate and diff print for both agents
   `1 workload(s) can't be updated in place: their selector is immutable. Delete the legacy workload
   before Flux installs the release. Only an app with a prepare step does it for you.`
   This is false here (different names, no collision) and gives no hint that the legacy agent will keep
   running and fight the new one. The selector check compares GitOps selector vs the *legacy* object's
   selector even when the two are different objects.
2. **Post-migration diff looks at the wrong object.** After cutover
   `apps diff psql-gosec-agent --drift <backup>` reports "no drift" and the default diff prints
   `LIVE SELECTOR cct.stratio.com/application_id=psql-ag…` because it still resolves the live app to the
   legacy `psql-agent`. It says nothing about the new Flux-managed Deployment. The same blind spot
   applies to any app whose GitOps workload name differs from its legacy name.
3. **Probe settings not carried.** The review lists 8 variables as "not rendered by the chart: lost
   after migration" (`LIVENESS_PROBE_{FAILURE_THRESHOLD=3,INITIAL_DELAY_SECONDS=2,PERIOD_SECONDS=5,
   TIMEOUT_SECONDS=1}`, `READINESS_PROBE_{FAILURE_THRESHOLD=3,INITIAL_DELAY_SECONDS=15,PERIOD_SECONDS=15,
   TIMEOUT_SECONDS=1}`). These are CCT parameters that fed the probes, not agent runtime settings, so
   the message is mostly cosmetic. The real difference after migration is **one value**: readiness
   `failureThreshold` is 3 legacy vs 1 in the chart (`gosec-agent/values.yaml`). Liveness and the other
   readiness values are identical. The chart exposes probes at
   `controllers.agent.containers.agent.probes.{liveness,readiness}.spec`. The plugin never patches
   probes (CONTEXT.md: "Image tags, replicas, probes and selectors are never patched") and does not
   translate the `*_PROBE_*` env vars into probe values.

## Analysis: the 8 `*_PROBE_*` variables (done 2026-10-13, read-only)

**What they are.** CCT parameters of the legacy descriptors `kubernetes-universe/packages/security/pg-agent.json` and `os-agent.json` (15.1.1-SNAPSHOT), group
`general.probes`, all `application:false` and UI-updatable. The descriptor's own tasks build the pod probes from them (`pg-agent.json:87-88`:
`K8sReadinessHttp(path /readiness, ..., failureThreshold ${general.probes.readinessProbeFailureThreshold}, ...)` and the liveness equivalent); CCT also exports each parameter as a
container env var, which is why the plugin sees them as live variables. They are inputs of the probe, not runtime settings of the agent (no reader found; the agent source is not available locally, so
"the agent never reads them" is an inference, supported by the legacy probe spec matching the env values exactly).

**Legacy vs descriptor default vs chart vs new live (both agents identical):**

| Setting | Legacy probe spec (backup) | Descriptor default | Chart `gosec-agent` (`values.yaml:46-75`) | New live Deployment |
|---|---|---|---|---|
| liveness `initialDelaySeconds` / `periodSeconds` / `timeoutSeconds` / `failureThreshold` | 2 / 5 / 1 / 3 | 2 / 5 / 1 / 3 | 2 / 5 / 1 / 3 | 2 / 5 / 1 / 3 |
| readiness `initialDelaySeconds` / `periodSeconds` / `timeoutSeconds` | 15 / 15 / 1 | 15 / 15 / 1 | 15 / 15 / 1 | 15 / 15 / 1 |
| readiness `failureThreshold` | **3** | **3** | **1** | **1** |

So 7 of the 8 "lost" variables are in fact reproduced (the chart hard-codes the same numbers through `controllers.agent.containers.agent.probes.*.spec`); only
`READINESS_PROBE_FAILURE_THRESHOLD` differs. The review message ("lost after migration") is wrong for 7 and right for 1.

**Why the chart has 1.** First chart version (`49fcc09`, 2025-03-05) had readiness `failureThreshold: 3`. `4e33159` (2025-04-03, "Migrate PostgreSQL Agent and OpenSearch Agent to new common chart") rewrote the
probe inline with `failureThreshold: 1`, and `b4c9924` (gosec-agent unification) kept it. Neither message gives a reason. Other charts vary (virtualizer and spark-history also use 1; dg-agent, discovery,
rocket 3; eureka-agent 5), so 1 may be a style choice rather than a requirement.

**What the difference means in practice.**
- `/readiness` on the new agent answers 200 in ~1 ms (measured on `psql-gosec-agent`, localhost:8080), so it looks like a local liveness-style check, not a dependency check (what it does when GoSec is
  down was **not** tested). Readiness only gates Service endpoints (the ServiceMonitor scrape targets on 8080/9187) and rollout progress; there is no ingress.
- With threshold 1 and period 15 s, a single slow or failed check (timeout 1 s) removes the pod from the Service until the next success: a short metrics gap. With 3 it takes 45 s of failures. Liveness
  (restart decisions) is identical, so no extra restarts come from this. The 2 restarts of `opensearch1-gosec-agent` (exit code 1 at 2026-10-09T20:24Z, 31 s after start) are an application exit, not a probe kill.
- Severity: low on eosdev. The real general risk is another client who **customised** these CCT parameters (they are UI-updatable): their values would be silently dropped, and the plugin would not carry them.

**Can a patch change it?** Yes. Probes are map paths in the HelmRelease values, so a patch merges instead of replacing a list. Verified with `helm template` (chart `gosec-agent`, `ci/default-values.yaml`):
adding `controllers.agent.containers.agent.probes.readiness.spec.failureThreshold: 3` renders `failureThreshold: 3` and leaves `httpGet`, delays, period and timeout untouched.

### Recommended solution

1. **Chart (fixes the default for every client):** set readiness `failureThreshold: 3` in `gosec-agent/values.yaml`, i.e. match the descriptor and the chart's own first version, unless the chart authors
   (the PLT-1952 / PLT-2505 authors) can name a reason for 1. Then the eosdev patch needs nothing.
2. **Plugin (preserves customised clients):** translate the eight `*_PROBE_*` variables into `probes.{liveness,readiness}.spec.{initialDelaySeconds,periodSeconds,timeoutSeconds,failureThreshold}`
   through a catalog mapping, write only the values that differ from the rendered chart, verify by re-render (like resources, commit `934fa0f`), and stop listing a variable as lost when it was
   translated or already equal. `CompareWorkloadSpecs` does not compare probes today (resources, image, replicas, selector only), so this also makes probe differences visible.
3. **Interim for eosdev, only if item 1 is not merged first:** the three-line patch in the "Tenant-file interim patch" below, on both agents.
4. Update `CONTEXT.md` ("Image tags, replicas, probes and selectors are never patched") and the note in `docs/TASK-charts-universe-alignment.md:256` ("the chart hard-codes probes") when item 2 lands.

Not verified: what `/readiness` checks when GoSec or the datastore is unreachable (decides whether threshold 3 vs 1 matters more than a metrics gap); whether any agent code reads the env vars.

## Decisions already made (do not re-litigate)

- **Cluster, done 2026-10-09 (user approved):** both legacy agents scaled to 0 (reversible):
  `opensearch1-agent` at ~12:08Z, `psql-agent` at ~12:12Z. Verified afterwards: role churn stopped
  (0 role events in a clean 90 s window), CCT did not recreate the Deployments, `psql-gosec-agent` phases
  finish `__SUCCESSFULLY__`, both HelmReleases Ready.
  Rollback: `kubectl -n stratio-datastores scale deploy psql-agent opensearch1-agent --replicas=1`.
- **Cluster, pending (manual, user schedules it):** after a soak, delete the legacy objects by label
  (`kubectl -n stratio-datastores delete deploy,svc,sa,role,rolebinding,servicemonitor -l
  cct.stratio.com/application_id=psql-agent.stratio-datastores`, same for `opensearch1-agent`), delete
  Vault roles `stratio-datastores_psql-agent` and `stratio-datastores_opensearch1-agent`, and check GoSec
  for services/policies still referencing the old agent names. Not verified: GoSec registrations and
  Vault roles (needs credentials), and that policies are keyed by the datastore service rather than the
  agent name (inference).
- **Tenant-file interim patch (NOT applied; the user wants solutions delivered through the plugin).**
  If someone needs it before the plugin change lands, add to both agents' HelmRelease patch
  (psql near line 69, opensearch near line 303 of
  `keos-fleet/clusters/eosdev/tenants/config/stratio.yaml`):
  ```yaml
  controllers:
    agent:
      containers:
        agent:
          probes:
            readiness:
              spec:
                failureThreshold: 3
  ```
- Standing user rule (memory `legacy-cutover-stays-manual`): no generic "delete legacy workload" step;
  a per-component prepare step is acceptable **only on request**. Whether to add a gosec-agent prepare
  step is an open question below, not a decision.

## Proposed changes

1. **Fix the selector `[ACTION]`.** Only emit it when the GitOps workload and the legacy workload have
   the same kind, namespace **and** name. When they differ, emit a different `[ACTION]`:
   "legacy workload `psql-agent` keeps running next to the new `psql-gosec-agent`; remove or scale it
   down after verifying the new one" listing the legacy object (name, kind, namespace, replicas).
2. **Teach diff/drift/backup about the GitOps-side workload.** When the app's HelmRelease exists and
   its workload name differs from `App.LiveName()`, `apps diff` default/`--baseline` and `--drift`
   should say which object each side is, and report the legacy object's presence explicitly. At minimum:
   print a `[NOTE]` "this comparison is against the legacy `psql-agent`; the Flux-managed
   `psql-gosec-agent` is not compared". Better: compare both.
3. **Carry probe values.** Add a catalog-level mapping (or built-in rule) from the CCT `*_PROBE_*`
   parameters to the chart's `probes.<liveness|readiness>.spec.{failureThreshold,initialDelaySeconds,
   periodSeconds,timeoutSeconds}`, written into the patch only where live differs from the rendered
   chart (so identical values do not clutter the patch), and verified by `VerifyResources`-style
   re-render like resources. Stop listing the 8 variables as "lost" when they were translated.
   Reconcile with the CONTEXT.md statement that probes are never patched (update the doc and the
   design-decisions section if the rule changes).
4. **(Ask the user first)** a `prepare-gosec-agent` step that scales the legacy agent to 0 and, after
   confirmation, removes its objects by label, with a backup (see TASK-01 defect A). Default is to do
   nothing and rely on the corrected `[ACTION]` text from item 1.

## Acceptance criteria

- Unit/integration tests with a fake cluster containing legacy `psql-agent` (CCT labels) and a Flux
  `psql-gosec-agent`: the selector `[ACTION]` is not printed for the different-name case; the new
  legacy-keeps-running `[ACTION]` is; `--drift` output states which object it compared.
- A test where the legacy readiness `failureThreshold` is 3 and the chart default is 1: the patch
  contains `probes.readiness.spec.failureThreshold: 3` and the re-render check passes; no patch lines
  for probe values that already match.
- `CONTEXT.md`, `docs/config-reference.md` (catalog keys) and `CHANGELOG.md` updated; `go test ./...`.

## How to reproduce

```
flux stratio apps diff psql-gosec-agent                 # misleading [ACTION], live selector is the legacy one
flux stratio apps diff psql-gosec-agent --view patch
kubectl -n stratio-datastores get deploy -o wide | grep -E 'agent'
kubectl -n stratio-datastores logs deploy/opensearch1-gosec-agent --since=10m | grep 'deleted successfully'
```
(The legacy agents are now at 0 replicas; scale them back to 1 to reproduce the flapping.)

## Requirement: investigate before changing (see "Migration principle" in `TASK-00`)

Keep each client's original configuration as close as possible, but welcome real improvements. Before
implementing, investigate and record, with evidence, whether each difference between the legacy agent and
the GitOps agent makes sense in the new scenario and why:

- **Agent service name** (`psql-agent` to `psql-gosec-agent`, `opensearch1-agent` to
  `opensearch1-gosec-agent`; env `AGENT_SERVICE_NAME`, `SERVICE_NAME_AGENT`): what GoSec does with it
  (registration key? policy ownership? audit trail?), whether a rename orphans anything for clients that
  already have policies, and whether the new name is better. Not verified in the audit: GoSec side.
- **OpenSearch metrics role:** the legacy agent manages role `metrics`, the GitOps agent
  `OPENSEARCH_METRICS_USER=prometheus` (the two fought each other). Which name does the monitoring stack
  (Prometheus scrape config, ServiceMonitor, opensearch-operator `spec.monitoring.user: prometheus`, which
  the migrate patch itself wrote) expect? Decide which is right for the product, not just for eosdev.
- **Readiness `failureThreshold`** (legacy 3, chart 1): why the chart chose 1, whether 1 is an improvement
  (faster failover) or a regression (flapping on slow GoSec responses), and only then whether to carry 3.
  The same question for liveness and the other readiness values (they were identical on eosdev).
- **The 8 `*_PROBE_*` variables the review calls lost:** confirm they are CCT parameters only (audit says
  so) and stop reporting them as losses if the chart covers them through `probes.*`.
- **Whether the legacy agents should be removed at all for clients whose GoSec setup refers to the old
  names**; the audit only proved the eosdev case (legacy Postgres agent broken, OpenSearch pair fighting).

## Related

- `TASK-01` defect A (back up what a step deletes) applies if item 4 is built.
- Audit artifact with all raw outputs: https://claude.ai/artifact/7TbG1ew9A6CGCMUDShFDx4
