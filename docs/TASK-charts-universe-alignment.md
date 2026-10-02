# Task: re-verify every Helm chart against the CCT descriptor universe and align what is stale

**Status: open.** Repo: **`Stratio/charts`** (checkout `/stratio/charts/charts`, `master`, chart version
`15.1.0-SNAPSHOT`; **not** flux-stratio: this file lives here only so it sits next to its siblings).
Tool: the legacy Python client's `charts` subcommand at `/stratio/charts/charts/bin/migration`
(`migrate.py charts ...`). Related: `docs/TASK-keos-apps-env-list-overlays.md` (do that first or
together for rocket/virtualizer), `docs/TASK-chart-spec-comparison.md` (re-measure after this task).

## Why

The eosdev migration comparison showed the plugin's chart renders lag what is actually deployed.
Concrete evidence:
- `rocket/values.yaml:594` defaults the image tag to `4.0.1`; the universe's
  `universe-images.lst:197-200` lists `rocket-api|driver|executor|ml-prediction-server:4.1.0`, and live
  eosdev runs 4.1.0.
- eureka-agent renders image 1.0.1 while live (and `universe-images.lst:144`,
  `eureka-bdl-agent_2.12:1.1.0`) is 1.1.0; dg-hdfs-agent renders 1.1.1 against live 1.2.0 (check its
  entry in `universe-images.lst`).
- 34 live-only variables on rocket, 7 on genai, 4 on virtualizer, 1 each on intelligence and
  eureka-agent are not rendered by the chart (`flux stratio apps diff <app> --baseline latest`; the
  names are listed at the end of this file). Many are variables newer than the chart
  (`GENAI_*`, `GOVERNANCE_AVAILABILITY_*`, `SPARTA_CONFIG_*`), the rest legacy CCT artefacts that
  should stay unrendered (`CUSTOMER_SIZE`, `MIGRATION_STORAGE_PROFILE`, `*_PROBE_*`, `NOT_USED`).
- the charts repo claims alignment (`a9acf1c`, "Align charts with kubernetes-universe 15.1 final
  release (#248)"), so the question is: aligned with **which ref**, and what was not covered.

## What the tool covers, and what it does not (read first)

Read `/stratio/charts/charts/bin/migration/README.md` fully. Key points:
- Descriptor identity is `data.serviceName:data.model:data.version` **inside** the JSON, not the file
  name; use the version the JSON declares (no `-SNAPSHOT` unless the JSON has it).
- Subcommands: `charts analyze` (diagnose: MATCHED / "available to add" / removed), `charts values`
  and `charts environment` (**print to stdout only**; never redirect to the file you read from), `charts
  import` (scaffold, writes), `charts commons`, `charts validate`; plus `bin/check_descriptors.sh`
  (renders with `helm template`; catches what `validate` cannot).
- **`charts` only covers the descriptor's `parameters` block.** Workflow D (README §6.4) exists
  because a version bump also changes `container.runners[].image` (the most frequent change; **no
  subcommand detects it**), `healthChecks`, `networking`, `secrets`, `permissions`, `dependencies`,
  `resources`, `priorities`, `constraints`. Closing this task after `charts analyze` alone is wrong.
- `analyze` compares the descriptor against what is **already tracked**, not against the previous
  universe version, so long-untracked parameters show as "available to add" noise: separate signal by
  diffing `--scaffold` output between two universe versions (README §6.4 step 2).

## Inputs and where they are
- Universe repo: `/stratio/p/kubernetes-universe` (branches `branch-15.1` checked out at
  `b4ea15e3`, `master`, `branch-14.10`, `branch-14.8`; also `/stratio/p/kubernetes-universe-cct-3.0.26`).
  Descriptors in `packages/<folder>/*.json`; image versions in `universe-images.lst`.
- The ZIP the CLI wants is not in the charts repo (and must **not** be committed). Build it per
  README §6.4 step 1 (`git archive <ref> packages/ | tar -x`, zip the contents of `packages/`).
- Chart ↔ descriptor-folder map: README §4 and `bin/migration/config.json` (`chart_path`). Charts in
  the repo not in that table (`dg-agent`, `eureka-agent`, `litellm`, `opendata`, `governance-datamarket`,
  `governance-datamarket-agent`, `search-engine`, `spark-history`, `gosec-agent`, ...) need their
  mapping established in step 1. Operator-managed components (hdfs, kafka, opensearch, postgres,
  pgbouncer) have no chart and are out of scope.

## Steps

### 1. Decide the target ref (do not skip)
The eosdev CCT deployment may come from a newer snapshot than `branch-15.1`. For a sample of the
live-only variables (e.g. `GENAI_CHAIN_SQL_CHAT_HISTORY_TOKEN_LIMIT`, `GOVERNANCE_AVAILABILITY_*`,
`SPARTA_CONFIG_WORKFLOW_DEPLOY_VALIDATION_ENABLED`, `GOVERNANCE_PAGE_SIZE`) `grep` the descriptors on
`branch-15.1`, on `master`, and in `-cct-3.0.26`. If they exist only on a newer ref, that ref is the
alignment target (or at least raise it with the user: which release does the chart `15.1.0` promise?).
Record the conclusion in the report.

### 2. Build the chart ↔ descriptor matrix
One row per chart directory in `/stratio/charts/charts` (excluding `common`, `common-installable`) × per
descriptor/flavor that feeds it (a chart can have several: `bdl-datarest` 9 flavors, `genai` 4,
`rocket` 5 ...). Columns: chart, `service:model:version`, `config/<flavor>_env_vars.yaml`, `--prefix`
used in `values.yaml`, last aligned ref (from `git log`/CHANGELOG).

### 3. Per row, run the parameters check
```bash
cd /stratio/charts/charts && source bin/migration/.venv/bin/activate   # python3 -m venv + pip install -r requirements.txt if absent
python3 bin/migration/migrate.py charts analyze --zip-file <zip> -s <service> -m <model> -v <version> \
  --env-vars <chart>/config/<flavor>_env_vars.yaml --values <chart>/values.yaml --prefix <prefix>
python3 bin/migration/migrate.py charts analyze ... --env-vars /dev/null --scaffold | sort > /tmp/scaffold_<ref>.txt   # for the two-version diff
python3 bin/migration/migrate.py charts validate --values <chart>/values.yaml --config <chart>/config/<flavor>_env_vars.yaml
bash bin/check_descriptors.sh <chart> [--type-key ...] [--set ...]
```
Record: variables MATCHED, descriptor variables **not yet tracked** (candidates to add), **removed**
variables left in `values.yaml`, `validate` failures, `check_descriptors.sh` failures. Also note any
default whose scaffold value changed between the previous and target ref.

### 4. Per row, run the non-parameters check (Workflow D, step 3)
Diff descriptor JSON excluding `parameters` and versions between the previous aligned ref and the
target ref (README snippet). Compare the result and the *current descriptor* against the chart for:
- `container.runners[].image` tag → chart `controllers.<c>.containers.<c>.image.tag` and
  `universe-images.lst`. **List every chart whose default tag differs from the universe.**
- `resources` → the chart's default `controllers.*.containers.*.resources` and the keos-apps S/M/L
  overlays (they standardize per size; note where the descriptor and the chart disagree).
- `healthChecks` → probes; `networking`, `secrets`/`permissions` (SecretsBundle/SecretsIdentity in
  `lifecycles.install`), `dependencies`, `priorities`, `constraints` → chart templates/values.

### 5. Cross-check against the live eosdev evidence
For each live-only variable listed below, classify: (a) exists in the target descriptor → add to
the chart (`import`/`values`/`environment` workflow B); (b) CCT artefact absent from the target
descriptor → leave unrendered, document; (c) newer than the target ref → raise.

### 6. Make the changes (one PR per chart, or per small family; never one giant PR)
- Workflow B for added/removed parameters; image tag bumps by hand (no subcommand does it);
  keep the CHANGELOG convention (`## 15.1.0 (upcoming)` list, `[PLT-xxxx]`/`[NOJIRA]` prefix).
- After editing `env_vars.yaml`, `values.yaml` blocks may hold CCT markers (`${Application.id}`,
  `${tenantId}`, `${k8sNamespace}`, `${external_domain}`): translate them (README §5.4 warning).
- `bdl-datarest`: README §7.1 documents a pre-existing mismatch
  (`templates/common.yaml` expects `config/bdl-datarest-oraclecommon_env_vars.yaml` /
  `bdl-datarest-pgcommon_env_vars.yaml`, files are named without the `bdl-datarest-` prefix). Check whether
  it still exists and report it; fix only if trivially safe.
- Do not touch litellm beyond what PR #257 / keos-apps #147 / keos-use-cases #202 (PLT-4838 litellm
  name) already do. Do not commit the universe ZIP. Do not edit `bin/migration` unless a bug blocks the work.
- Resource defaults: do not change chart defaults only to match one live cluster; resources are
  configured per size in keos-apps overlays and per tenant in patches. Report mismatches versus the
  descriptor instead; the decision is the user's.

### 7. Verify
- `charts validate` + `bin/check_descriptors.sh` for every touched chart; `pytest bin/migration/tests`
  if the CLI changed.
- Re-run on eosdev (read-only): `flux stratio apps diff <app> --baseline latest` for rocket, genai,
  virtualizer, intelligence, eureka-agent, dg-hdfs-agent, discovery, dlc-entity, datamarket-agent.
  Expected: the "N variables aren't rendered by the chart" lists shrink to the legacy CCT artefacts;
  the rendered image tags equal live. Attach before/after counts to each PR.

## Deliverable
First, a **report** (PR description of the first PR, or a doc outside the repo) with the matrix from
step 2, per-row findings from steps 3-5, and the target-ref decision from step 1. Show it to the user
before bulk edits: it determines how many PRs there are.

## Live-only variables on eosdev (input for step 5, from `apps diff --baseline latest`)
- rocket (34): `CUSTOMER_SIZE`, `CUSTOM_LINEAGE_AND_QRS_FILE_SYSTEM_METHODS`,
  `GENAI_CHAIN_GOVERNANCE_GLOSSARY_CACHE_REFRESH_INTERVAL_SECONDS`, `GENAI_CHAIN_SQL_CHAT_HISTORY_TOKEN_LIMIT`,
  `GENAI_GENERIC_SQL_CHAIN_INVOKE_PARAM_cde_quality_blocking_{allow_expansion,enabled}`,
  `GENAI_SQL_CHAIN_INVOKE_PARAM_cde_quality_blocking_{allow_expansion,enabled}`,
  `GOVERNANCE_AVAILABILITY_{DETECTION_ENABLED,FAILURE_THRESHOLD,PROBE_INTERVAL}`,
  `{LIVENESS,READINESS,STARTUP}_PROBE_{FAILURE_THRESHOLD,PERIOD_SECONDS,TIMEOUT_SECONDS}`,
  `MIGRATION_STORAGE_PROFILE`, `NOT_USED`, `ROCKET_POD_NAME` (overlay-shadowed, see the keos-apps task),
  `SPARTA_CONFIG_CATALOG_CACHE_DEFAULT_IDENTITY_REFRESH_INTERVAL`, `SPARTA_CONFIG_CATALOG_CACHE_MIN_AGE_FOR_INVALIDATION`,
  `SPARTA_CONFIG_CATALOG_JDBC_POOL_{ENABLED,IDLE_TIMEOUT}`, `SPARTA_CONFIG_VALIDATOR_CACHED_TABLES_ASK_TIMEOUT`,
  `SPARTA_CONFIG_VALIDATOR_SKIP_PREDECESSOR_CHECK`, `SPARTA_CONFIG_WORKFLOW_BLOCK_SENSITIVE_ACCESS_IN_CODE_STEPS`,
  `SPARTA_CONFIG_WORKFLOW_DEPLOY_VALIDATION_ENABLED`, `SPARTA_TIMEOUT_WORKFLOW_RUN`,
  `SPARTA_WORKFLOW_SCHEDULER_RECONCILIATION_GOVERNANCE_SYNC_MAX_WAIT`, `SSCC_S3A_GLOBAL_CREDENTIALS`.
- genai (7): `genai-api/NOT_USED`; `genai-ui/GENAI_GOVERNANCE_CHAT_BUSINESS_TERM_PROPOSAL_REVERSE_RELATIONS`,
  `…_KNOWLEDGE_PROPOSAL_DOMAIN`, `…_SKIP_FK_DETECTION_WITH_VIRTUAL_FKS`, `…_TARGET_STATUS_FOR_PROPOSALS`,
  `GOVERNANCE_GLOSSARY_CACHE_REFRESH_INTERVAL_SECONDS`, `GOVERNANCE_PAGE_SIZE`.
- virtualizer (4): `CONF_KV_spark_sql_warehouse_dir`, `EXTRA_JARS`, `EXTRA_JARS_SPARK_CLASSPATH`,
  `EXTRA_JARS_SPARK_EXECUTOR_CLASSPATH`; plus `VR_DEPLOYMENT`/`VR_SERVICE` (live fieldRef to a CCT
  annotation vs rendered literal; not a chart gap).
- intelligence (1): `NOT_USED`. eureka-agent (1): `BDL_GOVERNANCE_ONTOLOGY_DROP_OPERATIONAL_COLLECTION`.
  discovery, dlc-entity, datamarket-agent, litellm: none.
Their classification (descriptor-backed vs CCT artefact) is NOT done yet: that is step 5.
