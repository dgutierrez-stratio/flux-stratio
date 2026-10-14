# TASK-11 — chart variables vs the Command Center descriptor: why do legacy variables go missing or change, if the chart comes from the same descriptor?

**Status: investigated 2026-10-14 (results in "Investigation results" and "Fixes applied" at the end). F1, F2 and F4 applied (uncommitted); F3 deliberately not done by the user. Decisions D1-D6 moved to `TASK-12-chart-variable-decisions.md`. No cluster change.**
Raised by the user during the eosdev migration of 2026-10-13 (see `TASK-00-eosdev-audit-index.md`, "Migration principle").
Tools that may change: `Stratio/charts` (worktree `/stratio/aws/flux/.worktrees/charts-align-15.1.0`), `keos-apps`
(worktree `/stratio/aws/flux/.worktrees/keos-apps-env-list-overlays`), `flux-stratio` (this repo, branch `feat/application-migration`).

## Goal

The user's question, almost verbatim:

> Our chart architecture defines variables that are mapped to the YAML structure in `values.yaml`. That structure comes from the original Command Center (CCT) descriptor. If those variables are lost, why? Is it not the same descriptor for our current chart implementation?

Answer it with evidence, **per variable**, and turn the answers into fixes where fixes are warranted. "Same descriptor" has to be tested, not assumed: which descriptor release did the legacy install come from, which one was each chart generated from, and which of the legacy values ever travelled through the descriptor at all.

## Requirement for this task

Read "Migration principle" in `TASK-00`. For every variable below, investigate whether the difference makes sense in the new GitOps-with-charts scenario **and why**, record the conclusion with evidence in this file before changing any tool, and classify it **preserve legacy**, **accept new** or **needs a product decision**. A long list of differing variables is a list to investigate, not a list of bugs. Assume neither direction.

## What is already known (verify what you rely on)

1. **The genai-ui "119 lost variables" was a plugin false positive, not a chart gap.** On 2026-10-13 `apps migrate genai --dry-run` reported 119 genai-ui variables as "not rendered by the chart". A null value in the rendered ConfigMap (`TRANSLATE_LITELLM_MODEL`, from an unquoted empty `${LLM_MODEL_*}` substitution in keos-apps) makes `unstructured.NestedStringMap` fail (`internal/diff/envvars.go:45`), the error is discarded and the whole ConfigMap reads as empty. That bug is **TASK-10**; do not re-investigate it here. With nulls read as `""` the comparison is: 101 same, **18 different**, **0 lost**; every one of the 119 keys has a line in the chart's `genai/config/genai_ui_env_vars.yaml`. Full table: `/tmp/claude-1001/-stratio-gitops-flux-stratio/8951e99e-48a0-404d-9a81-f913f1865961/scratchpad/genai/genai-ui-vars.md`. Consequence for this task: for genai the question is **why the 18 differ**, not why they are missing.
2. **Other apps did really lose variables or could not patch them.** These are the stronger cases for the user's question (inventory below). Existing task files cover the migration side: `TASK-03` (dg-hdfs-agent), `TASK-04` and `TASK-virtualizer-decoupled-connector-jars.md` (virtualizer), `TASK-02` (agent probe variables).
3. **The chart is a subset of the descriptor by construction.** `bin/migration/README.md` in the charts repo: the `charts` CLI "only covers the descriptor's `parameters` block"; `container`, `healthChecks`, `networking`, `secrets`, `permissions`, `dependencies`, `resources` and `lifecycles` need separate skills/workflows (Workflow D). The repo tracks **no descriptor version metadata**: every `Chart.yaml` is `version: 0.0.0`, `appVersion: "0.0.0"`, there is no annotation, and the `upgrade` skill says so explicitly ("do not invent an automatic detection mechanism").
4. **Charts have been caught lagging their descriptor before.** Commits `c1e9f8c` (genai-ui: six 15.1.0 parameters missing), `614ddaa` (dg-agent: seven "live-only on eosdev ... would be lost on migration"), `7a151b5` (virtualizer `CONF_KV_spark_sql_warehouse_dir`), `04d763e` (rocket, 20 variables) were all added **after** the original 15.1 integrations (`6ba0075` genai, `bb8ab28` virtualizer, ...). The 2026-10-02 alignment task is `docs/TASK-charts-universe-alignment.md` (PR #258); read its "Universe reference" and "Outcome" sections first. It also recorded that virtualizer `EXTRA_JARS*` were left out on purpose ("lifecycle upserts, not parameters").
5. **Three of the lost variables are not descriptor `parameters` at all** (verified for this file, see "Worked mapping examples"): `EXTRA_JARS*` is a computed `model/parameter/upsert` task in `lifecycles.install`; `SERVER_MAXHTTPHEADERSIZE` is in no descriptor (it comes from the legacy deployment repo's `.json.tmpl`); `COMM_SERVICE_NAME` is a descriptor parameter whose value was an installation input. This is the working hypothesis for most of the answer, **not yet a conclusion**.

## Inventory of candidate variables

Source of every row: `scratchpad/dryrun-all.txt` (the `--dry-run` of 2026-10-13), `scratchpad/findings.md` (run ledger), `scratchpad/genai/genai-ui-vars.md`, and the task files named. "Chart path" is the `.Values` path in the worktree chart, or `none`. Legacy values are shown only when not secret-looking.
Status: **different** = chart renders it, value differs; **lost** = not rendered, dropped by migration; **unpatchable** = differs and the plugin cannot write it to the patch (no `.Values` path or follows release name).

Counts: genai-ui 18 different (0 lost); virtualizer 4 lost (+2 false positives); dg-hdfs-agent 11 unpatchable (+1 chart default already fixed); rocket 13 lost (held, not migrated); connectors 16 lost (different app shape); psql-gosec-agent 8 and opensearch1-gosec-agent 8 lost (same probe variables); intelligence 1 lost; genai-api 1 lost (`NOT_USED`); dg-datarest-pgi 1 false positive. **Total about 80 rows; ~40 distinct variable names once the repeated probe variables are folded.**

### genai-ui (18 different; held `enabled: false` until TASK-10 is fixed)

| Var | Legacy | Chart path | Status | First reading (hypothesis, verify) |
|---|---|---|---|---|
| OPENSEARCH_ENABLED | `true` | `genaiUi.settings.chainChat.opensearchEnabled` = `include "common.dependencies.opensearch.enabled"` | different (`false`) | Descriptor default `false`, `application:false` (a form input). Legacy install set it via `GENAI_OPENSEARCH_ENABLED` (apps-layer-automation `genai-ui/vars.env:31`, default `false`). The chart turned the variable into a **tenant dependency toggle** (`dependencies.opensearch.enabled`, keos-apps `helm.yaml:50-53` `${OPENSEARCH_ENABLED}`). Installation input, not a descriptor default. See worked example 1. |
| OPENSEARCH_HOST, OPENSEARCH_PORT | `opensearch1.stratio-datastores`, `9200` | `...chainChat.opensearchHost/Port` = `common.dependencies.opensearch.host/port` | different (`""`) | Follows the dependency toggle above: the helper returns empty when the dependency is disabled. |
| DISCOVERY_ENABLED, DISCOVERY_URL, DISCOVERY_INTERNAL_URL | `true`, `https://discovery.stratio.k8s.eosdev.../discovery`, `discovery.stratio-apps` | `genaiUi.settings.externalDashboards.discovery*` = `common.dependencies.discovery.*` | different (`false`, `""`, `""`) | Same pattern: dependency toggle replaced a form variable. The migration patch already carries the legacy values. |
| DISCOVERY_DATABASE | `3` | `...externalDashboards.discoveryDatabase` | different (`""`) | Catalog `exclude` with no recorded reason (ledger). Find out what it is and why `3`. |
| GOVERNANCE_OPENSEARCH_ENABLED | `true` | `...generalProperties.governanceOpenSearchEnabled` | different (`false`) | Chart default is `true` (`values.yaml:1002`); keos-apps overrides to `${GOVERNANCE_OPENSEARCH_ENABLED:=false}` (`helm.yaml:76`). Overlay value. |
| GOVERNANCE_OPENSEARCH_SERVICE, GOVERNANCE_OPENSEARCH_URL | `opensearch1.keos-core`, `https://opensearch1.keos-core:9200` | `...governanceOpenSearchHost/Url` | different (`opensearcher-coordinator.keos-core`) | keos-apps `helm.yaml:77-78` builds it from `${GOVERNANCE_OPENSEARCH_OSCLUSTER_NAME}`: GitOps governance uses a different OpenSearch cluster than the legacy `opensearch1`. Check whether that cluster exists on eosdev and whether it is the right one. |
| HA_PROXY_PATH | `/genai-ui` | `genaiUi.general.networking.haProxyPath` | different (`/genai-ui.stratio-genai`) | Descriptor default is `/${Application.name}` (`genai-ui-default.json:198`); chart sets `/{{ .Release.Name }}-ui.{{ .Release.Namespace }}` (`values.yaml:1022`): chart ingress design. |
| VIRTUAL_HOST | `genai.stratio.k8s.eosdev.labs.stratio.com` | `...networking.haProxyHost` = `global.cluster.url` | different (`admin.k8s.eosdev...`) | Same: GitOps serves apps on the `admin` host with a path. Decision whether eosdev keeps the legacy host. |
| SERVICE_URL | `https://genai.stratio.k8s.eosdev.../genai-ui` | `...networking.serviceUrl` | different (`https://admin.k8s.eosdev...//genai-ui.stratio-genai`) | Same. Note the double slash in the chart default (`values.yaml:1023`). |
| MY_ACCOUNT_URL | `https://admin.k8s.eosdev.../gosec/ui//#/my-account` | `genaiUi.environment.gosec.myProfileUrl` | different (single slash, trailing `/`) | Cosmetic; check the descriptor default. |
| GOVERNANCEAPI_FULL_NAME, GOVERNANCEAPI_URI, GOVERNANCE_URL | `dg-businessglossary-api...` | `genaiUi.general.governanceRegistration.*`, `generalProperties.governanceUrl` | different (`governance-...`) | Deliberate `dg-` to `governance-` rename (memory `governance-rename-is-ours`). Expected: accept new. |
| VAULT_ROLE | `stratio-genai-genai-ui` | `genaiUi.general.identity.approlename` | different (`stratio-genai_genai-ui`) | Deliberate (memory `vault-role-naming`). Expected: accept new. |

### virtualizer (4 lost, 2 false positives; migrated 2026-10-13, TASK-04)

| Var | Legacy | Chart path | Status | Source |
|---|---|---|---|---|
| EXTRA_JARS (server) | `,,,,<sscc-hdfs url>,<sscc-postgresql url>,` | none at migration time; commit `feb37d7` (unpublished, in the worktree) adds `virtualizer.server.extraJars` | lost | `TASK-virtualizer-decoupled-connector-jars.md`; descriptor `lifecycles.install/start/26`. See worked example 2. |
| EXTRA_JARS_SPARK_CLASSPATH, EXTRA_JARS_SPARK_EXECUTOR_CLASSPATH | `,,,,` and `,` | none | lost (no-op, all parts empty) | TASK-04; same lifecycle upsert family. |
| SERVER_MAXHTTPHEADERSIZE (monitor) | `65536` | none | lost | TASK-04. In **no descriptor**: it comes from `apps-layer-automation/apps/virtualizer-monitor/virtualizer-monitor.json.tmpl:24-28` (`general.custom`). |
| VR_DEPLOYMENT, VR_SERVICE | fieldRef annotation, value `virtualizer` | literal | false positive | TASK-04 / TASK-00 #07. Not a data question; skip here. |

### dg-hdfs-agent (chart `dg-agent`, `agent.type=hdfs`; 11 unpatchable, TASK-03)

| Var | Legacy | Chart path | Status | First reading |
|---|---|---|---|---|
| COMM_SERVICE_NAME | `hdfs1.stratio-datastores` | **none** (`dg-agent_common_env_vars.yaml:38` hard-codes `{{ .Release.Name }}`) | unpatchable | Descriptor parameter `customServiceName` (`id: COMM_SERVICE_NAME`, no default, example `dg-hdfs-internal-agent`, in `packages/connectors/agent-hdfs-internal-default.json:68`). Legacy value was a CCT input (`${SERVICE_NAME}` in `connector-dfs-hdfs.json.tmpl:138`). Chart never exposed the parameter (hard-coded since `29a4347`). Highest priority: identity of the datastore in Governance. |
| CUSTOM_SERVICE_DS_DRIVER_LOCATION | `http://connectors-management.stratio-apps/v1/api/artifact/Connector_sscc-hdfs_2.12_1.1.1-922b277.jar` | built from `dependencies.connectors.*` | unpatchable | New `connectors` service replaces `connectors-management`; probably accept new. |
| COMM_DS_URL, COMM_DS_URL_LL, DATASTORE_HOST | host without port | built from `common.dependencies.postgres.*` | unpatchable | Explicit `:5432`; probably neutral. |
| DISCOVERY_BASE_URI, DISCOVERY_MANAGEMENT_BASE_URI, GOVERNANCE_BASE_URI, GOVERNANCE_DEPLOYMENT, GOVERNANCE_MANAGEMENT_BASE_URI | `dg-...` | fixed in the chart | unpatchable | Deliberate rename. |
| VAULT_ROLE | `stratio-datastores-dg-hdfs-agent` | follows release name/namespace | unpatchable | Deliberate. |
| CUSTOM_STRATIO_CREDENTIALS | blank (backup) | chart default | different | Fixed in charts `0a1e649`, not yet in a published chart. Chart skew case. |

### Apps not migrated in the run, or with no patch (from the dry runs)

| App | Variables | Notes |
|---|---|---|
| rocket (13, held) | `CUSTOMER_SIZE` (M), `MIGRATION_STORAGE_PROFILE` (medium), `SSCC_S3A_GLOBAL_CREDENTIALS` (s3), `NOT_USED` (empty), and the nine `{LIVENESS,READINESS,STARTUP}_PROBE_*` | The alignment task called `CUSTOMER_SIZE`, `MIGRATION_STORAGE_PROFILE`, `*_PROBE_*`, `NOT_USED` "legacy CCT artefacts that should stay unrendered". Verify that claim per variable; `SSCC_S3A_GLOBAL_CREDENTIALS` is the odd one. |
| connectors (16) | `API_SSL_ENABLED`, `APP_API_PORT`, `FILESYSTEM_ROOTPATH`, `SQLITE_*`, `JWT_SECRET_EXPIRATION`, `VAULT_*`, `SERVICE_ID`, `INSTANCE_NAME`, `EXPOSE_INGRESS`, `VIRTUAL_HOST`, `CUSTOM_STRATIO_CREDENTIALS` | Not "lost" in the chart sense: the GitOps `connectors` chart is a busybox httpd + loader, a different workload than legacy `connectors-management`. Out of the descriptor question; confirm and say so. |
| psql-gosec-agent, opensearch1-gosec-agent (8 each) | `LIVENESS/READINESS_PROBE_{FAILURE_THRESHOLD,INITIAL_DELAY_SECONDS,PERIOD_SECONDS,TIMEOUT_SECONDS}` | TASK-02: probe values were CCT `healthChecks` injected as env vars. |
| intelligence (1), genai-api (1) | `NOT_USED` | Legacy artefact. |
| dlc-entity, discovery, litellm, datamarket-agent | not listed as "lost" in the dry runs | Out of scope unless the investigation finds otherwise. |

## Where things live

**Charts**
- `/stratio/aws/flux/.worktrees/charts-align-15.1.0`: worktree on branch `dgutierrez-stratio/align-15.1.0`, head `feb37d7`, 20 commits ahead of `master`. **This is the checkout the plugin renders from** (`~/.fluxcd/flux-stratio/environment.yaml`, `repos.charts`). Contains PR #258's content plus unpublished commits `0a1e649` (dg-agent credentials), `feb37d7` (virtualizer extraJars) and the litellm merge.
- `/stratio/charts/charts`: main checkout, branch `master` at `a9acf1c` (does **not** contain PR #258). `/stratio/l/charts`: another clone on `task/plt-4387`. `/stratio/aws/flux/.worktrees/charts-plt-4838`: worktree on `dgutierrez-stratio/PLT-4838-litellm-name`. Do not read charts from these for this task.
- Chart layout: `<chart>/values.yaml` plus `<chart>/config/<flavor>_env_vars.yaml` (one `KEY: {{ .Values.<path> }}` per line, "generated from the CCT descriptors" per the charts `README.md`). Shared helpers live in `common/templates/dependencies/*.tpl`.

**What eosdev runs (corrected 2026-10-14: deployed snapshot == worktree templates, see "Investigation results").** All migrated HelmReleases use chart `15.1.0-PR258-SNAPSHOT` from HelmRepository `helm-universe` (`http://qa.int.stratio.com/repository/helm`), `reconcileStrategy: ChartVersion`: virtualizer, dg-hdfs-agent (`dg-agent`), litellm, connectors, discovery, eureka-agent, dg-datarest-pgi (`bdl-datarest`), both gosec agents. genai, rocket, intelligence are not installed yet. So the deployed snapshot is **older than the worktree** the plugin renders (virtualizer was byte-identical before `feb37d7`; dg-agent differed by the credentials default). `doctor` does not compare chart versions (TASK-03 plugin item 1).

**Descriptors**
- `/stratio/p/kubernetes-universe`: local clone of `Stratio/kubernetes-universe`, now at `branch-15.1` tip `f14a1355`, whose descriptors read `15.1.1-SNAPSHOT` (verified: `genai-ui-default.json`, `virtualizer-default.json`, `agent-hdfs-internal-default.json`). Release tags such as `15.1.0-3c39ac4`, `15.1.0-49ff205` exist. **Do not compare against the tip.** The reference decided on 2026-10-02 is the 15.1.0 release commit `54ab1918` (descriptors read `15.1.0-BUILD`; parent `7324e952` reads `15.1.0-SNAPSHOT`), see `docs/TASK-charts-universe-alignment.md`.
- Descriptor paths: `packages/genai/genai-ui-default.json` (service `genai`, model `genai-ui`), `packages/virtualizer/virtualizer-{default,ui,monitor}.json`, `packages/connectors/agent-hdfs-internal-default.json` (service `connectors-dfs`, model `agent-dfs-hdfs-internal`). Inheritance via `parentConfiguration` up to `stratiobase:default`.
- **Legacy deployment repo** `/stratio/aws/apps-layer-automation` (`master` at `1d19c64`): per-app `vars.env`, `<app>.json.tmpl` (installation overrides applied on top of the descriptor), `sizes/*`, `pre-install.sh`. This is where "Command Center form inputs" and legacy-only defaults live (`GENAI_OPENSEARCH_ENABLED`, `SERVICE_NAME`, `SERVER_MAXHTTPHEADERSIZE`, `VIRTUALIZER_DECOUPLED_CONNECTOR_*`). The `scaffold` skill's Step 10 cross-checks it.
- eosdev's own CCT: `keos-cct/cct-universe` runs image `cct-universe:3.0.10`. What descriptor release it serves is **not** established.

**Legacy descriptor versions (collected from the pre-migration backups `~/.fluxcd/flux-stratio/backups/<app>/2026-10-13T11-00-17Z/`)**

| App | `application_service` : `application_model` | `application_version` | `app.stratio.com/universe` |
|---|---|---|---|
| genai-api / genai-ui | `genai`:`genai-api` / `genai`:`genai-ui` | `15.1.0` | `"15.1"` |
| virtualizer, virtualizer-ui, virtualizer-monitor | `virtualizer`:`default` / `virtualizer-ui` / `virtualizer-monitor` | `15.1.0` | `"15.1"` |
| dg-hdfs-agent | `connectors-dfs`:`agent-dfs-hdfs-internal` | `15.1.0` | `"15.1"` |
| rocket, intelligence | `rocket`:`default`, `intelligence`:`default` | `15.1.0` | `"15.1"` |

Each Deployment also carries `cct.stratio.com/application_generation` (opaque id). So **the label-level version matches the chart target (15.1.0)**; no label proves the exact build (`15.1.0-<sha>`, `-BUILD`, `-SNAPSHOT`) the legacy install was rendered from. **No evidence of descriptor skew at release level was found; skew at build level and chart-vs-snapshot skew (deployed `PR258-SNAPSHOT` vs worktree) are open.** Supporting data points: the charts' image tags equal live after PR #258 ("rendered images equal live", alignment task Outcome), and the live genai images are `genai-api:0.8.0`, UI `0.9.0`.

## Worked mapping examples

The chain is: descriptor field → (charts CLI) `config/<flavor>_env_vars.yaml` line → `values.yaml` path → keos-apps overlay / tenant file → rendered ConfigMap or Deployment env.

### 1. genai-ui `OPENSEARCH_ENABLED` (a "different" variable)

1. Descriptor `packages/genai/genai-ui-default.json:944-957`: parameter `opensearchEnabled`, `internalName: OPENSEARCH_ENABLED`, `default: false`, `application: false`, `required: true`, boolean "Enable OpenSearch (for parametric filters)". Sibling `opensearchHost` default `${opensearch.host.default}.${opensearch.namespace}`, `opensearchPort` default `${opensearch.port}`.
2. Legacy installation: apps-layer-automation `apps/genai-ui/vars.env:31` `GENAI_OPENSEARCH_ENABLED=${GENAI_OPENSEARCH_ENABLED:-false}` fed into `genai-ui.json.tmpl:10` (`"opensearchEnabled": ${GENAI_OPENSEARCH_ENABLED}`). eosdev ran with `true`.
3. Chart env line: `genai/config/genai_ui_env_vars.yaml:83` `OPENSEARCH_ENABLED: {{ .Values.genaiUi.settings.chainChat.opensearchEnabled | quote }}`.
4. Chart values: `genai/values.yaml:1054` `opensearchEnabled: '{{ include "common.dependencies.opensearch.enabled" . }}'`. The helper (`common/templates/dependencies/_opensearch.tpl:27`) returns `.Values.dependencies.opensearch.enabled` unless the chart declares it required.
5. keos-apps `components/genai/app/base/helm.yaml:50-53` sets `dependencies.opensearch.enabled: ${OPENSEARCH_ENABLED}`; the tenant file's genai entry does not enable the opensearch dependency, so it renders `false`; `OPENSEARCH_HOST/PORT` become `""` because the helpers return empty when disabled.
6. Rendered value `false` vs legacy `true`. **The descriptor default (`false`) and the chart agree; the legacy value was the CCT installation input.** The migration patch carries `chainChat.opensearchEnabled` back (ledger), which is the intended mechanism. Open question: should the tenant file say `dependencies.opensearch.enabled: true` instead, so the dependency graph (`dependsOn`, GosecPolicy `genai-api-governance-opensearch`, keos-apps `gosec.yaml:7 enable: ${OPENSEARCH_ENABLED}`) matches reality?

### 2. virtualizer `EXTRA_JARS` (a "lost" variable)

1. Descriptor `packages/virtualizer/virtualizer-default.json`, `lifecycles.install.start[26]` (also `update.start[16]`, `upgrade.start[16]`): task `type: model/parameter/upsert`, `parameterName: extraJars`, `internalName: EXTRA_JARS`, `value: ${environment.artifacts.extensionsList},${connectors.connectorS3.envS3StorageArtifactURL},${connectors.connectorAzureGen2...},${connectors.connectorGCS...},${connectors.connectorJdbc.connectorJdbcDrivers}`. **It is not in `parameters`.**
2. `charts import/analyze` is driven by `parameters`, so it never saw it. The `lifecycles` skill covers "new/computed environment parameters", but the alignment run did not apply it to virtualizer (alignment Outcome, item 4).
3. Legacy value: `connectorJdbcDrivers` was filled by `apps-layer-automation/apps/virtualizer/pre-install.sh:32-66` only when `VIRTUALIZER_DECOUPLED_CONNECTOR_{POSTGRES,HDFS}=true` (`vars.env:24-25`, default `false`; eosdev opted in).
4. Chart at migration time: `virtualizer/config/storage/virtualizer_hdfs_env_vars.yaml` had no `EXTRA_JARS`; only the s3/adls/gcs flavors did. Fix prepared in worktree commit `feb37d7` (adds `dependencies.connectors.config.{hdfs,postgresql}.connectorJar`, `virtualizerServer.connectors.connectorJdbc.connectorJdbcDrivers`, helper `virtualizer.server.extraJars`), not published, not deployed.
5. Rendered value: unset. Root-cause class: **a descriptor concept the charts CLI does not cover (lifecycle upsert) plus a legacy opt-in input**.

### 3. dg-hdfs-agent `COMM_SERVICE_NAME` (an "unpatchable" variable)

1. Descriptor `packages/connectors/agent-hdfs-internal-default.json:68` declares the parameter `customServiceName` with `id: COMM_SERVICE_NAME` (no default; example `dg-hdfs-internal-agent`).
2. Legacy installation: `apps-layer-automation/apps/connector-dfs-hdfs/connector-dfs-hdfs.json.tmpl:138` `"customServiceName": "${SERVICE_NAME}"`; `vars.env:20` default `dg-hdfs-internal-agent`; eosdev's value was `hdfs1.stratio-datastores`.
3. Chart: `dg-agent/config/dg-agent_common_env_vars.yaml:38` `COMM_SERVICE_NAME: {{ .Release.Name }}`, no `.Values` path, since the chart's first commit `29a4347`. So the parameter **exists in the descriptor but the chart never mapped it**; the plugin cannot patch a hard-wired value.
4. Rendered value: `dg-hdfs-agent` (release name). Governance discovery logs show requests under `datastoreName=dg-hdfs-agent` while the GoSec policy and HDFS service are still `hdfs1.stratio-datastores` (TASK-03).

Pick one more from the inventory when classifying (suggested: `SERVER_MAXHTTPHEADERSIZE`, which is in no descriptor, and one `*_PROBE_*` variable, which should trace to `healthChecks`).

## Hypotheses to test

H1. **Descriptor version skew.** Legacy was rendered from a different 15.1.0 build than the one the chart was generated from, or the chart is older than `54ab1918`. Evidence so far: labels all say 15.1.0 / universe 15.1; images equal live. Not excluded: build-level differences, and the deployed chart (`PR258-SNAPSHOT`) being older than the worktree.
H2. **The chart was generated from a descriptor subset.** The `charts` CLI reads only `parameters`; lifecycle upserts (`EXTRA_JARS*`), `healthChecks` (probe variables), engine-injected values (`CUSTOMER_SIZE`, `MIGRATION_STORAGE_PROFILE`) never enter `values.yaml`. Evidence: examples 2 and the rocket/agent rows.
H3. **`values.yaml` defaults diverge from the descriptor defaults.** The chart author replaced a variable by a different mechanism (a dependency toggle, a release-name convention, an ingress path scheme). Evidence: examples 1 and the genai networking rows.
H4. **Overlay or tenant values.** keos-apps `helm.yaml` and the size overlays override chart defaults (`governanceOpenSearch*`, `${...:=false}`); the tenant file does not enable what the legacy install had enabled. Evidence: genai `GOVERNANCE_OPENSEARCH_*`.
H5. **Variables that were never in the descriptor.** Legacy-deployment-repo defaults (`SERVER_MAXHTTPHEADERSIZE`, `SERVICE_NAME`, `GENAI_OPENSEARCH_ENABLED`) and CCT-engine injection. There the question "is it not the same descriptor?" has the answer "the legacy install was descriptor **plus** `apps-layer-automation` **plus** form inputs; the chart is descriptor only".
H6. **Deliberate changes of ours.** `dg-` to `governance-`, Vault role underscore, new `connectors` service: accept new, record and close.

## Investigation steps for the next session

1. **Confirm the descriptor reference.** Use the 15.1.0 release commit `54ab1918` of `/stratio/p/kubernetes-universe` (fall back to `7324e952`, `-v 15.1.0-SNAPSHOT`), never the branch tip. Try to establish which universe build eosdev's CCT served: inspect the `cct-universe` deployment in `keos-cct` and the legacy `application_generation`, or ask the user. Record the answer even if it is "unknown".
2. **Classify each row of the inventory by channel.** For each variable, find (a) whether the descriptor has it in `parameters`, `lifecycles.*`, `data`, or nowhere (use the charts CLI below, or grep `internalName`/`id` in the JSON), (b) whether `apps-layer-automation` sets it (`vars.env`, `*.json.tmpl`, `sizes`, `pre-install.sh`), (c) the chart path and what feeds it (values default, keos-apps overlay, tenant file). The channel decides the root cause (H1-H6).
3. **Use the charts tooling (read-only runs).**
   - Plugin `charts@stratio-platform` (source `/stratio/IA/platform-claude-plugins/plugins/charts`, installed under `~/.claude/plugins/cache/stratio-platform/charts/1.0.0/skills/`): `/charts:compare-descriptors` is the read-only, chart-independent report of what changed in one `service:model` between two releases (parameters, merged `data`, merged `lifecycles`, per inheritance level); `/charts:upgrade` aligns a chart with a newer release and applies only the diff; `/charts:data` and `/charts:lifecycles` translate the non-`parameters` sections (the missing-coverage areas of H2); `/charts:scaffold` and `/charts:import` create charts. Run them from the charts worktree, **on a branch, one chart per run**, and answer the removal gate deliberately.
   - Plain CLI: `python3 bin/migration/migrate.py charts analyze --zip-file <zip> -s <service> -m <model> -v <version> [--values values.yaml --prefix <prefix>]` reports MATCHED / available to add / removed; add `--env-vars /dev/null --scaffold` for the full resolved catalog, `--data` and `--lifecycles` for the merged sections. The ZIP can be built by the plugin's scaffold procedure from a `git worktree` of kubernetes-universe, so no manual ZIP is needed. Version to pass is the `data.version` inside the JSON. Use `bin/check_descriptors.sh <chart-dir>` to render; it mis-reports charts with Vault agent annotations (documented).
   - Descriptor identities to pass: `genai genai-ui`, `virtualizer default|virtualizer-ui|virtualizer-monitor`, `connectors-dfs agent-dfs-hdfs-internal`, `rocket default`, `intelligence default`.
4. **Check skew explicitly.** Render the deployed chart (`helm pull dg-agent --version 15.1.0-PR258-SNAPSHOT --repo http://qa.int.stratio.com/repository/helm`, untrusted download: its own new empty directory) and the worktree chart with the HelmRelease values and compare for each migrated app; the charts' own CHANGELOG (`15.1.0 (upcoming)`) lists what landed after the first integration. Record per app: identical / differs and how.
5. **Settle the genai 18** (needs TASK-10's fix or the scratch build mentioned in the table file; do not fix TASK-10 here). For each, decide preserve-legacy / accept-new / needs-decision using H3-H4. In particular decide whether the tenant file should enable the `opensearch` and `discovery` dependencies (which would also change `dependsOn`, GoSec policies and the discovery postrequisites; see the ledger notes on discovery and the "Virtualizer" Metabase connection) instead of relying on patch values.
6. **Answer the rocket and probe questions from the descriptor's `healthChecks`/`data`**, not from the claim in the alignment task.
7. **Only then design fixes** (next section). Do not change a chart before its variable has a recorded classification.

## Expected deliverable

In this file, replace the inventory's "first reading" column by a recorded conclusion per variable:

| Var | Root cause (H1-H6, with evidence) | Classification (preserve-legacy / accept-new / needs-decision) | Fix and repo |
|---|---|---|---|

and a short answer to the user's question, in plain words, stating which fraction of the variables fall in each root-cause class and whether "same descriptor" holds. Then the proposed fixes per repo. Candidates, none of them decided:

- **charts:** expose descriptor parameters the chart hard-wires (`COMM_SERVICE_NAME`, default `{{ .Release.Name }}`); add the lifecycle-upsert variables the `lifecycles` skill would have produced (`EXTRA_JARS*`, merge/publish `feb37d7`); add a monitor extra-env value for `SERVER_MAXHTTPHEADERSIZE` only if the decision says preserve; record the descriptor release a chart was aligned to (a `Chart.yaml` annotation or a `docs` table) so skew is detectable; republish a snapshot so eosdev stops running a chart older than the worktree. Chart output of the `charts` tools outranks manual edits.
- **keos-apps:** decide genai `GOVERNANCE_OPENSEARCH_*` defaults, dependency toggles for opensearch/discovery in the tenant defaults, the `${LLM_MODEL_*}` quoting (TASK-10 hardening), size overlays.
- **flux-stratio:** make `apps diff/migrate` say **why** a variable is lost, by channel (descriptor parameter not mapped / lifecycle upsert / not in descriptor / dependency-derived) when the catalog knows; consider a `doctor` check of chart version vs deployed (TASK-03 plugin item 1) and a catalog field for accepted differences with a `why`; keep the legacy-preserving patch path as it is.

## Acceptance criteria

- Every inventory row has a recorded root cause with evidence (file and line, or command and output) and a classification; no row is left at "first reading".
- The user's question is answered explicitly: which descriptor release the legacy install and each chart correspond to (or "unknown", with what was tried), and which share of variables fall in each channel.
- Each proposed fix names its repo, the file, and a verification (for charts: `charts analyze`/`validate` and `helm template` before/after, plus `flux stratio apps diff <app> --baseline <backup>` on the affected app; for the plugin: tests under `internal/diff`/`internal/cli` with fixtures).
- Nothing is changed for a variable classified preserve-legacy or accept-new that has no defect; decisions that need the user are listed at the end of this file.
- If code is changed in this repo: `CONTEXT.md`, `docs/migration-runbook.md`, `CHANGELOG.md` updated, `go test ./...` passes.

## Constraints for the session

- **The `charts` tool output outranks manual edits** in charts alignment work (memory `charts-tool-output-wins`): variables, schema paths and defaults come from `charts analyze/values/environment/validate`; hand edits only where they coincide with the tool, or where the tool cannot express it (then say so).
- **Migration patches must keep legacy live config**, even over GitOps overlay values (memory `migration-preserves-legacy`). Do not "fix" a difference by letting the patch drop a legacy value.
- Deliberate renames are not findings: `dg-` to `governance-`, Vault role hyphen to underscore (memories `governance-rename-is-ours`, `vault-role-naming`).
- A long list of differing variables is a list to investigate, not a list of bugs; do not turn rows into tasks before classifying them. Findings that need work become `TASK-NN` files, one at a time.
- Read-only on the cluster and all repos until a fix is agreed; charts changes go on a branch in the charts worktree; no commit or push without the user asking. Never print secret values (CCT/Vault values in the backups include secrets).
- Per user preference, delegate bulky read/verify work to sonnet subagents and keep them read-only.

## How to reproduce the starting data

```
S=/tmp/claude-1001/-stratio-gitops-flux-stratio/8951e99e-48a0-404d-9a81-f913f1865961/scratchpad
cat $S/genai/genai-ui-vars.md                     # the 119 genai-ui variables, 101 same / 18 different
grep -n "aren't rendered\|can't be written" $S/dryrun-all.txt   # per-app lost / unpatchable lists
cat $S/findings.md                                # run ledger (W1-W4)
flux stratio apps diff <app> --baseline ~/.fluxcd/flux-stratio/backups/<app>/2026-10-13T11-00-17Z -v
kubectl get hr -A -o custom-columns='NS:.metadata.namespace,N:.metadata.name,CHART:.spec.chart.spec.chart,VER:.spec.chart.spec.version'
```

## Related

`TASK-00` index; `TASK-02` (probe variables), `TASK-03` (dg-hdfs-agent), `TASK-04` and `TASK-virtualizer-decoupled-connector-jars.md` (virtualizer), `TASK-05` (the mirror image: GitOps-only keys), `TASK-10` (genai ConfigMap null false positive), `docs/TASK-charts-universe-alignment.md` (PR #258, universe reference), `docs/TASK-keos-apps-env-list-overlays.md`, `docs/TASK-chart-spec-comparison.md`.

---

# Investigation results (2026-10-14, read-only)

Evidence came from `git show` on `/stratio/p/kubernetes-universe` (never the working tree), the pre-migration backups `~/.fluxcd/flux-stratio/backups/<app>/2026-10-13T11-00-17Z`, the charts worktree `charts-align-15.1.0` (`feb37d7`), keos-apps worktree `env-list-overlays`, `/stratio/aws/apps-layer-automation`, and `kubectl`/`helm pull` against eosdev (read-only). No secret value was printed.

## Answer to the user's question, in plain words

**Yes, it is the same descriptor.** What differs is what travelled with it.

1. **Same release and build.** The Deployment labels only say 15.1.0, so the build was fingerprinted from the legacy env key sets and image tags against the seven 15.1.0 builds of the descriptors (`b5e4b439`, `49ff2051`, `fb64d7f6`, `bede7905`, `3c39ac44`, `80e63ea3`, `54ab1918`) and the branch tip (`15.1.1-SNAPSHOT`):
   - genai-api, genai-ui, virtualizer (+ui, +monitor), rocket: legacy lies in `fb64d7f6`..`54ab1918` (no `GENAI_GATEWAY_URL`, which `fb64d7f6` removed; images genai-api 0.8.0, ui 0.9.0, virtualizer-ui/monitor 0.6.0, rocket 4.1.0; none of the 21 variables that only exist at the tip).
   - dg-hdfs-agent: `80e63ea3`..`54ab1918` (has `COMM_CACHE_IMPLEMENTATION`, `DFS_REMEMBER_READ_FILES`, `SSCC_DFS_METRICS_ENABLED`, `DFS_PAGED_DESCENT_FROM`, all added after `3c39ac44`; image 1.2.0).
   - intelligence: `3c39ac44`..`54ab1918` (image 5.1.1).
   - These descriptors do not change inside that window (except the dg-hdfs-agent additions above), so the legacy install cannot be pinned finer than the window; `54ab1918` is always consistent with it.
   - Every chart matches `54ab1918` (the images, the presence/absence of the fingerprint variables). No app and no chart matches the June builds or the branch tip.
   - eosdev's own `cct-universe` image is `3.0.10`; which universe build it served is not recorded anywhere I could read without the CCT database. Recorded as unknown, not needed given the fingerprints.
2. **No chart-vs-snapshot skew today (corrects "What eosdev runs").** The `15.1.0-PR258-SNAPSHOT` tarball in `helm-universe` has the same sha256 as the artifact Flux pulled on 2026-10-13T14:08:52Z (`b89ff9ae…` dg-agent, `aac13958…` virtualizer) and its templates and values are identical to the worktree, including `0a1e649` and `feb37d7`. So what Flux deployed is what the repo serves now. (virtualizer still renders no `EXTRA_JARS` on eosdev because the tenant does not set the connector jars: that is decision D6 below, not skew.)
3. **The legacy install was the descriptor plus four other channels, and the chart is only the descriptor's `parameters`:**
   `legacy = descriptor parameters + lifecycles upserts + general.custom + apps-layer-automation (vars.env, *.json.tmpl, pre-install.sh) + Command Center form inputs`.
   The `charts` CLI covers `parameters` only (`bin/migration/README.md`), and the chart author re-expressed several parameters with GitOps mechanisms (dependency toggles, release-name conventions, native probes). keos-apps then overlays defaults.

Share of the ~80 inventory rows (about 40 distinct names):

| Root cause | Rows | What |
|---|---|---|
| H5 install input / apps-layer-automation, not a descriptor default | ~7 | `OPENSEARCH_ENABLED`, `DISCOVERY_DATABASE`, `HA_PROXY_PATH`, `VIRTUAL_HOST`, the value of `COMM_SERVICE_NAME`, the `EXTRA_JARS` opt-in, `SERVER_MAXHTTPHEADERSIZE` |
| H3 chart expresses it another way | ~14 + 17 probe rows | dependency toggles, `SERVICE_URL`, `MY_ACCOUNT_URL`, `:5432`, driver URL, rocket/gosec probes as native probes, `SSCC_S3A_GLOBAL_CREDENTIALS` (storage flavor) |
| H2 descriptor concept the charts CLI does not cover | ~6 | `EXTRA_JARS*` (lifecycle upsert), `CUSTOMER_SIZE` (upgrade-only upsert), `MIGRATION_STORAGE_PROFILE`, `NOT_USED` |
| H4 keos-apps overlay / use-cases default | 3 | `GOVERNANCE_OPENSEARCH_*` |
| H6 deliberate change of ours | ~9 | `dg-`→`governance-`, `VAULT_ROLE`, `connectors-management`→`connectors` |
| H7 chart did not map an existing parameter | 2 | `COMM_SERVICE_NAME`, `CUSTOM_STRATIO_CREDENTIALS` (fixed in `0a1e649`, now in the snapshot) |
| H8 different workload | 16 | the `connectors` rows |

H1 (version skew) is excluded. The brief's claim that `CUSTOMER_SIZE` and `*_PROBE_*` are "application-level" is wrong: they are `application:false` parameters (`rocketbase.json:891-902`, `:943-1063`).

## Recorded conclusion per variable

Descriptor lines are `internalName` lines at `54ab1918`; G = `packages/genai/genai-ui-default.json`, AC = `packages/connectors/agent-abstract-connectors-default.json`, R = `packages/rocket/rocketbase.json`, V = `packages/virtualizer/virtualizer-default.json`. "aa" = apps-layer-automation.

### genai-ui

| Var | Root cause (evidence) | Classification | Fix and repo |
|---|---|---|---|
| OPENSEARCH_ENABLED | H5+H3. G:948 default `false`, `application:false`; aa `genai-ui/vars.env:31` + `genai-ui.json.tmpl:10`, eosdev set `true`; chart `genai/values.yaml:1054` = `include common.dependencies.opensearch.enabled` (dependency toggle, default false); keos-apps `helm.yaml:50-53`. Descriptor and chart agree on `false` | preserve-legacy via patch (already carried); toggle is **D1** | none for the value; D1 |
| OPENSEARCH_HOST, OPENSEARCH_PORT | H3. G:964/980; chart `values.yaml:1055-56` via `dependencies.opensearch.host/port`, empty when the dependency is off; when on it renders `<name>-coordinator.<ns>` (legacy `opensearch1.stratio-datastores` had no suffix) | follows D1 | D1 |
| DISCOVERY_ENABLED, DISCOVERY_URL, DISCOVERY_INTERNAL_URL | H3. G:1920 default `true`, G:1934/1953 `""`; aa `genai-ui.json.tmpl:17-19`; chart `externalDashboards` via `dependencies.discovery.*`, tenant toggle defaults false; URL default `https://<cluster.url>/<name>.<ns>` vs legacy `/discovery` | preserve-legacy via patch (carried); toggle is **D1** | D1 |
| DISCOVERY_DATABASE | H5. Integer id of the Discovery (Metabase) database GenAI queries (G:1969, default `1`); aa `vars.env:29` default 1, eosdev override `3`; the id depends on creation order. Chart `dependencies.discovery.database` is patchable. The plugin catalog **excludes** it (`internal/config/seed.go:319`, commit `b6afcc0`, no reason recorded) so the patch drops `3` | preserve-legacy | **Fix F3** (flux-stratio): drop the exclude; verify the live id first |
| GOVERNANCE_OPENSEARCH_ENABLED | H4. G:587 default `true`; aa `vars.env:32` is dead (the tmpl never uses it), so legacy was `true` by descriptor default; keos-apps `helm.yaml:76` `${...:=false}` flips it, fed by use-cases `resourceset-apps-genai.yaml:319` | needs-decision **D2** | D2 |
| GOVERNANCE_OPENSEARCH_SERVICE, _URL | H4. G:601/617; legacy `opensearch1.keos-core`; keos-apps `helm.yaml:77-78` builds `<cluster>-coordinator.keos-core` from the use-cases cluster name `opensearcher`. **Both clusters exist on eosdev** (`kubectl get svc -A`: `opensearch1*` and `opensearcher*` in `keos-core`). Chart quirk: `governanceOpenSearchServiceName` (no suffix, used by the GosecPolicy) differs from `...Host` (with `-coordinator`, used by the env) | needs-decision **D2** | D2 |
| HA_PROXY_PATH | H5+H3. G:200 default `/${Application.name}`; aa `vars.env:25` `/${APP_NAME}.${APP_NAMESPACE}`, eosdev `/genai-ui`; chart `values.yaml:1022` copies the aa convention | needs-decision **D5** | D5 |
| VIRTUAL_HOST | H5+H3. G:185 default `genai.${tenantId}.${external_domain}`; aa `vars.env:24`; chart `haProxyHost` = `global.cluster.url` (admin host) | needs-decision **D5** | D5 |
| SERVICE_URL | H3 + **chart bug**. G:272 `https://${haProxyHost}/${Application.name}`; chart `values.yaml:1023` builds `https://<cluster.url>//<release>-ui.<ns>` (double slash, commit `5fed353`) | preserve-legacy (descriptor never had `//`) | **Fix F2** (charts): remove the extra slash; value follows D5 |
| MY_ACCOUNT_URL | H3. G:2155 default has `//` (legacy equals it); chart `myProfileUrl` normalises to a single slash plus trailing `/` | accept-new (cosmetic) | none |
| GOVERNANCEAPI_FULL_NAME, GOVERNANCEAPI_URI, GOVERNANCE_URL | H6. G:463/477/574 | accept-new | none (catalog excludes them) |
| VAULT_ROLE | H6. G:331; chart `identity.approlename` | accept-new (memory `vault-role-naming`) | none |

### virtualizer

| Var | Root cause (evidence) | Classification | Fix and repo |
|---|---|---|---|
| EXTRA_JARS | H2+H5. Not in `parameters`: V `lifecycles.install.start[26]` (also `update.start[16]`, `upgrade.start[16]`), a `model/parameter/upsert` of `${environment.artifacts.extensionsList},…connectorJdbcDrivers`; legacy value filled by aa `virtualizer/pre-install.sh:30-66` only when `VIRTUALIZER_DECOUPLED_CONNECTOR_{POSTGRES,HDFS}=true` (default false, eosdev true). The hdfs flavor now supports it (`feb37d7`, in the published snapshot); eosdev renders none because the tenant does not set the jars, and the legacy URLs point at `connectors-management` | needs-decision **D6** | D6; chart side done; see `TASK-virtualizer-decoupled-connector-jars.md` |
| EXTRA_JARS_SPARK_CLASSPATH, EXTRA_JARS_SPARK_EXECUTOR_CLASSPATH | H2. V install.start[24]/[25]; legacy `,,,,` and `,` (all parts empty) | accept-new (exact no-op on hdfs) | none |
| SERVER_MAXHTTPHEADERSIZE (monitor) | H5. In no descriptor: aa `virtualizer-monitor.json.tmpl:24-28` (`general.custom`, the generic `CONFIGURATION` parameter of `stratiobase:default`) and `:74` (top-level key); bulk-imported in `9c9460a`, removed from virtualizer-api in `9b214d5` but kept for the monitor, no explanation. Spring Boot relaxed binding maps it to `server.max-http-header-size` (default 8 KB); legacy pod has `65536`. The chart has no free-form env path for the monitor. Not proven that the monitor reads it (image not available locally) | needs-decision **D3** (my recommendation: preserve) | D3 |
| VR_DEPLOYMENT, VR_SERVICE | false positive (fieldRef annotation, TASK-04 / TASK-00 #07) | n/a | TASK-04 |

### dg-hdfs-agent (chart `dg-agent`)

| Var | Root cause (evidence) | Classification | Fix and repo |
|---|---|---|---|
| COMM_SERVICE_NAME | H7+H5. Parameter `customServiceName` (AC:335, required, no default); aa `connector-dfs-hdfs.json.tmpl:138` `${SERVICE_NAME}`, `vars.env:20` default `dg-hdfs-internal-agent`, eosdev `hdfs1.stratio-datastores`; chart `dg-agent_common_env_vars.yaml:38` hard-codes `{{ .Release.Name }}` since `29a4347`; keos-apps `hdfs/base/helm-patch.yaml:7` sets `releaseName: dg-hdfs-agent`. Governance sees `datastoreName=dg-hdfs-agent` while GoSec and HDFS stay `hdfs1.stratio-datastores` | preserve-legacy | **Fix F1** (charts): value `agent.config.common.customServiceName`, default release name; then the plugin can patch it |
| CUSTOM_SERVICE_DS_DRIVER_LOCATION | H3+H6. AC `…agent-hdfs-internal-default.json:119`; legacy `connectors-management/v1/api/artifact/Connector_sscc-hdfs_2.12_1.1.1-922b277.jar`; chart `connectors/dg-agent_hdfs_env_vars.yaml:5` from `dependencies.connectors.*`; live `http://connectors.stratio-apps/sscc-hdfs-0.4_2.12-1.1.1-922b277.jar` (new service, new file name) | accept-new | none |
| CUSTOM_SERVICE_SSCC_DRIVER_LOCATION (not in the original list) | The patch carries the legacy value (`agent.config.hdfs.customSSCCDriverJar`, chart default `""`); live still has the `connectors-management` URL, which exists today (`deploy/connectors-management` is still up in `stratio-apps`). Whether the agent reads it is unknown (TASK-03 section C) | preserve-legacy (carried); revisit when `connectors-management` is retired | none now |
| COMM_DS_URL, COMM_DS_URL_LL, DATASTORE_HOST | H3. AC:246/259/200; chart builds from `common.dependencies.postgres.*` with explicit `:5432` | accept-new (neutral) | none |
| DISCOVERY_BASE_URI, DISCOVERY_MANAGEMENT_BASE_URI, GOVERNANCE_BASE_URI, GOVERNANCE_DEPLOYMENT, GOVERNANCE_MANAGEMENT_BASE_URI | H6. Hard-wired in the chart (`:61-74`) | accept-new | none |
| VAULT_ROLE | H6 | accept-new | none |
| CUSTOM_STRATIO_CREDENTIALS | H7. Chart default blank after `0a1e649`, which is in the published snapshot and live (`CUSTOM_STRATIO_CREDENTIALS` is blank in `dg-hdfs-agent-hdfs-config`) | closed | none |

### rocket, probes, others

| Var | Root cause (evidence) | Classification | Fix and repo |
|---|---|---|---|
| rocket `CUSTOMER_SIZE` (M) | H2+H3. A real parameter (R:891-902, `application:false`), used only by `lifecycles.upgrade/pre` (R:9855-9900) to upsert `STARTUP_PROBE_FAILURE_THRESHOLD` 120/240/480/720 for S/M/L/XL. Chart sets native `startup failureThreshold: 120` (`values.yaml:654-667`), no size escalation. aa `rocket/pre-upgrade.sh:67` sets 1000 during upgrade | accept-new for install; **D4** for upgrades | D4 |
| rocket `MIGRATION_STORAGE_PROFILE` (medium) | H2. R:912-923, description says it estimates DB-migration downtime; no lifecycle references it. Reader unknown | needs-decision **D4** | D4 |
| rocket `*_PROBE_*` (9) | H3. R:943-1063 feed `data.probes`; chart native probes `values.yaml:639-690` have the same failureThreshold/period/timeout as the legacy Deployment | accept-new (false positive) | none |
| rocket `SSCC_S3A_GLOBAL_CREDENTIALS` (s3) | H3. A parameter of the s3 group (`rocket.json:159-167`), emitted even in HDFS mode; chart renders it only in the s3 flavor (`rocket_s3_env_vars.yaml:11`, default `internal-s3-secret`); eosdev rocket is HDFS | accept-new while on HDFS; becomes preserve-legacy (`s3`) if it moves to S3 | none now |
| `NOT_USED` (rocket, intelligence, genai-api) | H2. A sink `internalName` shared by unrelated parameters (R:4536, 5422, 5449, 5476; intelligence:767, 1309; genai-api:1095-1139, 1387, 1401); empty value | accept-new | none |
| psql-gosec-agent, opensearch1-gosec-agent: 8 `*_PROBE_*` each | H3 (not H2): plain parameters (`security/pg-agent.json:252-357`, `os-agent.json:247-352`) replaced by native probes (`gosec-agent/values.yaml:46-75`) with the same values, except readiness `failureThreshold` (chart 1, legacy and descriptor 3) | accept-new for 7 of 8; `failureThreshold` is preserve-legacy | owned by TASK-02 |
| connectors (16) | H8. Legacy `connectors-management` is the REST app (SQLite, JWT, Vault); the `connectors` chart is a `connectors-loader:2.2.0` init container plus busybox `httpd` on port 80 (`connectors/values.yaml:20-40`) | accept-new, out of the descriptor question | none |

Other observations, not in the inventory: rocket chart `terminationGracePeriodSeconds` is 30 for startup/liveness while legacy is 60; genai-api `chainDevelopment` parameters (`exposeMtlsPortInLB`, `loadBalancerK8sServiceName`, `extraSANToAddToCertificate`, `exposeInsecurePyPiPort`) have no counterpart in the genai chart (application-level parameters, not env vars); `connectors-management` is still running and the legacy `EXTRA_JARS`/SSCC URLs point at it.

## Proposed fixes (none applied)

| # | Repo | Change | Verification |
|---|---|---|---|
| F1 | charts (branch in `charts-align-15.1.0`) | dg-agent: add `agent.config.common.customServiceName` (default `{{ .Release.Name }}`) and use it in `dg-agent_common_env_vars.yaml:38`; use the `charts` tools where they can express it | `helm template` before/after with the dg-hdfs-agent HelmRelease values (unchanged by default, set when given); `flux stratio apps diff dg-hdfs-agent --baseline <backup>` shows `COMM_SERVICE_NAME` patchable |
| F2 | charts | genai `values.yaml:1023`: remove the double slash in `serviceUrl` | `helm template` shows a single slash |
| F3 | flux-stratio | drop the `DISCOVERY_DATABASE` exclude in `internal/config/seed.go:319` (+ `seed_test.go`, `CHANGELOG.md`, `docs/config-reference.md` if it lists excludes) after confirming the live Discovery database id | `go test ./...`; `apps diff genai --baseline` shows it in the patch (needs the TASK-10 fix or a scratch build) |
| F4 | flux-stratio (optional, only if wanted) | make `apps diff/migrate` say why a variable is "lost" by channel (probe env var expressed as native probe, lifecycle upsert, not in descriptor, dependency-derived) | tests under `internal/diff` with fixtures |

Not proposed: a `Chart.yaml` descriptor-release annotation or a doctor check of chart versions. This investigation found no skew, so the case for them is weaker than the brief assumed; the user may still want them (TASK-03 plugin item 1).

## Decisions for the user

- **D1.** genai: enable the `opensearch` and `discovery` dependencies in the tenant file, or keep relying on the patch values? Enabling also changes `dependsOn`, the GosecPolicy `genai-api-governance-opensearch`, keos-apps `gosec.yaml:7`, the discovery postrequisites, and gives the OpenSearch host a `-coordinator` suffix.
- **D2.** genai governance OpenSearch: legacy was enabled and used `opensearch1.keos-core`; GitOps defaults to disabled and points at `opensearcher-coordinator.keos-core`. Both clusters exist on eosdev. Which one holds the governance indices, and should the tenant keep the legacy one?
- **D3.** virtualizer-monitor `SERVER_MAXHTTPHEADERSIZE=65536`: preserve (needs a chart extra-env value for the monitor and a plugin mapping for legacy `general.custom` keys) or drop. The monitor sits behind oauth2; dropping risks 431/400 on large cookies.
- **D4.** rocket: ask the rocket team whether the server reads `MIGRATION_STORAGE_PROFILE`, and whether upgrades need size-based startup-probe escalation (chart budget is 120 x 5 s).
- **D5.** genai host and path: keep `genai.<tenant>…` and `/genai-ui`, or move to the admin host with `/<release>-ui.<ns>`? The patch can carry either.
- **D6.** virtualizer connector jars on eosdev: set `dependencies.connectors.config.{hdfs,postgresql}.connectorJar` in the tenant file (the chart side is published), or accept running without the decoupled connectors. See `TASK-virtualizer-decoupled-connector-jars.md`.

Open points: the live Discovery database id (needed before F3); whether the monitor really reads `SERVER_MAXHTTPHEADERSIZE`; whether the agent reads `CUSTOM_SERVICE_SSCC_DRIVER_LOCATION`.

## Fixes applied (2026-10-14, uncommitted; the user decided which of F1-F4 to do)

The decisions D1-D6 were moved to `TASK-12-chart-variable-decisions.md` for a separate session (D6 is the `EXTRA_JARS` connector-jar choice).

| # | Decision | Where | What was done and verified |
|---|---|---|---|
| F1 | do, "the chart must also work in pure GitOps mode (not migrated)" | charts worktree `charts-align-15.1.0`: `dg-agent/config/dg-agent_common_env_vars.yaml`, `dg-agent/values.yaml`, `CHANGELOG.md` | New value `agent.config.common.customServiceName` (default `""`, the 15.1.0 descriptor's parameter name); `COMM_SERVICE_NAME: {{ .Values.agent.config.common.customServiceName \| default .Release.Name \| quote }}`. **Default behaviour is unchanged**: `helm template dg-hdfs-agent` before (the deployed snapshot) and after render byte-identical output apart from the chart version labels; `dg-postgresql-agent` with the CI values still renders `COMM_SERVICE_NAME: dg-postgresql-agent`; with `--set agent.config.common.customServiceName=hdfs1.stratio-datastores` the ConfigMap holds that value. The `charts analyze` tool (descriptor ZIP built from `54ab1918`) reports the variable as MATCHED against the existing line, so it cannot express the change; the value path follows the chart's own `agent.config.common.<descriptor parameter name>` convention. After the chart is published, the plugin patches it like any other path (not tried: it needs the chart on the render path and a migration run, so it is the next check). |
| F2 | do, same constraint | `genai/values.yaml:1023`, `CHANGELOG.md` | `serviceUrl` loses the extra slash: rendered `SERVICE_URL: https://admin.example.test/genai-ui.stratio-genai` (single slash) with the CI values. A fresh install is affected the same way, so this is a pure bug fix. |
| F3 | **not done** (the user said to ignore it) | - | The `DISCOVERY_DATABASE` catalog exclude stays. Noted in `TASK-12` as a manual check when genai is migrated. |
| F4 | do | flux-stratio: `internal/diff/nativeprobe.go` (+ test), `chartdiff.go`, `internal/appdiff`, `internal/appmigrate`, `internal/cli/review_sections.go` (+ test), `CONTEXT.md`, `docs/migration-runbook.md`, `CHANGELOG.md` | A legacy `{LIVENESS,READINESS,STARTUP}_PROBE_*` variable whose rendered workload has the same value in the matching container probe is listed in a new note section, `carried natively by the chart` (not a warning), instead of in "values are lost"; one whose chart probe differs stays in the lost list with the chart's value in the WHY column; `NOT_USED` gets its own WHY. `go vet ./...` and `go test ./...` pass. Checked on the real backup: `apps diff psql-gosec-agent --baseline ~/.fluxcd/flux-stratio/backups/psql-gosec-agent/2026-10-13T11-00-17Z -v` now shows 7 probe variables as carried natively and one real difference, `READINESS_PROBE_FAILURE_THRESHOLD` (chart 1, live 3), which is TASK-02's finding. Not done: a catalog field for accepted differences, a doctor check of chart vs deployed version (no skew was found, see the investigation). |

Nothing was committed or pushed.
