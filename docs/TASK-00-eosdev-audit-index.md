# TASK-00 — eosdev batch-migration audit: index of findings and tasks

Audit of the 2026-10-09 `flux stratio apps migrate --all` run on eosdev (tenant `stratio`), done by
re-running the plugin's own `apps diff` (default, `--baseline`, `--drift`) against the live cluster and
the pre-migration backups (`~/.fluxcd/flux-stratio/backups/<app>/2026-10-09T10-51-40Z`).
Full results and every command with its output: https://claude.ai/artifact/7TbG1ew9A6CGCMUDShFDx4

## Working rules (set by the user)

- Findings are handled one at a time: present, clarify doubts, agree a solution, then the user confirms
  before moving to the next.
- Each finding becomes its own numbered file here, `TASK-NN-<slug>.md`, written so another agent can
  pick it up without this conversation: context, evidence, decisions, proposed change, acceptance
  criteria, reproduction. Whether a finding becomes a real work item is the user's later decision.
- Solutions are improvements to the reference migration tools: `flux-stratio` (`/stratio/gitops/flux-stratio`)
  and `flux-keos` (`/stratio/gitops/flux-keos`), not one-off fixes on the cluster.
- Cluster actions during the audit are only taken with explicit approval, kept reversible, and recorded
  in the relevant task.

## Migration principle (a requirement for every task below)

The goal is to migrate legacy apps to the GitOps-with-charts architecture while keeping each client's
original configuration **as close as possible**. Migration can also bring positive changes, and those
are welcome. So for **every** difference, variable, setting or behaviour an item touches, whoever picks
the task up must first **investigate whether the change makes sense in the new scenario, and why**:
where the value is defined (legacy descriptor, chart, catalog), what it does at runtime, and whether the
new value is an improvement, a neutral rename, or a regression for existing clients. Record the
conclusion and its evidence in the task file before changing a tool, and classify each item as
**preserve legacy**, **accept new** or **needs a product decision**. Never assume either direction. A long
list of differences (for example the 11 variables of TASK-03) is a list to investigate, not a list of bugs.

## Outcome in one paragraph

14 apps were migrated. 6 operator apps are identical to their backup after the patch (baseline diff
"no differences"). Every value loss found was announced by `apps migrate` before the user applied it.
The problems are around the patches: a prepare step that deleted objects it had not backed up, legacy
agents left running next to their GitOps replacements, and diff/backup paths that break on a name
collision or read the legacy object.

## Findings

| # | Finding | Status | Task file | Plugin |
|---|---|---|---|---|
| 01 | `prepare-dlc` deleted `dlc-entity` Deployment + Ingress; the Ingress was never backed up; prepare ran while Flux could not render the replacement (undeclared `rocket`). Cluster: left down on purpose until `rocket` is enabled | decided, task written | `TASK-01-prepare-step-safety-and-ingress-backup.md` | flux-stratio |
| 02 | Legacy `psql-agent` / `opensearch1-agent` ran next to the new gosec agents (OpenSearch role flapping); misleading selector `[ACTION]`; diff reads the legacy object; readiness `failureThreshold` 3 vs 1 not carried. Cluster: legacy scaled to 0 | decided, task written | `TASK-02-gosec-agent-duplicate-and-probes.md` | flux-stratio |
| 03 | `dg-hdfs-agent`: `CUSTOM_STRATIO_CREDENTIALS` is `hdfs-external-secret` live (legacy and local render: blank); 11 further values change and cannot be patched (`COMM_SERVICE_NAME`, connector jar URL, ...). Suggests the deployed chart differs from the worktree the plugin renders from | task written; charts credentials fix pushed (`0a1e649`); rest is investigation + plugin work | `TASK-03-dg-hdfs-agent-values-review-and-chart-skew.md` | flux-stratio, charts |
| 04 | `virtualizer` loses `EXTRA_JARS` (HDFS + PostgreSQL SSCC jars), `EXTRA_JARS_SPARK_*_CLASSPATH`, monitor `SERVER_MAXHTTPHEADERSIZE=65536` (announced, confirmed by drift) | task written; decisions on eosdev connectors and the header-limit default are open | `TASK-04-virtualizer-lost-variables-and-annotation-fieldref.md`; the `EXTRA_JARS` part is analysed in `TASK-virtualizer-decoupled-connector-jars.md` (chart gap, solution recommended) | flux-stratio, charts |
| 05 | `hdfs1`, `kafka1` (and `psql` config) gain settings absent from backup and tenant file: `dfs.replication: "1"`, `offsets.topic.replication.factor: "1"`, Postgres `work_mem`, `max_wal_size`, ...; all come from the GitOps size overlay `S` | task written. Corrected: caused by the keos-apps size overlay `S` (not operator defaulting); hdfs/kafka replication analysed 2026-10-13: reject both on eosdev, fix kafka S in keos-apps; hdfs/kafka restarted at 10:56, psql needs a restart. `--baseline` is one-directional and cannot see GitOps-only keys | `TASK-05-gitops-size-overlay-adds-config-and-restarts.md` | keos-apps, flux-stratio |
| 06 | Classification collision with a same-named `PgDatabase`: `apps diff dlc-entity` errors in every mode (also with `--as`), and the `genai-litellm.stratio-genai` backup holds only `cr.yaml` (the PgDatabase), so its `--drift` fails. litellm has a Vault-moving prepare step and needs a good backup first | pending discussion | not written | flux-stratio |
| 07 | Diff noise: `<fieldRef:status.podIP>` vs `<status.podIP>` reported as a difference (HOST, SERVICE_ACCOUNT_NAME); selector `[ACTION]` on `eureka-agent` without a prepare step (legacy Deployment was removed by hand) | pending discussion | not written | flux-stratio |
| 08 | Doctor warns `keos-system-services` checkout is not at the revision the cluster runs; renders of apps built from it may be skewed (related to 03) | pending discussion | not written | flux-stratio (doctor) |
| 09 | litellm post-requisites Job builds `MASTER_API_KEY="sk-$(cat ...)"` unconditionally; the legacy key carried over by `prepare-litellm` already starts with `sk-`, so it sends `sk-sk-...`, the model/credential API calls fail and the Job still ends `Complete` (eosdev image `0.3.0-b7465f3` has the entrypoint's "prefix only when missing" rule from genai-litellm `7b695f5`). The Job log also prints the key (shell trace) | task written 2026-10-14; recommended: apply the entrypoint's rule in the Job (keos-apps) and fail on non-2xx; reject stripping the prefix in `prepare-litellm`. File number 06 by request (index row 06 is a different, unwritten finding) | `TASK-06-litellm-postrequisites-master-key-prefix.md` | keos-apps |
| 10 | `apps diff`/`apps migrate` read ConfigMap data with `unstructured.NestedStringMap` (`internal/diff/envvars.go:45`, `chartvalues.go:133`, `settle.go:79,121`); one null value (genai-ui `${LLM_MODEL_*}` empty, Flux renders it as null even when quoted) makes the whole ConfigMap read as empty, error discarded: 119 false "lost" genai-ui vars and a patch that drops real legacy values. Blocks the genai migration (2026-10-13 run held genai, rocket, intelligence, datamarket-agent, dlc-entity) | task written 2026-10-14; fix verified in a scratch build (null → `""`, warn on null keys); keos-apps hardening `${LLM_MODEL_*:-""}` | `TASK-10-diff-configmap-null-values.md` | flux-stratio, keos-apps |
| 11 | Why do chart-rendered variables differ from or miss legacy values if `values.yaml` comes from the Command Center descriptor? Inventory across apps (genai-ui 18 different, virtualizer `EXTRA_JARS`/`SERVER_MAXHTTPHEADERSIZE`, dg-hdfs-agent 11 unpatchable, rocket, ...). Hypothesis: legacy = descriptor `parameters` + lifecycle upserts + `apps-layer-automation` overrides + Command Center form inputs; the chart covers only `parameters` | investigated 2026-10-14 (read-only): same descriptor build, no chart skew; differences come from install inputs, lifecycle upserts and GitOps re-expression; fixes F1-F4 proposed (dg-agent `COMM_SERVICE_NAME`, genai `SERVICE_URL`, `DISCOVERY_DATABASE` catalog exclude, optional diff reasons), decisions D1-D6 open | `TASK-11-chart-variables-vs-cct-descriptor.md` | charts, keos-apps, flux-stratio |
| 12 | Product decisions left by the TASK-11 investigation: genai opensearch/discovery dependencies (D1), which OpenSearch holds governance (D2), virtualizer-monitor `SERVER_MAXHTTPHEADERSIZE` (D3), rocket size/startup probes (D4), genai host and path (D5), virtualizer connector jars (D6) | written 2026-10-14, nothing decided; for a separate study session | `TASK-12-chart-variable-decisions.md` | charts, keos-apps, flux-stratio |

Expected, no task: `VAULT_ROLE` hyphen to underscore on every chart app and `dg-` to `governance-`
business-glossary names are deliberate (catalog excludes; memories `vault-role-naming`,
`governance-rename-is-ours`).

## Not migrated in this run (context for later tasks)

Skipped by the user or by the tenant file: `dg-postgresql-internal-agent`, `dg-postgresql-internal-agent-op`
(chosen "skip"), `datamarket-agent`, `genai`, `litellm`, `rocket`, `intelligence` (no tenant-file entry).
All except litellm show "no drift" against their backups.

## Useful commands

```
flux stratio apps diff <app>                              # rendered tenant file + patch vs live
flux stratio apps diff <app> --baseline <backups>/<app>/2026-10-09T10-51-40Z   # same vs backup
flux stratio apps diff <app> --drift    <backups>/<app>/2026-10-09T10-51-40Z   # live now vs backup
flux stratio doctor
```
