# TASK-13 — the rocket chart renders five catalog variables into a ConfigMap nothing mounts, so neither the server nor the catalog it spawns ever sees them, and `apps diff/migrate` calls them rendered

**Status: open. Analysed 2026-10-14 (read-only, verdict and recommendation below, awaiting the user's go-ahead for the chart change). No chart, cluster or plugin change made for this finding.**

> The other TASK files this one cites (TASK-00 to TASK-12 and the un-numbered `TASK-*.md`) were retired on 2026-10-14. Their content is summarised in `docs/MIGRATION-HISTORY.md` and the files remain in git history.

Found in the eosdev legacy->GitOps migration run of 2026-10-13 (W6, rocket; see `docs/MIGRATION-HISTORY.md`).
Tools to change: `charts` (rocket chart; worktree used by the cluster `/stratio/aws/flux/.worktrees/charts-align-15.1.0`, branch
`dgutierrez-stratio/align-15.1.0`, snapshot `15.1.0-PR258-SNAPSHOT`) and `flux-stratio` (this repo, branch `feat/application-migration`).
Related: `TASK-charts-universe-alignment.md` (rocket section: "catalog-flavor ones in `rocket_catalog_env_vars.yaml`", the placement that
caused this), `TASK-04` (live-only variables the chart does not render), `TASK-11` / `TASK-12` (chart variables vs the CCT descriptor).

## Requirement for this task

Read "Migration principle" in `TASK-00`. Keep the legacy configuration; a fresh GitOps install must behave like a migrated one. Classify each
variable **preserve legacy**, **accept new** or **needs a product decision**, with evidence. Part A is a chart bug that also hits a fresh
install; part B is the plugin's false negative that let it through the migration.

## What happened

The chart renders five `SPARTA_CONFIG_CATALOG_*` variables (assume-all-databases-governed, cache expire/refresh, operations parallelism/timeout)
only into the ConfigMap `rocket-catalog-config`. That ConfigMap is the config of the chart's *catalog flavour* (`server.type=catalog`, the
legacy `rocket-catalog-standalone`). In the default flavour (`server.type=rocket`) no workload references it. The rocket server is therefore
started without the five, and so are the four children it creates at runtime (`rocket-catalog`, `-debug`, `-externalservice`, `-validator`),
which receive a filtered copy of the server's environment. Legacy had all five on the server and on every child.

`apps migrate rocket` did not report them as lost, because the env-var comparison treats every ConfigMap no workload references as the lone
workload's when the chart renders exactly one workload (part B). `apps diff --drift` did show them (live now vs backup); `apps diff` and
`apps migrate` did not.

## Evidence

### Chart (`charts-align-15.1.0/rocket`)

| Where | What |
|---|---|
| `config/rocket_catalog_env_vars.yaml:17-21` | The five lines: `SPARTA_CONFIG_CATALOG_ASSUME_ALL_DATABASES_GOVERNED`, `..._CACHE_EXPIRE_AFTER_ACCESS`, `..._CACHE_REFRESH_INTERVAL`, `..._OPERATIONS_PARALLELISM`, `..._OPERATIONS_TIMEOUT`, all `.Values.rocketCatalog.general.catalog.*` |
| `values.yaml:1615-1624` | `rocketCatalog.general.catalog`: `catalogOperationsParallelism: 4`, `catalogOperationsTimeout: "295"`, `catalogCacheRefreshInterval: 10h`, `catalogCacheExpireAfterAccess: 48h`, `catalogAssumeAllDatabasesGoverned: true` |
| `values.yaml:934-944` | `configMapsFromFile` renders every file unconditionally: `common-config`, `catalog-config`, `rocket-config`, `<storage>-config`, `<storage>-hadoop-config` |
| `values.yaml:608-618` | The one container's `envFrom`: `<release>-common-config` always, `<release>-<server.type>-config` (so `rocket-config` for `rocket`, `catalog-config` only for `catalog`), plus the storage ones when `server.type=rocket`. **Nothing references `catalog-config` when `server.type=rocket`** |
| `config/rocket_common_env_vars.yaml:119-122` | The four sibling catalog variables that did land in the shared file: `CACHE_DEFAULT_IDENTITY_REFRESH_INTERVAL`, `CACHE_MIN_AGE_FOR_INVALIDATION`, `JDBC_POOL_ENABLED`, `JDBC_POOL_IDLE_TIMEOUT` (values `rocketCommon.general.catalog`, `values.yaml:1517-1520`); `SPARTA_CONFIG_CATALOG_AVAILABLE_ENABLED` is in `rocket_server_env_vars.yaml:183` |
| Git | Introduced by `d5a7ac6` "[PLT-4049] Split rocket and catalog (#207)"; the 15.1.0 alignment plan (`TASK-charts-universe-alignment.md`, rocket section) said "catalog-flavor ones in `rocket_catalog_env_vars.yaml`" |

### Descriptor (`/stratio/p/kubernetes-universe`, release commit `54ab1918`)

- `packages/rocket/rocketbase.json:1586-1771` declares **nine** `SPARTA_CONFIG_CATALOG_*` parameters, including the five (lines 1593, 1607, 1623,
  1729, 1743). `rocketbase` is the parent of `rocket` (model `default`, the server) and of `rockets3`, so the server inherits all five.
  `rocket-catalog.json` (service `rocket`, model `catalog`, parent `stratiobase`) declares its own copy of the same parameters (lines 1167-1345).
- Defaults in both: assume-all-governed `true`, parallelism `4`, timeout `295`, expire-after-access `48h`, refresh-interval `10h`: **equal to the
  chart values and to the legacy live values**. The descriptor text for assume-all-governed: "When true, all databases are considered governed
  without querying DESCRIBE DATABASE EXTENDED. Use as emergency fallback if listing databases fails." (`rocketbase.json:1587`).
- So the descriptor gives the five to the server as well as to the standalone catalog; the chart gave them to the standalone catalog only.

### Live cluster (eosdev, namespace `stratio-rocket`, read-only, 2026-10-14 08:3x UTC)

- HelmRelease `rocket` v1, chart `15.1.0-PR258-SNAPSHOT`; HelmChart `flux-system/stratio-rocket-rocket` pulled it; the HelmRelease sets no `rocketCatalog.*` value.
- ConfigMaps rendered: `rocket-catalog-config` (35 keys), `rocket-common-config` (216), `rocket-hdfs-config` (12), `rocket-rocket-config` (256), `rocket-prerequisites` (2).
- Deployment `rocket` `envFrom`: `rocket-common-config`, `rocket-hdfs-config`, `rocket-rocket-config`. Its pod carries `checksum/configMaps`, so a change to any rendered ConfigMap
  rolls it. Inside the running server (`/proc/1/environ`, names only): `SPARTA_CONFIG_CATALOG_` has `CACHE_DEFAULT_IDENTITY_REFRESH_INTERVAL`, `CACHE_MIN_AGE_FOR_INVALIDATION`,
  `JDBC_POOL_ENABLED`, `JDBC_POOL_IDLE_TIMEOUT`, `AVAILABLE_ENABLED` and **none of the five**.
- **Orphan scan** (every Helm-labelled ConfigMap of the cluster against every Deployment/StatefulSet/DaemonSet/Job/CronJob `envFrom`, `configMapKeyRef`, volume and projected source): the only
  app-chart ConfigMap no workload references is `stratio-rocket/rocket-catalog-config`. (The others listed are the monitoring Grafana dashboards, kyverno and ingress-nginx metrics, consumed by
  sidecars/operators, not app charts.) Within the rocket chart, `rocket-prerequisites` is mounted by the prerequisites Job.
- The children are created by the server, not Helm: Deployments `rocket-catalog`, `-debug`, `-externalservice`, `-validator` have `ownerReferences` -> Deployment `rocket`, label
  `app.kubernetes.io/managed-by=rocket-rocket-server`, no `envFrom`, and 329 literal env entries each. Server log 08:16:27Z: "Deploying worker rocket-catalog ... deployment successfully created".
- Child env vs the server's `envFrom` ConfigMaps: every child variable comes from the server's environment (or is set by the server itself: `SPARTA_BOOTSTRAP_MODE=Catalog`, `VAULT_ROLE`, the
  `SPARTA_BOOTSTRAP_*_JOB_TIMEOUT`, ...), and about 200 server-only variables (`SPARTA_API_*`, `GENAI_*`, `ROCKET_CICD_*`, ...) are not forwarded. The server forwards a filtered copy of its own env.
  This is inferred from the data; the rocket-api source is not available here.
- **The five are missing on all four children and on the server; nothing else of the catalog ConfigMap is lost.** Of the 35 `rocket-catalog-config` keys, 28 also exist in the server's `rocket-common-config` / `rocket-rocket-config` (some with
  a flavour-specific value), two (`GENAI_CHAIN_PYSPARK_TRANSFORMATION_ENABLED`, `GENAI_CHAIN_SQL_TRANSFORMATION_ENABLED`) are in neither legacy nor the new server, and the five reach nobody.
- **Legacy vs new**, from the pre-migration backups (`~/.fluxcd/flux-stratio/backups/rocket/2026-10-13T11-00-17Z/env-vars.deployment.rocket.env` and
  `.../rocket-{catalog,debug,externalservice,validator}/2026-10-13T11-00-22Z/env-vars.deployment.rocket-<child>.env`; the task text gave `11-00-17Z` for `rocket-catalog`, the directory is `11-00-22Z`):

| Variable | Legacy server | Legacy child (all four) | New server | New children |
|---|---|---|---|---|
| `SPARTA_CONFIG_CATALOG_ASSUME_ALL_DATABASES_GOVERNED` | `true` | `true` | absent | absent |
| `SPARTA_CONFIG_CATALOG_CACHE_EXPIRE_AFTER_ACCESS` | `48h` | `48h` | absent | absent |
| `SPARTA_CONFIG_CATALOG_CACHE_REFRESH_INTERVAL` | `10h` | `10h` | absent | absent |
| `SPARTA_CONFIG_CATALOG_OPERATIONS_PARALLELISM` | `4` | `4` | absent | absent |
| `SPARTA_CONFIG_CATALOG_OPERATIONS_TIMEOUT` | `295` | `295` | absent | absent |

  The set difference legacy child minus new child is exactly these five plus `SSCC_S3A_GLOBAL_CREDENTIALS` (an S3 storage variable, not applicable to the hdfs storage; the alignment plan already lists
  it as "not a gap"); new-only on every child: `SPARK_SECURITY_LOCAL_EXECUTOR_ENABLED`, `SPARK_SECURITY_LOCAL_EXECUTOR_TASK_UGI_PROVIDER`. The same holds for debug, externalservice and validator.
  Legacy server minus new server also shows `CUSTOMER_SIZE`, `MIGRATION_STORAGE_PROFILE`, `NOT_USED` and the `*_PROBE_*` set, all already known and decided elsewhere.
- Why not just add `catalog-config` to the server's `envFrom` (rejected): it carries flavour-specific values that contradict the server's, e.g. `SPARTA_BOOTSTRAP_MODE=catalogstandalone` (server `remote`),
  `SPARTA_IMPERSONATION_ENABLED=false` (common `true`), `SPARTA_CONFIG_VIRTUALIZER_FORCE_JDBC=true` (server `false`), `SPARTA_PLUGIN_*_CACHE_ENABLED=false` (server `true`). The last envFrom wins, so it would break the server.
- The leftover legacy `rocket-catalog-standalone` Deployment (CCT-owned, 22 h old, `cct.stratio.com/application_model: catalog`) carries all five; it is the legacy twin of the chart's catalog flavour and is not touched here.

### The "Operation not authorized" hypothesis: **not supported**

Server log, 08:16:28-30Z (also in `.../scratchpad/logs/W6/verify/log-rocket-b6b48c47-pb6f8.txt:246-248`; three occurrences in the current pod, none in the rocket-catalog child):

```
CrossdataUtils$  Executing query with JDBC  : SHOW DATABASES
Slf4jLogger      ... Error: Operation not authorized  (x2)
CatalogCacheActor  Bootstrap JDBC fetch failed for default execution identity databases: Error: Operation not authorized
```

The virtualizer log for the same query (`stratio-apps/virtualizer-...`, session started 08:16:29Z, user `execution-identity-rocket.stratio-rocket`):
`SHOW DATABASES` -> GoSec authorization `action=Read resourceType=database resource=default -> Received authz response: false`, then `action=Write ... resource=default -> false`, then
`Spark Job ... failed: Operation not authorized`. So the denial is **GoSec refusing the execution identity access to the virtualizer database `default`**, raised while *listing* databases.
`ASSUME_ALL_DATABASES_GOVERNED` is documented as skipping `DESCRIBE DATABASE EXTENDED` (a later step) and as a fallback for a failing listing; it does not authorize the listing. The five missing variables
are real (this task) but are **not** the cause of this error. The chart's catalog GosecPolicy (`values.yaml:361-380`, `:493-510`) only grants PostgreSQL ACLs to the execution identity; no Crossdata/virtualizer
policy for it exists in the chart. Whether legacy had the same denial is not known (legacy logs are gone); a separate finding is needed (see open questions). What the missing variables do change is
the server's catalog cache behaviour (expiry, refresh, parallelism, timeout) falling back to the image's built-in defaults, which are not known.

## Part A — chart fix (`charts`, rocket) — proposed, NOT applied

Single source for both flavours: the five become shared variables. `rocket-common-config` is already `envFrom` of the server **and** of the catalog flavour, so the server, the standalone catalog and (through
the server's forwarding) the four children all get them; the descriptor already makes them inherited parameters of the server. Move, do not duplicate (a duplicate in `catalog-config` would let the two
drift, and the catalog flavour already reads `common-config` first).

`rocket/config/rocket_catalog_env_vars.yaml` (delete lines 17-21):

```diff
-SPARTA_CONFIG_CATALOG_ASSUME_ALL_DATABASES_GOVERNED: {{ .Values.rocketCatalog.general.catalog.catalogAssumeAllDatabasesGoverned | quote }}
-SPARTA_CONFIG_CATALOG_CACHE_EXPIRE_AFTER_ACCESS: {{ .Values.rocketCatalog.general.catalog.catalogCacheExpireAfterAccess | quote }}
-SPARTA_CONFIG_CATALOG_CACHE_REFRESH_INTERVAL: {{ .Values.rocketCatalog.general.catalog.catalogCacheRefreshInterval | quote }}
-SPARTA_CONFIG_CATALOG_OPERATIONS_PARALLELISM: {{ .Values.rocketCatalog.general.catalog.catalogOperationsParallelism | quote }}
-SPARTA_CONFIG_CATALOG_OPERATIONS_TIMEOUT: {{ .Values.rocketCatalog.general.catalog.catalogOperationsTimeout | quote }}
```

`rocket/config/rocket_common_env_vars.yaml` (alphabetical, around lines 119-122):

```diff
+SPARTA_CONFIG_CATALOG_ASSUME_ALL_DATABASES_GOVERNED: {{ .Values.rocketCommon.general.catalog.catalogAssumeAllDatabasesGoverned | quote }}
 SPARTA_CONFIG_CATALOG_CACHE_DEFAULT_IDENTITY_REFRESH_INTERVAL: {{ ...catalogCacheDefaultIdentityRefreshInterval | quote }}
+SPARTA_CONFIG_CATALOG_CACHE_EXPIRE_AFTER_ACCESS: {{ .Values.rocketCommon.general.catalog.catalogCacheExpireAfterAccess | quote }}
 SPARTA_CONFIG_CATALOG_CACHE_MIN_AGE_FOR_INVALIDATION: {{ ...catalogCacheMinAgeForInvalidation | quote }}
+SPARTA_CONFIG_CATALOG_CACHE_REFRESH_INTERVAL: {{ .Values.rocketCommon.general.catalog.catalogCacheRefreshInterval | quote }}
 SPARTA_CONFIG_CATALOG_JDBC_POOL_ENABLED: {{ ...catalogJdbcPoolEnabled | quote }}
 SPARTA_CONFIG_CATALOG_JDBC_POOL_IDLE_TIMEOUT: {{ ...catalogJdbcPoolIdleTimeout | quote }}
+SPARTA_CONFIG_CATALOG_OPERATIONS_PARALLELISM: {{ .Values.rocketCommon.general.catalog.catalogOperationsParallelism | quote }}
+SPARTA_CONFIG_CATALOG_OPERATIONS_TIMEOUT: {{ .Values.rocketCommon.general.catalog.catalogOperationsTimeout | quote }}
```

`rocket/values.yaml` (same defaults as today and as the descriptor):

```diff
   rocketCommon:
     general:
       catalog:
         ...
         catalogCacheMinAgeForInvalidation: 10m
+        catalogOperationsParallelism: 4
+        catalogOperationsTimeout: "295"
+        catalogCacheRefreshInterval: 10h
+        catalogCacheExpireAfterAccess: 48h
+        catalogAssumeAllDatabasesGoverned: true
         validatorSkipPredecessorCheck: false
...
 rocketCatalog:
   general:
     catalog:
       crossdataCatalogForceJdbc: true
       spartaWorkerWarmUpPythonDriver: false
-      catalogOperationsParallelism: 4
-      catalogOperationsTimeout: "295"
-      catalogCacheRefreshInterval: 10h
-      catalogCacheExpireAfterAccess: 48h
-      catalogAssumeAllDatabasesGoverned: true
       crossdataServiceName: ...
```

Compatibility: a tenant that overrides `rocketCatalog.general.catalog.catalog{OperationsParallelism,OperationsTimeout,CacheRefreshInterval,CacheExpireAfterAccess,AssumeAllDatabasesGoverned}` would
stop having effect. None does on eosdev (the tenant file and keos-apps overlays carry no `rocketCatalog` key; searched `/stratio/gitops` and `/stratio/aws/flux/.worktrees`, only the chart worktrees match). If the user
wants a catalog-only override later, keep `rocketCatalog.*` as an *override layer* (same names in both files, `catalog-config` is read last by the catalog flavour) rather than the only source.

Also (chart CI, optional): a render check that fails when a rendered ConfigMap is referenced by no workload for the default flavour. It would have caught this (and the orphan is the only one in the cluster today).
Other orphan pattern checked: there is **no** per-child config file for debug/externalservice/validator in `rocket/config/` (children get config only by the server's forwarding); `rocket_{hdfs,s3}_env_vars.yaml`
and `rocket-config` are referenced by the matching flavour; with `server.type=catalog` the `rocket-config` ConfigMap is rendered and unreferenced by design (expected, harmless).

Chart acceptance: `helm template` with `server.type=rocket` renders the five into `<release>-common-config` and no longer into `<release>-catalog-config`; with `server.type=catalog` they are still in
the pod through `common-config`; every rendered ConfigMap of the default flavour is referenced by a workload.

## Part B — plugin fix (`flux-stratio`) — proposed, NOT applied

### B1. Root cause: unreferenced ConfigMaps are credited to the lone workload

`internal/diff/envvars.go`:
- `indexRendered` (`:39-64`) already computes `orphans`: the ConfigMaps no rendered workload references (`envFrom` configMapRef, `configMapKeyRef`; `referencedConfigMaps`, `:155-185`).
- `workloadEnv` (`:66-101`), doc `:72-76` and code `:97-101`: **`if len(r.workloads) == 1 { for _, cm := range r.orphans { applyConfigMap(cm, "") } }`**. "A chart that renders a single
  workload also gets every ConfigMap no workload references ... a lone workload is the only one those values can belong to". The rocket chart renders exactly one workload (the children are runtime-created),
  so `rocket-catalog-config` is credited to the `rocket` Deployment and the five count as rendered, with the same value as live. (`stringMapAt`, `internal/diff/stringmap.go`, only decodes the ConfigMap data; it is not the
  cause. Introduced by `b383e35` "Chart mode: compare env vars per workload", which kept the older "compare every rendered ConfigMap" behaviour for a single workload. CONTEXT.md:447 records the older bug this
  was meant to soften: a variable of an unmounted ConfigMap, rocket's `rocketCatalog.*`, became a no-op patch.)
- Consumers: `chartdiff.go:486` (`workloadVars`; the `!hasRendered` branch at `:292-303` is what fills `LiveOnly`), `settle.go:79,121`, `workloadspec.go:331`.

Fix: **attribute a ConfigMap variable to a workload only through that workload's own `envFrom` / `configMapKeyRef`.** Delete the single-workload orphan block and its doc paragraph. A live variable that exists only in an
unreferenced ConfigMap then lands in `LiveOnly` (the existing `[REVIEW] N variable(s) in the live cluster aren't rendered by the chart`) instead of being silently "equal".

### B2. Report the orphan, with the reason

- `ChartDiffResult` gets `OrphanConfigMaps []OrphanConfigMap` (`Name`, `Keys` count, `LiveNames` = live variable names that exist in it). Fill it from `renderedEnv.orphans` and the live env.
- `LiveOnlyVar` gets `OrphanConfigMap string` (set when the name is a key of an orphan); `liveOnlyWhy` (`internal/cli/review_sections.go:149-161`) returns
  `rendered only into ConfigMap <name>, which no rendered workload references`. The five rocket variables then read exactly like that in the REVIEW table.
- A separate section from `review_sections.go`: `[REVIEW]` when an orphan shares variable names with the live workload ("chart renders ConfigMap X but no workload uses it; N of its variables are live: a chart bug, the
  values are not delivered"), `[NOTE]` when none do (the flavour-specific ConfigMap of a chart with a `server.type` switch is legitimately unused).
- Existing tests that depend on the old fallback (they put the ConfigMap next to a Deployment with no `envFrom`): **15 in `internal/diff`** (`TestChartDiff_ProducesMappedPatch`, `_UnmappedDiffReported`,
  `_LiveOnlyAndRenderedOnlyCounted`, `_SecretLiveValueReportedNotPatched`, `_ExcludePathsApplied`, `_ExcludedDifferenceIsReportedNotPatched`, `_ExcludedIsSortedByNameNotPath`,
  `_UnattributedAmbiguousKeyNotGuessed`, `_SharedPathWouldChangeAMatchingVariable`, `_SharedPathMatchingSameValueIsPatched`, `_ProbeVariableCarriedNativelyIsNotLost`, `TestWorkloadEnv_UnreferencedConfigMaps`,
  `TestSettledByPatch`, `TestVerifyMapped`, `TestVerifyMapped_NullKeyDoesNotDropMappedPath`) and **6 in `internal/appdiff`** (`TestDiff_ChartMode_*`). Verified on a scratch copy of the repo with the block removed
  (nothing was changed in this repo). They need their fixtures to reference the ConfigMap (`envFrom`), which is what a real chart does; add a small fixture helper for it instead of weakening the assertions.

### B3. Unit tests to add

1. `TestWorkloadEnv_UnreferencedConfigMapIsNotApplied` (`envvars_test.go`, replaces `TestWorkloadEnv_UnreferencedConfigMaps`): one Deployment with `envFrom` of `server-config`, plus ConfigMap `catalog-config`
   `{X: "1"}` that nothing references: `workloadEnv` has no `X`, `indexRendered(...).orphans == ["catalog-config"]`; the same with two Deployments; and a variable reached through `envFrom`, through
   `configMapKeyRef`, and through an `envFrom` `prefix` is still resolved.
2. `TestChartDiff_VariableOnlyInUnreferencedConfigMapIsLiveOnly` (`chartdiff_test.go`): chart renders one workload + unreferenced ConfigMap holding five `SPARTA_CONFIG_CATALOG_*` keys, live env has the same five with the same
   values: `LiveOnly` names all five with `OrphanConfigMap == "catalog-config"`, `RenderedOnlyCount` unchanged, no patch, `OrphanConfigMaps[0].LiveNames` has the five; with the ConfigMap referenced instead (`envFrom`), `LiveOnly`
   is empty and nothing is reported (regression of the fix).
3. `review_test.go`: the REVIEW section lists the five with the "rendered only into ConfigMap ..., which no rendered workload references" WHY; an orphan with no live overlap yields only the NOTE.
4. Fixture reproducing the real case under `internal/diff/testdata`: the rocket chart's shape (`common-config`, `rocket-config` referenced; `catalog-config` unreferenced) with the legacy server env; `apps diff rocket --baseline` must list the five.

### B4. The runtime-created children are never compared (gap, secondary)

`apps diff/migrate rocket` compares only the Deployment `rocket`. The children are skipped on purpose (`internal/config/seed.go:382-385`, `docs/config-reference.md:125-127`: objects with `ownerReferences` are never an instance),
`apps diff rocket-catalog` fails with "none is selected by any catalog type" (`.../scratchpad/logs/W6/verify/rocket-catalog-diff.txt`), and Helm renders no counterpart for them. Their legacy backups exist
(`~/.fluxcd/flux-stratio/backups/rocket-{catalog,debug,externalservice,validator}/2026-10-13T11-00-22Z`) but nothing compares them with the new children's live env.

Because the server forwards its own env, fixing B1/A also fixes the children: the comparison of the server's env is the right check, and a children comparison only confirms it. Proposed (optional, decide with the user):
a catalog field (e.g. `chart.runtimeChildren: [{ownerDeployment: rocket, labelSelector: ...}]`) so that `apps diff --drift` and `apps migrate`'s post-check also diff the legacy child env files against the live children after
the cutover and print the difference as a `[REVIEW]`. Not needed for the fix of this finding.

### B5. Docs

`CONTEXT.md` (the chart-mode section and the table at `:447`: the unmounted-ConfigMap rule changes from "credited to a lone workload" to "reported as an orphan"), `docs/migration-runbook.md` (what an
orphan note means), `CHANGELOG.md`.

## Per-variable classification

| Variable | Legacy | Chart default | Verdict | Reason |
|---|---|---|---|---|
| `SPARTA_CONFIG_CATALOG_ASSUME_ALL_DATABASES_GOVERNED` | `true` | `true` | **preserve legacy** | Descriptor default `true`; the server and children had it. Without it they use the image's built-in default (unknown). Not the cause of "Operation not authorized" |
| `SPARTA_CONFIG_CATALOG_CACHE_EXPIRE_AFTER_ACCESS` | `48h` | `48h` | **preserve legacy** | Equal to descriptor and legacy |
| `SPARTA_CONFIG_CATALOG_CACHE_REFRESH_INTERVAL` | `10h` | `10h` | **preserve legacy** | Equal to descriptor and legacy |
| `SPARTA_CONFIG_CATALOG_OPERATIONS_PARALLELISM` | `4` | `4` | **preserve legacy** | Equal to descriptor and legacy |
| `SPARTA_CONFIG_CATALOG_OPERATIONS_TIMEOUT` | `295` | `"295"` | **preserve legacy** | Equal to descriptor and legacy (seconds, below the 300 s actor timeout the descriptor mentions) |
| `SSCC_S3A_GLOBAL_CREDENTIALS` (seen in the same diff) | `s3...` | n/a (hdfs storage) | accept new | Already judged not a gap in `TASK-charts-universe-alignment.md`; belongs to the S3 storage file |
| `SPARK_SECURITY_LOCAL_EXECUTOR_*` (new on every child) | absent | server-set | accept new | New in 4.1.0; not part of this finding |

No variable needs a product decision here: legacy, descriptor and chart agree on all five. The decisions that remain are in "Open questions".

## eosdev remediation once the chart is fixed (not applied)

1. Merge the chart change and **republish the snapshot** `15.1.0-PR258-SNAPSHOT` (the user's usual PR258 snapshot flow).
2. Flux keeps a same-version cached chart: **delete the HelmChart `flux-system/stratio-rocket-rocket`** so it is pulled again (learned with F1), then force the reconcile of the HelmRelease
   (`flux reconcile hr rocket -n stratio-rocket --with-source` or the `stratio-rocket-rocket` HelmChart first; see "Force Flux reconcile").
3. The rendered ConfigMaps change, so `checksum/configMaps` rolls the `rocket` Deployment. The server recreates/updates its children at start (log: "Deploying worker rocket-catalog ...").
   **Not verified:** whether the server patches an already existing child Deployment. If a child's env still lacks the five after the server is up, delete the four child Deployments
   (`rocket-catalog`, `-debug`, `-externalservice`, `-validator`; the server owns and recreates them) one at a time and let the server redeploy them. Do not touch `rocket-catalog-standalone` (legacy cutover stays manual).
4. Verify (names and values of non-secret variables only):
   - `kubectl -n stratio-rocket get cm rocket-common-config -o json | jq '.data|with_entries(select(.key|test("^SPARTA_CONFIG_CATALOG_(ASSUME|CACHE_(EXPIRE|REFRESH)|OPERATIONS)")))'` shows the five, and `rocket-catalog-config` no longer has them.
   - `/proc/1/environ` of the server and of each child lists the five names; values `true`, `48h`, `10h`, `4`, `295`.
   - Child env names vs the legacy child backups: the only differences left are `SSCC_S3A_GLOBAL_CREDENTIALS` (legacy only) and the two `SPARK_SECURITY_LOCAL_EXECUTOR_*` (new only).
   - `flux stratio apps diff rocket --drift ~/.fluxcd/flux-stratio/backups/rocket/2026-10-13T11-00-17Z` no longer shows the five as removed.

## Acceptance criteria

- Chart: `helm template` (hdfs storage, `server.type=rocket`) renders the five in `<release>-common-config`, none in `<release>-catalog-config`, and a script or CI check confirms every rendered ConfigMap of the
  default flavour is referenced; `server.type=catalog` still gets them (through `common-config`).
- eosdev: server and the four children carry the five with the legacy values; the child-env difference against the legacy backups is the one listed above; the server logs no new errors.
- Plugin: the tests in B3 pass and the 21 listed fixtures are updated, not weakened; on the real eosdev data, before the chart fix, `flux stratio apps diff rocket --baseline <rocket backup>` lists the five in the
  REVIEW table with the orphan WHY (and `apps migrate rocket` does too), and after the chart fix lists none; `go vet ./...` and `go test ./...` pass.
- `CONTEXT.md`, `docs/migration-runbook.md` and `CHANGELOG.md` updated.

## How to reproduce

```
# the orphan: five keys in the catalog ConfigMap, referenced by no workload
kubectl -n stratio-rocket get cm rocket-catalog-config -o json | jq -r '.data|keys[]' | grep -E 'ASSUME_ALL|CACHE_(EXPIRE|REFRESH)|OPERATIONS_'
kubectl -n stratio-rocket get deploy rocket -o json | jq -c '.spec.template.spec.containers[0].envFrom'          # common, hdfs, rocket
kubectl -n stratio-rocket get deploy rocket-catalog -o json | jq -r '.spec.template.spec.containers[0].env[].name' | grep -c 'ASSUME_ALL\|OPERATIONS_TIMEOUT'   # 0
kubectl -n stratio-rocket exec deploy/rocket -- sh -c 'tr "\0" "\n" </proc/1/environ | cut -d= -f1 | grep -c "SPARTA_CONFIG_CATALOG_ASSUME\|CACHE_EXPIRE\|OPERATIONS_"'  # 0
# legacy had them on both
grep -E '^SPARTA_CONFIG_CATALOG_(ASSUME_ALL_DATABASES_GOVERNED|CACHE_EXPIRE_AFTER_ACCESS|CACHE_REFRESH_INTERVAL|OPERATIONS_PARALLELISM|OPERATIONS_TIMEOUT)=' \
  ~/.fluxcd/flux-stratio/backups/rocket/2026-10-13T11-00-17Z/env-vars.deployment.rocket.env \
  ~/.fluxcd/flux-stratio/backups/rocket-catalog/2026-10-13T11-00-22Z/env-vars.deployment.rocket-catalog.env
# the plugin false negative (lists CUSTOMER_SIZE, MIGRATION_STORAGE_PROFILE, NOT_USED, SSCC_S3A_..., never the five); the drift view does show them
flux stratio apps diff rocket --baseline ~/.fluxcd/flux-stratio/backups/rocket/2026-10-13T11-00-17Z -v
flux stratio apps diff rocket --drift    ~/.fluxcd/flux-stratio/backups/rocket/2026-10-13T11-00-17Z
# the unrelated denial
kubectl -n stratio-rocket logs deploy/rocket | grep -E 'SHOW DATABASES|Operation not authorized|Bootstrap JDBC'
kubectl -n stratio-apps logs deploy/virtualizer --since=2h | grep -E 'execution-identity-rocket.*Received authz response: false'
# the plugin code
sed -n 66,101p internal/diff/envvars.go        # workloadEnv: the single-workload orphan block at 97-101
```

## Open questions for the user

- Move the five to `rocketCommon` (recommended, single source) or keep `rocketCatalog.*` as an override layer on top?
- The "Operation not authorized" at 08:16:29Z is a GoSec denial of `Read`/`Write` on virtualizer database `default` for `execution-identity-rocket.stratio-rocket`. Open a separate finding? Check first
  whether legacy had a Crossdata policy for that identity (the chart grants only PostgreSQL ACLs to it), and whether the denial repeats or only happens on the bootstrap fetch.
- Should `apps migrate` stop `--yes` when an orphan ConfigMap shares variable names with the live workload (B2 `[REVIEW]`), or only print it?
- Do the runtime-created children (B4) deserve a catalog field and a post-cutover comparison, or is fixing the server comparison enough?

## Related

- `TASK-00` index (the orchestrator adds this entry); `TASK-charts-universe-alignment.md` (rocket section, where the placement came from); `TASK-04` (the mirror case: live-only variables); `TASK-11`, `TASK-12`.
- Scratch data of this analysis: `/tmp/claude-1001/-stratio-gitops-flux-stratio/8951e99e-48a0-404d-9a81-f913f1865961/scratchpad/t13/` (name lists and value tables, no secrets).
