# TASK-04 — virtualizer: variables the chart does not render, annotation fieldRef false positive, and leftover legacy objects

**Status: open. Investigation + chart work + plugin work. No cluster change made for this finding.**
Found in the eosdev batch-migration audit of 2026-10-09 (see `TASK-00-eosdev-audit-index.md`).
Tools to change: `flux-stratio` (this repo, branch `feat/application-migration`) and the `virtualizer`
chart in `Stratio/charts` (local worktree `/stratio/aws/flux/.worktrees/charts-align-15.1.0`).

## Requirement for this task

Read "Migration principle" in `TASK-00`. For **each** variable and object below, investigate whether the
difference makes sense in the new GitOps-with-charts scenario **and why**, record the conclusion with
evidence in this file before changing a tool, and classify it **preserve legacy**, **accept new** or
**needs a product decision**. Keep each client's original configuration as close as possible; welcome real
improvements; assume neither direction. The verdicts in the table are the audit's first reading, not final.

## What happened

`apps migrate virtualizer` (chart `virtualizer`, namespace `stratio-apps`, three workloads from one
HelmRelease: `virtualizer` (Crossdata server), `virtualizer-monitor`, `virtualizer-ui`) listed before the
user applied it:

```
[REVIEW] 2 variable(s) differ and can't be written to the patch      VR_DEPLOYMENT, VR_SERVICE
[REVIEW] 4 variable(s) in the live cluster aren't rendered by the chart
    Their values are lost after migration.
  virtualizer-monitor/SERVER_MAXHTTPHEADERSIZE  65536
  virtualizer/EXTRA_JARS                        ,,,,<sscc-hdfs url>,<sscc-postgresql url>,
  virtualizer/EXTRA_JARS_SPARK_CLASSPATH        ,,,,
  virtualizer/EXTRA_JARS_SPARK_EXECUTOR_CLASSPATH  ,
```

`prepare-virtualizer` then deleted the three legacy Deployments (backups
`~/.fluxcd/flux-stratio/backups/virtualizer/2026-10-09T10-51-40Z` and `…10-54-07Z`) and Flux installed
`virtualizer@15.1.0-PR258-SNAPSHOT` at ~10:55-10:58Z. The audit's `apps diff virtualizer --drift <backup>`
confirmed the four variables are gone from the live workloads. All three pods are `1/1 Running`, 0
restarts, with no ClassNotFound or header errors in their logs.

> **`EXTRA_JARS` / `EXTRA_JARS_SPARK_*` are now analysed in `TASK-virtualizer-decoupled-connector-jars.md`** (root cause in the chart, history, evidence, recommended solution). The row below is superseded.

## Audit facts and verdicts (read-only subagent; verify what you rely on)

| Variable | Legacy | New | What it does (source) | First verdict |
|---|---|---|---|---|
| `EXTRA_JARS` | `,,,,<sscc-hdfs-1.1.1 url>,<sscc-postgresql-1.6.1 url>,` (connectors-management URLs) | unset | The image entrypoint downloads these over HTTP onto the driver classpath (`/docker/lib/environments/context.sh:69`). Composed in `virtualizer-default.json:5302-5308` as `extensionsList, S3, ADLS2, GCS, connectorJdbc.connectorJdbcDrivers`; the last part is filled by `/stratio/aws/apps-layer-automation/apps/virtualizer/pre-install.sh:32-66` only when `VIRTUALIZER_DECOUPLED_CONNECTOR_POSTGRES` / `_HDFS` are `true`. Defaults are `false` (`vars.env`); `narnia/extensions.yaml:76-77`, the profile for this kind of environment, sets both `true`. | **Needs a decision, leaning preserve.** Real loss, but only for environments that opted in (eosdev did). |
| `EXTRA_JARS_SPARK_CLASSPATH`, `EXTRA_JARS_SPARK_EXECUTOR_CLASSPATH` | `,,,,` and `,` | unset | Spark classpaths; every part empty. | **Accept** (no-op). |
| `SERVER_MAXHTTPHEADERSIZE` (monitor only) | `65536` | unset | Tomcat request-header limit (default 8 KB). A default of the legacy deployment repo, not of the descriptor: `apps-layer-automation/apps/virtualizer-monitor/virtualizer-monitor.json.tmpl:24-28` via `general.custom` (stray copy at line 74), introduced in 14.8, so it applies to every client. The universe descriptor and the chart never define it. Probable reason: large oauth2-proxy session cookies (`sessionStorage.type: cookie`, cookie `stratio-cookie`); no commit message says so. | **Needs a decision, leaning preserve.** Real risk for clients with many groups (larger cookies). |
| `CROSSDATA_STORAGE_PATH`, `CROSSDATA_SSCC_S3/ADLS2/GCS_DEFAULT` | unset | `/data`, `false`, `false`, `false` | From the chart: `virtualizer_hdfs_env_vars.yaml` (storage path = `server.storage.hdfs.path`, S3 flag) and `virtualizer_common_env_vars.yaml:129-130` (ADLS2, GCS). `/data` is an HDFS path (matches `CONF_KV_spark_sql_warehouse_dir=/data`), no volume needed. They disable the bundled cloud connectors on an HDFS install. | **Accept.** What server 1.1.0 does with them is not verified (read in Java; the entrypoint does not reference them). |
| `VR_DEPLOYMENT`, `VR_SERVICE` | fieldRef `metadata.annotations['cct.stratio.com/application_name']` = `virtualizer` (`deployment.yaml:1457-1461`, `1623-1627`; pod-template annotation line 1305) | literal `virtualizer` (chart default `VR_SERVICE` is `{{ .Release.Name }}`) | Service and deployment name. | **Accept: plugin false positive** (values are equal). |
| `GOVERNANCEAPI_FULL_NAME`, `GOVERNANCEAPI_URI`, monitor `VAULT_ROLE` | `dg-businessglossary-api…`, `stratio-apps-virtualizer-monitor` | `governance-businessglossary-api…`, `stratio-apps_virtualizer-monitor` | Deliberate renames, excluded by the catalog. | **Accept** (memories `governance-rename-is-ours`, `vault-role-naming`). |

Evidence for "EXTRA_JARS is a loss only if the decoupled connectors are used":
- The live server loads the bundled `postgresql-42.7.11.jar` and `native-connector_2.12-4.1.0`
  (`NATIVE_CONNECTOR_DEFAULT=true`, `CROSSDATA_POSTGRES_DEFAULT=true`; the same two flags were set in the
  backup). No ClassNotFound errors; the healthcheck queries succeed.
- **Not verified:** whether anything on eosdev queries through the decoupled HDFS/PostgreSQL SSCC jars.
  Missing jars only show up when a query uses them.
- The deployed chart is byte-identical to the worktree chart (compared with `helm pull` + `diff -rq`;
  only `Chart.yaml`/`Chart.lock` differ), so there is no chart skew on this app.
- The chart sets `EXTRA_JARS` only for s3/adls/gcs (`config/storage/virtualizer_{s3,adls,gcs}_env_vars.yaml`,
  only when `defaultConnector=false`). `virtualizer_hdfs_env_vars.yaml` has no `EXTRA_JARS` and there is no
  HDFS or PostgreSQL decoupled option. The `common` library chart has the helpers
  (`common/templates/dependencies/connectors/_hdfs.tpl`, `dependencies.connectors.config.hdfs.connectorJar`)
  but virtualizer does not use them.
- The same two jars are carried by the eureka-agent patch as `bdlAgentAdditionalJars`
  (`keos-fleet/.../stratio.yaml` ~line 581), so that app did keep them.
- `docs/TASK-charts-universe-alignment.md:315-318` classifies `EXTRA_JARS*` as "not gaps to track". That is
  accurate for the descriptor but wrong for HDFS, where the chart wires nothing. Correct that note.

Evidence for the header limit:
- The monitor is Spring Boot with Tomcat 10.1.57 on port 28000. No header/431/exception lines in the
  monitor's whole log since the cutover. The ingress controller prints no access logs, so 431/400 at the
  ingress could not be checked; it has `proxy-buffer-size: 16k`.
- **Not verified:** whether Spring Boot 3 still honours the old `server.max-http-header-size` property name.

## Other differences found (investigate; none judged yet)

- `imagePullPolicy`: `IfNotPresent` (legacy) to `Always` (chart). Investigate why the chart chose `Always`
  and whether that is a deliberate improvement or a regression for air-gapped / registry-rate-limited sites.
- The downwardAPI volume `/var/stratio/namespace` (volume `ns-name`) and its mount are gone on all three
  workloads. Check whether any container reads that file.
- `virtualizer-placements` ConfigMap volume renamed to `virtualizer-placements-config` (optional; the
  ConfigMap exists in neither the old nor the new setup).
- UI port name `virtualizer-ui` to `ui`; `terminationGracePeriodSeconds` removed from the monitor and UI
  probes. Check Services/ServiceMonitors/Ingresses that refer to the port name.
- **Leftover legacy ingresses:** `virtualizer-monitor-virtualizer.stratio.k8s.eosdev…` and
  `virtualizer-ui-virtualizer.stratio.k8s.eosdev…` still exist next to the new `admin.k8s…` ingresses.
  `prepare-virtualizer` deletes only Deployments. Find out whether the old ingresses still route
  (through the same Service) and whether they should be removed, and whether other `prepare-*` steps leave
  the same kind of leftover (compare with TASK-01, which asks the same question for deleted-but-not-backed-up
  objects).
- The log line `/data/crossdata/materializedviews don't exist` comes from the unchanged
  `CROSSDATA_DELEGATED_FINE_GRAINED_BASE_PATH` (same value legacy and live): not caused by the migration.

## Plugin behaviour today (facts for the implementer)

- Live-only variables (in the legacy workload, not rendered by the chart) are recorded as `LiveOnly` in
  `internal/diff/chartdiff.go:283-288` and printed by `internal/cli/review_sections.go:80-90` with
  "Their values are lost after migration". Nothing is written to the patch.
- `docs/TASK-keos-apps-env-list-overlays.md` is **not** a mechanism for legacy variables: it removes
  redundant `env:` lists from keos-apps size overlays so chart defaults are not dropped. It also says the
  plugin must not copy live-only entries blindly, because a flattened fieldRef or a Secret value cannot be
  copied faithfully.
- The only inline-env route in the bjw-s based chart is `controllers.<c>.containers.<n>.env`. For the
  monitor the chart has no default `env`, so a patch could add `SERVER_MAXHTTPHEADERSIZE` without shadowing
  anything. For the server, `env` holds six `VR_*` entries; adding `EXTRA_JARS` that way means copying the
  whole list.
- `internal/envvars/envvars.go:156-166` resolves only fieldRef `metadata.namespace` and `metadata.name`;
  any other fieldPath becomes the placeholder `<path>`. `internal/diff/chartdiff.go` (~line 280) then
  reports it as `UnmappedLiveUnknown`. That is the `VR_*` false positive.

## Work items

### Charts (`Stratio/charts`, virtualizer chart) — depends on the decisions below
1. Add HDFS and PostgreSQL decoupled-connector options to `virtualizer_hdfs_env_vars.yaml` (and the
   postgres equivalent), using the `common` helpers, defaulting to off, so an environment that opted in
   can keep `EXTRA_JARS`.
2. Add a monitor extra-env value (e.g. `virtualizerMonitor.general.custom` or an `env` map) that defaults to
   `SERVER_MAXHTTPHEADERSIZE=65536`, matching the legacy default for every client. Verify the property name
   works under Spring Boot 3 / Tomcat 10 before choosing the default.
3. Fix the note in `docs/TASK-charts-universe-alignment.md:315-318`.

### Plugin (`flux-stratio`)
1. **Resolve annotation fieldRefs** (`metadata.annotations['k']`) from the workload's pod-template
   annotations (and `metadata.labels['k']`) in `internal/envvars/envvars.go`, so `VR_DEPLOYMENT` /
   `VR_SERVICE` stop being reported. Unit-test with the real backup (`deployment.yaml` lines 1457-1461).
2. **Make the "lost" review actionable.** For each live-only variable show where it came from when the
   catalog or descriptor can say (here: the opt-in flags `VIRTUALIZER_DECOUPLED_CONNECTOR_*`), and do not
   list variables whose legacy value is empty (`,,,,`, `,`) as losses; print them as a `[NOTE]`.
3. **Investigate, then maybe implement** an explicit, confirmed carry of a live-only non-secret,
   non-fieldRef variable through the inline `env` path (like `tenantimport` does), gated by the guidance in
   `TASK-keos-apps-env-list-overlays.md`. Only if the investigation shows it is safe for multi-container
   charts where `env` already has defaults.
4. Decide (with TASK-01) whether `prepare-virtualizer` should also remove or report the legacy ingresses.

### Tenant file (interim, eosdev only, NOT applied)
Anyone who needs the monitor header limit before the chart change lands can add, in the `virtualizer`
patch under `controllers.monitor.containers.virtualizer-monitor`, an `env` map with
`SERVER_MAXHTTPHEADERSIZE: "65536"`. Do not add `EXTRA_JARS` that way without first copying the server's
six `VR_*` entries.

## Acceptance criteria

- Section "Audit facts and verdicts" ends with a recorded final verdict per row, with the evidence, and the
  "Other differences" list is classified. Items needing the user are listed at the end.
- Plugin tests: annotation fieldRef resolved from the real backup yields no `VR_*` difference; a variable
  whose legacy value is only commas/empty is a note, not a loss.
- If chart items 1-2 are accepted: `helm template` of the new chart with the eosdev values reproduces the
  legacy `EXTRA_JARS` (when the flags are on) and `SERVER_MAXHTTPHEADERSIZE`; after a republish and rollout,
  `apps diff virtualizer --baseline <backup>` no longer lists them.
- `CONTEXT.md`, `docs/config-reference.md` (if the catalog changes), `CHANGELOG.md` updated;
  `go test ./...` passes.

## How to reproduce

```
flux stratio apps diff virtualizer --baseline ~/.fluxcd/flux-stratio/backups/virtualizer/2026-10-09T10-51-40Z -v
flux stratio apps diff virtualizer --drift    ~/.fluxcd/flux-stratio/backups/virtualizer/2026-10-09T10-51-40Z
kubectl -n stratio-apps get deploy virtualizer virtualizer-monitor virtualizer-ui
kubectl -n stratio-apps get ingress | grep -i virtualizer
```

## Open questions for the user

- Does eosdev actually need the decoupled HDFS and PostgreSQL SSCC connectors (queries through them), or
  are the bundled ones enough? (Decides preserve vs accept for `EXTRA_JARS`.)
- Agree that the monitor header limit should become a chart default for every client?

## Related

- `TASK-01` (prepare steps and what they leave or delete), `TASK-03` (same kind of per-variable review),
  `TASK-keos-apps-env-list-overlays.md`.
- Audit artifact with all raw outputs: https://claude.ai/artifact/7TbG1ew9A6CGCMUDShFDx4
