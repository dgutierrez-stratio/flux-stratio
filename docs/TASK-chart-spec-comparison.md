# Task: compare workload specs (resources, replicas, image) in chart-mode, and warn

**Status: implemented on branch `feat/application-migration` (uncommitted), items 1-4 done, item 5 investigated (open question for the owner, see "Findings").** Repo: `flux-stratio` (this one). Builds on the excluded-differences report
(`chartReview.Excluded`, `reportChartReview` in `internal/cli/apps.go`), already landed.
Related tasks: `docs/TASK-keos-apps-env-list-overlays.md`, `docs/TASK-charts-universe-alignment.md`.

**Universe reference:** any chart or CCT-descriptor comparison done for this task (e.g. checking a
chart's default image tag or resources against the descriptor) uses **release 15.1 of
`kubernetes-universe`** (`origin/branch-15.1`), never `master`/14.x. Record the commit SHA used. See
"Universe reference" in `docs/TASK-charts-universe-alignment.md`.

## Problem

Chart-mode (`apps diff`/`apps migrate` of a HelmRelease-backed app) compares **only environment
variables** of each live workload against the rendered one. Everything else a migration changes is
invisible: container `resources`, `replicas`, image tag, probes, volumes, priorityClass. The diff then
says "nothing to migrate" while the migrated pod is different. The legacy Python client has the same
gap (`cmd_patch_chart.py:106-144` reads ConfigMaps + container env only), so this is not a regression,
but it is the largest real migration-safety hole found on eosdev.

Where it happens:
- `internal/appdiff/chart.go:81-103` (`liveWorkloadEnvs`) fetches each live workload paired with its
  rendered one (`fetchWorkloadPairs`, `:283`), calls `envvars.Extract` and **keeps only the env map**;
  the live object is in hand and dropped.
- `internal/diff/chartdiff.go:188` (`ChartDiff`) consumes `LiveWorkloadEnv{Rendered, Env}` only.
- Precedent that the data exists: `internal/drift/drift.go:117-135` compares workload `.spec`
  between a backup and live; backups store whole manifests (`internal/backup/backup.go:536-545`).

## Measured on eosdev (2026-10-02, render = tenant file with its patches, `size: S`)

13 of 14 chart-rendered live workloads would change resources. Not exhaustive; re-measure after the
charts alignment task, because stale chart defaults inflate this.

| Workload | Live | Rendered at S | Why it matters |
|---|---|---|---|
| dg-hdfs-agent | req 4cpu/4096MB, lim 8cpu/4096MB | req 0.5/2048Mi, lim 8/2048Mi | Patch carries `JAVA_OPTS=-Xmx3072m` (tenant `stratio.yaml:549`) → heap > limit → OOMKill. **Critical** |
| rocket | lim 4cpu/4096MB | lim 1cpu/2046Mi (M: 2cpu/4092Mi) | memory −48%, cpu limit 4→1 |
| discovery | req/lim 2cpu/4096MB | req 0.5/lim 1, 2048Mi | halved; patch carries `resources.memory: 4096` |
| datamarket-agent | lim 1.5cpu/2048MB | 1024Mi at every size | patch carries `podMemoryLimit: 2048` > container limit |
| dg-datarest-pgi | req 2/4096MB, lim 4/4096MB | req 0.2/1536Mi, lim 0.6 | patch carries `podCpuLimit: 4`, `podMemoryLimit: 4096` |
| intelligence | **no memory limit** | 512MB req = limit | unbounded workload becomes capped |
| eureka-agent | 768MB | 7168Mi | ×9, scheduling risk |
| virtualizer, litellm, genai-api/ui | lower | higher | increases; litellm patch says `cpuLimit "0.5"` but container gets 1 |

Other fields: image downgrades dg-hdfs-agent 1.2.0→1.1.1 and eureka-agent 1.1.0→1.0.1 (the patch
carries `bdlAgentVersion: 1.1.0` only as an env var); replicas 0→1 for eureka-agent and
dg-datarest-pgi; probes/`priorityClassName`/`imagePullPolicy` differ (low); intelligence mounts
`hdfs-not-required-null-config` where live uses `hdfs1-stratio-datastores-config`. PVC names,
nodeSelector, tolerations, securityContext: no differences.

Root cause for part of it: the tenant file says `size: S` (`spec.defaultValues.size`; also
`tenant import --size` defaults to S, `internal/cli/tenant.go:36`) while live rocket has
`CUSTOMER_SIZE=M`. Many differences are overlay S/M/L sizing vs CCT hand-tuned sizes that fit none.

## Changes to make

### 1. `diff.CompareWorkloadSpecs` + report (core)
- New `internal/diff/workloadspec.go`: `CompareWorkloadSpecs(rendered, live *unstructured.Unstructured) []SpecDiff`.
  Compare, per container: `resources.requests/limits` (normalize quantities with
  `k8s.io/apimachinery/pkg/api/resource`, so `4096MB` vs `3.8Gi` is judged by value), `image`
  repo:tag, plus workload `spec.replicas`. Pair containers by name; if a workload has a single
  container on each side, pair them regardless of name (legacy names differ: `genai-api` vs `api`).
  `SpecDiff{Workload, Container, Field, Rendered, Live, Severity}`.
- Severity (drives wording only; all are warnings): *risk* = live memory request/limit larger than
  rendered, a limit appearing where live had none, image tag differing, replicas 0→N;
  *info* = increases, build-suffix-only image differences.
- `internal/appdiff/chart.go`: `liveWorkloadEnvs` already holds the pairs; call it there and return
  `SpecDiffs` on `appdiff.Result` (next to `UnmappedDiffs`/`LiveOnly`, `chart.go:69`).
- `internal/cli/apps.go`: `chartReview.SpecDiffs`; print in `reportChartReview` (`:445`) as
  `Warningf` grouped per workload, and include it in `hasWarnings()` (`:436`) so `migrate --yes` stops
  unless `--accept-warnings` (same gate as other review items, `apps_migrate.go:242`). Info-severity
  lines print via `Actionf` and do not count as warnings.
- Baseline mode (`--baseline`): read the backup's `deployment.yaml` / `workload.<kind>.<name>.yaml`
  as the live side (same files `drift` compares).
- Image tags and replicas are **never** carried into a patch. Warning only.
- Tests: `internal/diff/workloadspec_test.go` (quantity normalization, single-container pairing,
  no-limit→limit, tag diff, replicas); `internal/appdiff` test with a fixture live Deployment whose
  resources differ; `internal/cli/apps_report_test.go` for printing and `hasWarnings()`.

### 2. Cross-check carried env values against the rendered limit
Some patched env values embed a size that the container limit then contradicts. After computing the
patch, warn when a carried value exceeds the rendered container memory limit: `JAVA_OPTS`/`-Xmx`
(dg-agent), `VR_POD_MEMORY_LIMIT`, `MEMORY` (discovery), `podMemoryLimit` family. Generic rule:
parse `-Xmx<N>[kmg]` from any carried env value; for the named size variables compare as MB. Report
as a `SpecDiff` of field `env-vs-limit`. Keep the list of size-variable names in the catalog
(`config.ComponentType`, per type) rather than hardcoded, and document it in
`docs/config-reference.md`; update `internal/config/seed.go` in the same change.

### 3. Size check
Warn (do not infer or rewrite) when the tenant file's `size` differs from the size the live app
reports (`CUSTOMER_SIZE` in the live env when present, e.g. rocket M). One line:
`tenant size S but live rocket reports CUSTOMER_SIZE=M: overlay sizing will differ from live`.
Do not change `tenant import --size` default in this task.

### 4. Name chart defaults hidden by a list replacement
Today the generic "N variables aren't rendered by the chart: their values will be lost" does not say
when a **chart default** env entry is shadowed by the HelmRelease's own list (the keos-apps rocket
overlay replaces `controllers.rocket.containers.rocket.env`, hiding `ROCKET_POD_NAME`).
- Compare the chart's default env list names (`in.Values` before merge: see `internal/appdiff/chart.go:169-186`,
  `internal/diff/settle.go:21-36`) with the merged list; for each name present in the defaults,
  absent in the merged list, and present in live, add to the warning:
  `chart default env X is shadowed by the list at controllers.<c>.containers.<c>.env`.
- Fix the stale comment at `internal/diff/inline.go:11-16`: it claims no chart env file sets the
  `SPARTA_BOOTSTRAP_*` variables; `rocket/config/rocket_server_env_vars.yaml:153-173` does.

### 5. Verify the selector change (investigate, then decide)
On every rendered chart workload `spec.selector.matchLabels` changes from
`cct.stratio.com/application_id` to `app.kubernetes.io/*`. Deployment selectors are immutable, so
Flux/Helm cannot patch an adopted legacy Deployment in place. Only `prepare-dlc`
(`internal/prepare/step_dlc.go`) deals with it. Find out how the other 13 apps are expected to
cross over (does helm-controller delete/recreate? is there a force-recreate setting in the keos-apps
HelmRelease, `spec.upgrade.force`?). Deliver a short written finding in this file's "Findings"
section. If any app would fail to apply, add a warning (and propose a `prepare` step) rather than
guessing.

## Decision (2026-10-02, the owner): carry the legacy resources
The patch **covers all resource differences**: live resources the legacy deployment was modified with are
adopted (rule `migration-preserves-legacy`), not only the larger ones and not opt-in. Built as
`internal/diff/resourcepatch.go`: `controllers.<controller>.containers.<container>.resources` (controller
from the pod labels), requests and limits together, a limit live never had written as `null` (kustomize's
merge patch deletes the key), verified by re-rendering (`VerifyResources`; what the render doesn't
reproduce is taken out and stays a warning). Image tags and replicas are still **warnings only**.

## Do not
- Do not patch image tags, replicas, probes, priorityClass, selectors.
- Do not make resource differences silent by excluding them in the catalog.

## Verification
- `make fmt-check vet lint test build`.
- eosdev (read-only): `flux stratio apps diff dg-hdfs-agent` prints the `-Xmx3072m` vs 2048Mi
  warning and `--yes` migrate stops; `apps diff rocket` prints the resource, `CUSTOMER_SIZE` and
  `ROCKET_POD_NAME`-shadowed warnings; `apps diff dlc-entity` has no new noise.
- Re-measure the table above and attach the new numbers to this file.

## Implementation notes (what landed)
- `internal/diff/workloadspec.go`: `CompareWorkloadSpecs`, `CompareLiveSpecs`, `CheckEnvAgainstLimits`;
  `internal/diff/resourcepatch.go`: `resourceEdits`, `VerifyResources`; `MergePatch` (settle.go) renders a
  patch the way kustomize applies it (null deletes).
- `appdiff.Result.{SpecDiffs,CarriedResources,LiveSize}`, copied into `appmigrate.Result`; `--baseline` reads
  the live side from `deployment.yaml` / `workload.<kind>.<name>.yaml` by what the manifest declares.
- `apps diff|migrate` print `carrying the legacy resources of …` (information), risky spec differences as
  warnings (they stop `migrate --yes`), the rest as information, `tenant size S but live X reports
  CUSTOMER_SIZE=M` (warning, does not gate), and `chart default env X is shadowed by the list at …`.
- New catalog field `memoryEnv` (seeded for bdl-datarest, eureka-agent, virtualizer, discovery); an existing
  `~/.fluxcd/flux-stratio/catalog.yaml` keeps its old content until it is re-seeded or edited. `-Xmx` is
  checked whatever the variable. datamarket-agent has no known size variable name yet.
- `inline.go`'s stale comment corrected (the chart's `rocket_server_env_vars.yaml` does set `SPARTA_BOOTSTRAP_*`).

## Findings: re-measure on eosdev (read-only, charts #258 + keos-apps #152, 2026-10-02)
`apps diff <app>` after the change. Every resource difference below is **carried** by the patch and
verified by a real `helm template`, except where noted.

| Workload | Legacy (live) | GitOps at `size: S` | Result |
|---|---|---|---|
| dg-hdfs-agent | req 4cpu/4096M, lim 8cpu/4096M | req 0.5/2048Mi, lim memory 2048Mi | carried; the `-Xmx3072m` check passes (limit now 4096M); migrated into the working tree, second run "nothing to migrate" |
| rocket | req mem 4096M, lim 4cpu/4096M | req mem 2046Mi, lim 1cpu/2046Mi | carried; `tenant size S but live rocket reports CUSTOMER_SIZE=M` warning |
| discovery | req/lim 2cpu/4096M | req 0.5 / lim 1, 2048Mi | carried |
| datamarket-agent | req/lim mem 2048M | 1024Mi | carried |
| dg-datarest-pgi | req 2/4096M, lim 4/4096M | req 0.2/1536Mi, lim 0.6 | carried; **replicas rendered 1, live 0: warning** |
| intelligence | no memory limit | 512M req = limit | carried (`limits.memory: null`, reproduces) |
| eureka-agent | 768M | 7168Mi | carried; **replicas rendered 1, live 0: warning** |
| virtualizer, litellm, genai-api/ui, dlc-entity | lower than the overlay | overlay | carried (dlc-entity: legacy `1536M` vs overlay `1536Mi`, same number, different unit) |
| litellm | image `…genai-litellm:0.3.0` | chart `0.3.0-b7465f3` | information only (PLT-4838 owners) |

Item 5, selectors: **confirmed, and now warned about.** Every live legacy Deployment checked on eosdev selects on
`cct.stratio.com/application_id` (rocket-api, discovery, virtualizer, dg-hdfs-agent, dg-datarest-pgi,
eureka-agent, datamarket-agent, genai-api/ui, litellm, intelligence, dlc-entity); the chart renders
`app.kubernetes.io/*`. The keos-apps HelmReleases checked (rocket, dg-agent, virtualizer, dlc-entity) set
`spec.upgrade.force: false` and nothing recreates a workload. A server-side dry-run (`kubectl apply
--server-side --force-conflicts --dry-run=server`, nothing persisted) of the chart's rendered `intelligence`
Deployment over the live one was rejected by the API server: `spec.selector: Invalid value: …: field is
immutable`. A Helm release can't adopt a Deployment it doesn't own either, so the legacy Deployment has to be
deleted before Flux installs the release; only `prepare-dlc` automates it (the other apps rely on the manual
decommission step the runbook leaves to the operator). Built: `CompareWorkloadSpecs` reports a `selector`
difference as a risk (so `apps migrate --yes` stops unless `--accept-warnings`), downgraded to information for an app
whose prepare step has `ReplacesWorkloads` (only `prepare-dlc`). **Still open for the owner:** a generic prepare step that
deletes the legacy workload for the other apps, or leaving the cutover manual as today.

## Tested against `stratio.yaml` (eosdev, 2026-10-02)
The 19 resolvable apps of the tenant file migrated in place with `apps migrate --all --yes --accept-warnings --skip-prepare`
(charts #258 and keos-apps #152 through `--repo`; nothing committed in keos-fleet; the unmigrated 2 instances were
already unresolved before). Then:
- `apps migrate --all --dry-run` again: every app "nothing to migrate" (idempotent); comment lines of the tenant file
  unchanged; only `patches:` blocks differ from the snapshot taken before the first write.
- `flux-operator build rset` renders all 9 resource sets with the migrated tenant file.
- End to end (a scratch build that keeps the tenant patches applied, so kustomize applies them to the HelmRelease and
  `helm template` renders the result, compared with live): **no resource difference left on any app**, except
  intelligence's unset memory limit. The check found a bug the unit tests had missed: a `null` in a patch is dropped by
  kustomize when the key isn't already in the HelmRelease values, so a chart default (`5.12E8` for intelligence) can't
  be unset by a patch. `MergePatch` now does the same, intelligence's carry is rejected by the re-render, and the memory
  limit is a warning instead. Remaining differences: replicas 0 to 1 (eureka-agent, dg-datarest-pgi), litellm's image
  tag (information), the selector (above), and the env differences that were already there.
