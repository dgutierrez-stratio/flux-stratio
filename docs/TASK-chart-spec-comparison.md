# Task: compare workload specs (resources, replicas, image) in chart-mode, and warn

**Status: open.** Repo: `flux-stratio` (this one). Builds on the excluded-differences report
(`chartReview.Excluded`, `reportChartReview` in `internal/cli/apps.go`), already landed.
Related tasks: `docs/TASK-keos-apps-env-list-overlays.md`, `docs/TASK-charts-universe-alignment.md`.

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

## Open decision (ask the user before building)
Optionally carry **larger-than-rendered live resources** into the patch through
`controllers.<component>.containers.<container>.resources` (the controller key equals the pod's
`app.kubernetes.io/component` label in every chart checked), re-render to verify (as
`diff.VerifyMapped` does for env), opt-in flag only because it overrides the S/M/L overlays. Default
for this task: **do not build**; warnings only.

## Do not
- Do not patch image tags, replicas, probes, priorityClass, selectors.
- Do not make resource differences silent by excluding them in the catalog.

## Verification
- `make fmt-check vet lint test build`.
- eosdev (read-only): `flux stratio apps diff dg-hdfs-agent` prints the `-Xmx3072m` vs 2048Mi
  warning and `--yes` migrate stops; `apps diff rocket` prints the resource, `CUSTOMER_SIZE` and
  `ROCKET_POD_NAME`-shadowed warnings; `apps diff dlc-entity` has no new noise.
- Re-measure the table above and attach the new numbers to this file.
