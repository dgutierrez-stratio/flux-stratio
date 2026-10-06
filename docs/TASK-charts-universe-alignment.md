# Task: re-verify every Helm chart against the CCT descriptor universe and align what is stale

**Status: done, pending merge.** Implemented in **`Stratio/charts` PR #258**
(https://github.com/Stratio/charts/pull/258, branch `dgutierrez-stratio/align-15.1.0`, worktree
`/stratio/aws/flux/.worktrees/charts-align-15.1.0`, opened 2026-10-02): see "Outcome" at the end of this
file for what landed, how it was verified and what is left. Do not redo the work: if you pick this task up,
start from that PR. Repo: **`Stratio/charts`** (checkout `/stratio/charts/charts`, `master`, chart version
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
  `eureka-bdl-agent_2.12:1.1.0`) is 1.1.0; dg-hdfs-agent renders 1.1.1 against live 1.2.0.
  dg-hdfs-agent is not a separate chart or image: it is the **`dg-agent` chart** with
  `agent.type=hdfs`, whose single image `dg-custom-agent_2.12` (all 23 agent types) is `1.2.0` at the
  release (`universe-images.lst:133`) and `1.1.1` in the chart (`dg-agent/values.yaml:~195`).
- 34 live-only variables on rocket, 7 on genai, 4 on virtualizer, 1 each on intelligence and
  eureka-agent are not rendered by the chart (`flux stratio apps diff <app> --baseline latest`; the
  names are listed at the end of this file). Many are variables newer than the chart
  (`GENAI_*`, `GOVERNANCE_AVAILABILITY_*`, `SPARTA_CONFIG_*`), the rest legacy CCT artefacts that
  should stay unrendered (`CUSTOMER_SIZE`, `MIGRATION_STORAGE_PROFILE`, `*_PROBE_*`, `NOT_USED`).
- the charts repo claims alignment (`a9acf1c`, "Align charts with kubernetes-universe 15.1 final
  release (#248)"), but `branch-15.1` keeps moving (see below), so the question is: which 15.1
  commit was each chart aligned to, and what changed in 15.1 since then or was never covered.

## Universe reference (decided by the user, 2026-10-02): the 15.1.0 **release** commit only

**Every chart/descriptor comparison in this task is made against the 15.1.0 release of
`kubernetes-universe`** (`/stratio/p/kubernetes-universe`). Not the `branch-15.1` tip, not `master`,
not 14.x, not `-cct-3.0.26`. The same rule applies to the sibling tasks (they link here).
- **Reference commit: `54ab1918`** ("[BUILD] 15.1.0: New pre-release", 2026-09-11). Its parent
  `7324e952` is byte-identical apart from version strings (descriptors read `15.1.0-BUILD` at
  `54ab1918` and `15.1.0-SNAPSHOT` at `7324e952`; `universe-images.lst` is identical). Use
  `54ab1918` and pass the version the JSON itself declares (`-v 15.1.0-BUILD`, README §3 note); if the
  tool or plugin rejects it, use `7324e952` with `-v 15.1.0-SNAPSHOT`. **Record both SHAs in the
  report and in every PR, and do not mix SHAs within one run.**
- Why not the branch tip: branch 15.1 is meant to be released and closed, yet it kept moving toward
  `15.1.1-SNAPSHOT` (tip `f14a1355` on 2026-10-02, 42 commits past `b4ea15e3`). Those later commits
  introduced SNAPSHOT/milestone image tags (genai `0.8.1-SNAPSHOT`, dlc-entity `4.0.4-SNAPSHOT`,
  intelligence `5.1.2-SNAPSHOT`, virtualizer `1.1.1-SNAPSHOT`, dg-custom-agent `1.2.1-M2`, spark
  `3.5.9-1.0.0-M3`). **Use only final tags**: at `54ab1918` every image tag is final.
- Baseline for `/charts:upgrade` / `compare-descriptors`: `b4ea15e3` (2026-09-08, where #248 aligned
  the charts) → `54ab1918`. That window only changed connectors-management (Recreate + 4Gi), the
  pg/os gosec agents 3.0.5 (already integrated by #252), the removal of `mlmodel-server.json` and
  version strings. The remaining work is therefore chart lag that predates #248 plus parameters #248
  missed, not new universe changes.
- Anything present on eosdev but absent from `54ab1918` is not a chart gap: report it. Anything that
  landed after the release is listed in "Post-release deltas, not applied" below, never applied.

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

## Use the `charts` Claude Code plugin (`charts@stratio-platform`) for the heavy lifting

Installed from `/stratio/IA/platform-claude-plugins/plugins/charts` (read its `README.md` and the
`skills/*/SKILL.md` you use). It wraps the same `bin/migration` CLI with guided, resilient steps and
covers exactly the gap the plain CLI leaves (non-`parameters` sections). Use its slash commands
instead of re-deriving the manual commands below wherever it fits:

| Step of this task | Use | Why |
|---|---|---|
| 1 target ref, 2 matrix | `/charts:compare-descriptors` | Read-only, chart-independent report of what changed in one descriptor between two releases. Diffs `parameters`, merged (`parentConfiguration`-resolved) `data` and `lifecycles`, both as the effective result and per inheritance level (which ancestor introduced it). Good for scoping each chart before touching it and for seeing which 15.1 commit introduced a variable. Both versions given to it are `b4ea15e3` and the release `54ab1918` (see "Universe reference"); never compare against the branch tip, `master` or 14.x |
| 3 + 4 + 6 per chart | `/charts:upgrade` | Runs Workflow B (parameters) **and** the Workflow D diff (image tag, `data`, `lifecycles.install` via `charts analyze --data/--lifecycles`), applies only what differs, audits `lifecycles.upgrade` for tasks never mirrored in `install`, and is the only skill that removes entries (separate confirmation; never auto-deletes secrets/GoSec/Postgres entries) |
| new component only | `/charts:import` (`scaffold` → `data` → `lifecycles`) | Not expected here; every chart in the repo already exists |

Both the plugin and the repo's CLI (`migrate.py charts analyze --data --lifecycles` exists in
`master`) are compatible. The plugin materializes universe releases itself as detached git worktrees
under `/tmp/kubernetes-universe-cache/worktrees/` from an existing `kubernetes-universe` checkout
(it prefers `/stratio/p/kubernetes-universe`), so **you do not need to build the ZIP by hand** when
using it. Run it from `/stratio/charts/charts` (or a charts worktree).

Limits you must still cover yourself (the plugin says so in its own Roadmap):
- `/charts:upgrade` needs the **baseline** (currently-aligned) and **target** versions given
  explicitly, because the repo tracks no version metadata (git-log fallback). It diffs
  *release → release*: it will not find a chart that was already out of step with its own baseline
  (e.g. rocket's default tag 4.0.1 against the universe's 4.1.0). Check the current state against
  `universe-images.lst` / the target descriptor (`charts analyze`, step 3-4 below) as well.
- Not covered: leftover `${...}` markers, full secrets/GoSec review, non-`install`/`upgrade`
  lifecycles, `charts commons` extraction, `keos-apps` wiring, comparing resources with the keos-apps
  overlays, and classifying the live-only variables against the eosdev cluster (step 5). Those stay
  manual here.
- It edits files and may delete `values.yaml` entries: run it on a branch/worktree, one chart per
  run, review each diff, and answer its removal gate deliberately.
- It needs network/SSH to `github.com/Stratio/kubernetes-universe` only to refresh a checkout;
  it works offline from your existing checkout if the refs are already local.

## Inputs and where they are
- Universe repo: `/stratio/p/kubernetes-universe` (read it with `git show 54ab1918:<path>` /
  `git archive 54ab1918 packages/`; the checkout itself sits on the `branch-15.1` tip `f14a1355`, and
  every other ref (`master`, `branch-14.x`, `-cct-3.0.26`, the tip) is **not used** by this task).
  Descriptors in `packages/<folder>/*.json`; image versions in `universe-images.lst`.
- The ZIP the CLI wants is not in the charts repo (and must **not** be committed). Build it per
  README §6.4 step 1 (`git archive <ref> packages/ | tar -x`, zip the contents of `packages/`).
- Chart ↔ descriptor-folder map: README §4 and `bin/migration/config.json` (`chart_path`). Charts in
  the repo not in that table (`dg-agent`, `eureka-agent`, `litellm`, `opendata`, `governance-datamarket`,
  `governance-datamarket-agent`, `search-engine`, `spark-history`, `gosec-agent`, ...) need their
  mapping established in step 1. Operator-managed components (hdfs, kafka, opensearch, postgres,
  pgbouncer) have no chart and are out of scope.

## Steps

### 1. Pin the universe ref (decided by the user: the 15.1.0 release commit `54ab1918`; do not change it)
The target is the **15.1.0 release of `kubernetes-universe`**, commit `54ab1918` (identical content at
`7324e952`). Do not use the `branch-15.1` tip, `master`, `branch-14.x` or `-cct-3.0.26` for any
comparison. See "Universe reference" above.
- No fetch is needed (the commit is fixed); record `git rev-parse 54ab1918` and the `data.version`
  the descriptors declare, and use that single SHA for the whole task.
- For a sample of the live-only variables (e.g. `GENAI_CHAIN_SQL_CHAT_HISTORY_TOKEN_LIMIT`,
  `GOVERNANCE_AVAILABILITY_*`, `SPARTA_CONFIG_WORKFLOW_DEPLOY_VALIDATION_ENABLED`,
  `GOVERNANCE_PAGE_SIZE`) `grep` the descriptors **at `54ab1918`**. A variable that exists there and
  is missing from the chart is a chart gap to fix. One that does **not** exist there (only live, or
  only after the release) is **reported and left alone**: record it in the report as "post-release /
  CCT artefact", never add it to the chart.
- Use `/charts:compare-descriptors` between `b4ea15e3` and `54ab1918` (below) to see which commit
  introduced each variable.

### 2. Build the chart ↔ descriptor matrix
One row per chart directory in `/stratio/charts/charts` (excluding `common`, `common-installable`) × per
descriptor/flavor that feeds it (a chart can have several: `bdl-datarest` 9 flavors, `genai` 4,
`rocket` 5 ...). Columns: chart, `service:model:version`, `config/<flavor>_env_vars.yaml`, `--prefix`
used in `values.yaml`, last aligned ref (from `git log`/CHANGELOG).

### 3. Per row, run the parameters check
(`/charts:upgrade` runs this for you per flavor; the raw commands are listed so you can reproduce or
spot-check what it reports. Do the current-state checks even if `upgrade` is used, see its limits above.)
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
the chart (`import`/`values`/`environment` workflow B); (b) CCT artefact absent from the pinned 15.1
descriptor → leave unrendered, document; (c) exists only after the release commit `54ab1918` →
report, do not add.

### 6. Make the changes (one PR per chart, or per small family; never one giant PR)
Prefer `/charts:upgrade <chart>` (baseline + target given explicitly) over hand edits, then review
its diff; fall back to the manual Workflow B/D below for anything it reports but does not apply.
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
step 2, per-row findings from steps 3-5, and the target-ref decision from step 1. The audits below
already settled the per-chart outcome (so the number of PRs is known); the report still needs the
matrix and the verified commands. Hand each chart to an implementing agent with its section below as
the brief.

## What "aligned" means (decided 2026-10-02)
A chart is aligned with the release `54ab1918` when:
1. **Image tags** equal the descriptor's `container.runners[].image` and the `universe-images.lst`
   line (checked by hand: no tool detects it, and `/charts:upgrade` diffs release to release, so it
   misses a chart that was already behind at baseline).
2. **Parameters**: every descriptor parameter that is a real env var of the workload is tracked in
   `config/<flavor>_env_vars.yaml` plus `values.yaml` (under the flavor's prefix) with the
   descriptor's default; parameters the descriptor lacks are not tracked, unless chart-owned on purpose.
3. **Non-parameter sections** (`lifecycles`, `healthChecks`, `networking`, `secrets`, `permissions`,
   `dependencies`) are reflected when they changed (nothing relevant changed in the baseline-to-release window).
4. **Left alone, reported only:** CCT artefacts (`NOT_USED`, `application:true` entries such as
   `CUSTOMER_SIZE`/`MIGRATION_STORAGE_PROFILE` in the catalog flavor, `VR_*` fieldRefs, `HOST` and
   `SERVICE_ACCOUNT_NAME` already injected as pod env); anything post-release; resources and JVM heap
   (the keos-apps size overlays own them); deliberate pre-existing chart-vs-descriptor differences;
   litellm (PLT-4838); `bin/migration`; the universe ZIP (never committed).

CHANGELOG lines go at the **top** of the existing `## 15.1.0 (upcoming)` list (newest first, as the
file does; do not open a 15.1.1 section; do not touch `VERSION`). Corrections found by the audits: README §4 is stale (`connectors/agent-*.json` feed
`dg-agent`; `connectors-management.json` feeds `connectors`; `data-marketplace/` feeds
governance-datamarket and its agent; rocket has 4 descriptors, `mlmodel-server.json` was removed
before the release); `datamarket-agent/` in the charts repo is an untracked, git-ignored leftover
(the chart is `governance-datamarket-agent`).

## Per-chart plans (what to do, in words)
Line numbers are from 2026-10-02: re-check before editing.

### rocket (largest)
The chart's four images are still the 4.0.x line; the release is 4.1.0 for all four. Bump in
`rocket/values.yaml`: rocket-api tag (~594) `4.0.1` to `4.1.0`; ML prediction server image (~1741),
rocket-driver (~1904), rocket-executor (~1905) `4.0.0` to `4.1.0`. `ROCKET_VERSION` derives from the api tag.

Second commit: track the live-only variables that exist in the release descriptors (all introduced
before the release; the chart never tracked them). Start as literals, then `charts values` /
`charts environment` (workflow B), translating any `${...}` markers (none expected). Shared ones go in
`config/rocket_common_env_vars.yaml` (prefix `rocketCommon`), server-only in
`rocket_server_env_vars.yaml` (`rocketServer`), catalog-flavor ones in `rocket_catalog_env_vars.yaml`
(`rocketCatalog`). Copy each default **from the `54ab1918` descriptor** (the audit read the tip):
`CUSTOM_LINEAGE_AND_QRS_FILE_SYSTEM_METHODS`; `SPARTA_CONFIG_WORKFLOW_BLOCK_SENSITIVE_ACCESS_IN_CODE_STEPS`
(true); `SPARTA_CONFIG_WORKFLOW_DEPLOY_VALIDATION_ENABLED` (false); `SPARTA_TIMEOUT_WORKFLOW_RUN` (300);
`SPARTA_WORKFLOW_SCHEDULER_RECONCILIATION_GOVERNANCE_SYNC_MAX_WAIT` (5 minutes);
`GOVERNANCE_AVAILABILITY_{DETECTION_ENABLED,FAILURE_THRESHOLD,PROBE_INTERVAL}` (true/3/30);
`GENAI_CHAIN_GOVERNANCE_GLOSSARY_CACHE_REFRESH_INTERVAL_SECONDS` (90) and
`GENAI_CHAIN_SQL_CHAT_HISTORY_TOKEN_LIMIT` (20000); the four `…cde_quality_blocking_{enabled,allow_expansion}`
pairs of the SQL and generic-SQL chains (the generic pair is not in the catalog descriptor); the six
catalog cache/JDBC-pool/validator settings (`…CACHE_DEFAULT_IDENTITY_REFRESH_INTERVAL` 1h,
`…MIN_AGE_FOR_INVALIDATION` 10m, `…JDBC_POOL_ENABLED` true, `…JDBC_POOL_IDLE_TIMEOUT` 30m,
`SPARTA_CONFIG_VALIDATOR_CACHED_TABLES_ASK_TIMEOUT` 5s, `SPARTA_CONFIG_VALIDATOR_SKIP_PREDECESSOR_CHECK` false).

Leave alone: `CUSTOMER_SIZE`, `MIGRATION_STORAGE_PROFILE`, `NOT_USED`, and the nine `*_PROBE_*` env vars
(the chart hard-codes probes; the catalog descriptor marks them application-level);
`ROCKET_POD_NAME` (already injected as a pod env for the server type) and `SSCC_S3A_GLOBAL_CREDENTIALS`
(already in the S3 storage env file) are **not** gaps. Do not apply any 4.1.1 parameter or default
(Pekko timeouts, 75% RAM, startup threshold 360, shortened JDBC options, emptied `apiMaxFileSize*`):
post-release. Resources and termination grace period: report only. Coordinate with
`docs/TASK-keos-apps-env-list-overlays.md`.
Verify: `charts validate` per env file; `check_descriptors.sh rocket` with the flags of
`rocket/helm-template-examples.md` (hdfs and s3 storage, server and catalog types);
`flux stratio apps diff rocket --baseline latest`: tags equal live 4.1.0 and the "not rendered" list
shrinks from 34 to the CCT artefacts above.

### genai (flavors api, developer-proxy, ui)
The chart still carries the pre-release hash tags from #237; the release uses final tags. In
`genai/values.yaml`: api (~358) `0.8.0-6dd7bb1` to `0.8.0`; developer-proxy (~482) to `0.8.0`; ui (~425)
`0.9.0-42a78fd` to `0.9.0`; sandbox `AGENT_IMAGE` default (~958) `0.2.0-19873e8` to `0.2.0`. Bumping the
ui tag also changes `GENAI_UI_VERSION` and the governance registration payload in
`genai_prerequisites.yaml` (intended). **Before editing**, check that the final tags are the same
builds as the hash tags (registry digest or ask GenAI); if not, the hash tag may be deliberate.

Track six genai-ui parameters (all in the release descriptor since January): under
`genaiUi.settings.chainGovernance` (~1064) `GENAI_GOVERNANCE_CHAT_BUSINESS_TERM_PROPOSAL_REVERSE_RELATIONS`
(false), `…_KNOWLEDGE_PROPOSAL_DOMAIN` (`Knowledge Proposals`), `…_SKIP_FK_DETECTION_WITH_VIRTUAL_FKS`
(true), `…_TARGET_STATUS_FOR_PROPOSALS` (`Draft`); under `genaiUi.settings.chainChat` (~1041)
`GOVERNANCE_GLOSSARY_CACHE_REFRESH_INTERVAL_SECONDS` (90) and `GOVERNANCE_PAGE_SIZE` (300). Add them
alphabetically to `genai_ui_env_vars.yaml` and remove the stale `BOOTSTRAP_GLOSSARY_ITEMS` (the
descriptor dropped it in January). Leave alone: `genai-api/NOT_USED`, chart-only `GOVERNANCE_URL`, the
chart's `INGRESS_PROXY_TIMEOUT` 600 override, api resources, the developer-proxy load-balancer
parameters (no such Service in the chart). Do not add the ROCK-15325 GoSec-group task or 0.8.1/0.3.1
images (post-release). litellm untouched.
Verify: validate the three env files; `check_descriptors.sh genai`; `apps diff genai --baseline latest`
(7 live-only to only `NOT_USED`).

### eureka-agent
Image `1.0.1` to `1.1.0` (`values.yaml:~186`; `BDL_AGENT_VERSION` follows). Add the one live-only
parameter `BDL_GOVERNANCE_ONTOLOGY_DROP_OPERATIONAL_COLLECTION` (boolean, false; in the release
descriptor `bdl-default`, "Virtualization Agent"): an env-file line (alphabetical, near line ~66) wired
to a new `eureka.settings.publisherSettings.bdlGovernanceOntologyDropOperationalCollections: false`
(~511). Leave the `FETCH_ENABLED`, `VIRTUAL_HOST`, `GOVERNANCE_DEPLOYMENT` divergences and resources.
Verify: validate, `check_descriptors.sh eureka-agent`, `apps diff eureka-agent --baseline latest`.

### dg-agent (also dg-hdfs-agent and the other 22 agent types)
Required change: image `dg-custom-agent_2.12` `1.1.1` to `1.2.0` (`values.yaml:~195`); one edit covers
every agent type. Leave `1.2.1-M1/M2` (post-release). Done in the same PR (tool-reported live-only variables on dg-hdfs-agent): `AGENT_{GOVERNANCE,HADOOP,HTTP,SSCC}_LOG_LEVEL`,
`COMM_DS_PASSWORD` (common), `DFS_TEXT_FOLDER_ENGINE` (dfs), hdfs `CUSTOM_SERVICE_SSCC_DRIVER_LOCATION`. Still optional
(pre-existing gaps present in the release): sasgrid SSH host-key parameters, and a probable copy/paste bug (`SALESFORCE_PROXY_SECRET` inside the snowflake env file:
verify before touching). Verify: `helm template` with `agent.type=hdfs` and two other types;
`check_descriptors.sh` may not fit this layout (env files under `config/connectors/`); `apps diff`
needs a flux-stratio catalog entry first (separate task).

### spark-history
`stratio-spark` `3.5.7-1.0.2` to `3.5.8-1.0.2` (`spark-history/values.yaml:~73`): the release descriptor
has had 3.5.8-1.0.2 since universe PR #4035 (2026-07-24) and the chart missed it. `3.5.9-1.0.0-M3` is
post-release: do not use it. Nothing else changes. Verify: `helm template` + `check_descriptors.sh spark-history`.

### virtualizer
Images already equal the release (1.1.0, monitor/ui 0.6.0, spark executor 3.5.8-1.0.2). One live-only
variable is a chart gap: `CONF_KV_spark_sql_warehouse_dir` (parameter
`general.ghadoop.virtualizerStoragePath`, default `/data`; added under
`virtualizerServer.general.ghadoop` and in `virtualizer_common_env_vars.yaml`). `EXTRA_JARS`,
`EXTRA_JARS_SPARK_CLASSPATH` and `EXTRA_JARS_SPARK_EXECUTOR_CLASSPATH` are **not** gaps to track: in
the descriptor they are lifecycle `parameter/upsert` tasks computed from CCT connector state (live:
`,` for the executor classpath, and the JDBC/storage connector URLs for `EXTRA_JARS`), not
parameters; the chart wires the storage connector per flavor already. Leave `VR_DEPLOYMENT`/`VR_SERVICE`, the duplicate `identitiesDaasHostPort` parameter
(valid at the release; its removal is post-release), resources and probes.
Verify: validate each env file; `check_descriptors.sh virtualizer` per storage type; `apps diff virtualizer --baseline latest`.

### intelligence
Aligned (5.1.1). No change required. `analytic-environment` (release 5.1.0) is not pinned in the
chart: check on eosdev how the spawned analytic image is resolved and report. `NOT_USED` is a
placeholder. Optional: track `CUSTOM_PVC_INSTANCE_ANNOTATION`.

### dlc-entity
Image aligned (4.0.3). Cleanup (done: the live eosdev deployment sets none of them): remove five stale CDC-Engine literals from
`dlc-entity/config/dlc-entity_env_vars.yaml` (lines 1-4 and 10: `CDC_ENGINE_BASIC_AUTH_ENABLED`,
`CDC_ENGINE_SERVICE_ENABLED`, `CDC_ENGINE_SERVICE_NAME`, `CDC_ENGINE_URL`,
`DLC_MAX_RETRIES_ON_CDC_ENGINE_FAILURE`; the universe removed CDC support in 2025 and `charts analyze`
reports them as removed), after confirming the container does not read them. `DLC_RETRIES_ONLY_ON_REGEX`
differs from the descriptor in escaping: verify against the live ConfigMap, do not change blindly.

### bdl-datarest
All seven flavors match; image `0.8.2` aligned. README §7.1's mismatch does not exist in the chart
(shared files are referenced correctly through `configMapsFromFile`). The failure is in
`bin/check_descriptors.sh` (~lines 75-84): it treats `pgcommon_env_vars.yaml` and
`oraclecommon_env_vars.yaml` as flavors and fails with "bdl-datarest-oraclecommon_env_vars.yaml does not
exist". Report it and correct README §7.1; fix the script only if trivially safe (skip files lacking the
`<chart>-` prefix when `--type-key` is set; search-engine has legitimately unprefixed flavor files).

### No change required (state the evidence in the report)
governance (1.17.2 / ontology 0.13.1 match); governance-datamarket (api 2.4.0, ui 2.4.1; the 119
"untracked" opendata-overlay variables are by design); governance-datamarket-agent (3.3.1, 43/43);
opendata (api 2.4.0, ui 2.4.1, open-fs 0.2.2; `uiLinkToLogin` keeps the chart's own value);
connectors (loader 2.2.0; Recreate already renders; the 4Gi volume does not apply to an emptyDir);
discovery (`0.56.4-0.2.0`, 57/57); search-engine (2.7.3; only flag the searcher's 1024m heap in a 768Mi
pod as a pre-existing resource risk); gosec-agent (3.0.5, integrated by #252); litellm (out of scope;
chart `0.3.0-b7465f3` vs release `0.3.0`: report to the PLT-4838 owners).

### One branch, one PR (decided 2026-10-02)
All chart changes go in a **single worktree, a single branch and a single PR** against `Stratio/charts`
(no per-chart worktrees or PRs), as separate commits per chart and concern: rocket (images, then
variables), genai (images, then parameters), eureka-agent, dg-agent, spark-history, virtualizer,
dlc-entity cleanup, README §7.1 note. The PR description carries: a CHANGELOG line under `## 15.1.0 (upcoming)` (e.g. `* [NOJIRA] Align rocket with
kubernetes-universe 15.1.0 release (rocket 4.1.0 images; track live-only variables)`), the pinned SHAs
(`54ab1918` / `7324e952`), the descriptor `data.version`, and before/after `apps diff` counts.

## Worktree and branch (mandatory working method)
`/stratio/charts/charts` (`master`) has someone's **uncommitted edits in `bin/migration/`**
(`CONTEXT.md`, `cmd_patch_chart.py`, `cmd_steps.py`, `config.json`): nobody edits, stashes, resets or
checks out anything there. All chart work happens in **one** git worktree for the repo, cut from the
remote tip, as the existing `/stratio/aws/flux/.worktrees/charts-plt-4838` does.
- Created 2026-10-02: branch `dgutierrez-stratio/align-15.1.0` (no upstream set), worktree
  `/stratio/aws/flux/.worktrees/charts-align-15.1.0`, base `origin/master` = `a9acf1c`:
  `git -C /stratio/charts/charts worktree add -b dgutierrez-stratio/align-15.1.0 /stratio/aws/flux/.worktrees/charts-align-15.1.0 origin/master`.
  Never use the litellm branch or master's dirty tree as base.
- The Python venv exists only in the master checkout: run the worktree's own committed
  `bin/migration/migrate.py` with `/stratio/charts/charts/bin/migration/.venv/bin/python`, so master's
  dirty `bin/migration` is never used. Build the universe ZIP once (`git -C /stratio/p/kubernetes-universe
  archive 54ab1918 packages/`) outside every worktree; never commit it. Run `/charts:upgrade` and
  `/charts:compare-descriptors` with the worktree as working directory.
- One commit per chart and concern. Commit message
  `[NOJIRA] Align <chart> with kubernetes-universe 15.1.0 release (<what>)`. Rebase onto
  `origin/master` before opening the PR; CHANGELOG lines are at the top of the 15.1.0 list.
- Do not push or open a PR without the user's go-ahead. Evidence runs against eosdev must render the
  **worktree's** chart (point the plugin's charts path at it), not master's.
- After the PR merges: `git worktree remove` it and delete the local branch. Leave the pre-existing
  worktrees (`charts-plt-4838`, the prunable `plt-4394-virtualizer-15.1`) untouched.
- Rendering in the worktree needs `helm dependency build <chart>` (the `charts/` and `Chart.lock`
  outputs are git-ignored). `bin/check_descriptors.sh` is unreliable for charts with Vault agent
  annotations (their legitimate `{{ ... }}` trips its "unrendered" check, flakily because of `pipefail`):
  compare `helm template` of the previous chart (`git archive HEAD <chart> common`) with the edited one
  instead, using the examples in `<chart>/helm-template-examples.md` (run them under bash, quoting
  the `--set` values).

## Post-release deltas, not applied (belong to a future 15.1.1 alignment)
Report these in the appendix of the report; never apply them under this task. Tip at the time of the
audit: `f14a1355` (2026-10-02).
- rocket `4.1.1` (api `4.1.1-9bfc85e`, others `4.1.1-98520b7`) with ~28 parameter additions and default
  changes (Pekko timeouts, 75% RAM percentage, startup probe 360, worker probe thresholds,
  content-length limits, cluster-membership health check, upgrade strategy, deprecated
  `apiMaxFileSize*`, JDBC options).
- governance 1.17.3 / ontology 0.13.2 / `IMPORT_TIMEOUT` 600000 / `QUALITY_RULE_NAME_MAX_LENGTH`;
  datamarket api 2.4.1 / ui 2.4.2; opendata api 2.4.1 / ui 2.4.2; connectors 2.2.1.
- genai `0.8.1-SNAPSHOT` + sandbox `0.3.1-SNAPSHOT` + the ROCK-15325 GoSec-group lifecycle task;
  dlc-entity `4.0.4-SNAPSHOT`; intelligence `5.1.2-SNAPSHOT` / analytic `5.1.1-SNAPSHOT`.
- virtualizer `1.1.1-SNAPSHOT` / monitor+ui `0.6.1-SNAPSHOT` / spark `3.5.9-1.0.0-M3` / daas parameter dedupe.
- dg-custom-agent `1.2.1-M1/M2`.

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
Their classification (descriptor-backed vs CCT artefact) was done by the 2026-10-02 audits and is
folded into the per-chart plans above; step 5 remains to confirm each default against `54ab1918`.

## Outcome (2026-10-02): `Stratio/charts` PR #258
Reference used: the 15.1.0 release commit `54ab1918` of kubernetes-universe (identical content at
`7324e952`), final tags only. The PR (11 commits on top of `origin/master` `a9acf1c`, one per chart and
concern) contains:
- **rocket:** images to `4.1.0`; 20 live-only descriptor variables tracked (10 common, 10 server).
- **genai:** final tags (`0.8.0` api/proxy, `0.9.0` ui, sandbox `0.2.0`) instead of the hash tags; six genai-ui
  parameters added; stale `BOOTSTRAP_GLOSSARY_ITEMS` removed.
- **eureka-agent:** `1.1.0` + `BDL_GOVERNANCE_ONTOLOGY_DROP_OPERATIONAL_COLLECTION`.
- **dg-agent:** `dg-custom-agent` `1.2.0` (covers dg-hdfs-agent) + seven live-only variables
  (`AGENT_{GOVERNANCE,HADOOP,HTTP,SSCC}_LOG_LEVEL`, `COMM_DS_PASSWORD`, `DFS_TEXT_FOLDER_ENGINE`, hdfs
  `CUSTOM_SERVICE_SSCC_DRIVER_LOCATION`).
- **spark-history:** `stratio-spark` `3.5.8-1.0.2`. **virtualizer:** `CONF_KV_spark_sql_warehouse_dir`.
  **dlc-entity:** five stale CDC variables removed. **docs:** README §7.1 bdl-datarest note corrected.
- No change needed: governance, governance-datamarket(-agent), opendata, connectors, discovery,
  search-engine, gosec-agent, intelligence, bdl-datarest.

Verification (also posted as a comment on the PR): `charts analyze`/`validate` on the final env files,
`helm template` diffs of previous vs edited chart, and read-only `flux stratio apps diff <app> --baseline
latest` on eosdev. "Variables not rendered by the chart", before to after: rocket 34 to 14 (CCT artefacts
only), genai 7 to 1 (`NOT_USED`), virtualizer 4 to 3 (`EXTRA_JARS*`, lifecycle upserts), eureka-agent 1 to 0,
dg-hdfs-agent 7 to 0, dlc-entity unchanged. Rendered images equal live; the tenant file's patches that pinned
image tags (rocket, genai-ui, genai sandbox, eureka `bdlAgentVersion`) are now reported as redundant.

Working rule that applied (and applies to any follow-up): the output of the `charts` subcommands is the
highest-accuracy source. Variables, schema paths and defaults come from `charts analyze` / `values` /
`environment` / `validate`; manual edits are only acceptable when they coincide with the tool.

Left for whoever continues:
1. **Merge PR #258**, then rebase/clean up the worktree (`git worktree remove`, delete the local branch).
2. **Decision:** `COMM_DS_PASSWORD` was added with the descriptor default `stratio` (the live value too), a
   literal credential default; remove that line from the PR if it should not live in the chart.
3. **Confirm with the GenAI team** that the final genai tags are the same builds as the old hash tags (live
   eosdev already runs `genai-api:0.8.0`, `GENAI_UI_VERSION=0.9.0`, sandbox `0.2.0`; the universe commit
   `fd95c120` says "bump GenAI packages to release versions").
4. **Not done on purpose:** everything that landed after the release (15.1.1: rocket 4.1.1 and its ~28
   parameter changes, governance 1.17.3 and its parameters, datamarket/opendata 2.4.1/2.4.2, connectors
   2.2.1, SNAPSHOT/`-M` tags, ROCK-15325 GoSec-group task): see "Post-release deltas, not applied". Also not
   done: sasgrid SSH host-key parameters and the snowflake `SALESFORCE_PROXY_SECRET` copy/paste check in
   dg-agent; virtualizer `EXTRA_JARS*` (lifecycle upserts, not parameters); litellm (PLT-4838); resources.
5. **Observations outside this task:** genai-api `GOVERNANCE_URL` is set by the chart but absent on the live
   deployment; `bin/check_descriptors.sh` mis-reports charts with Vault agent annotations and treats
   bdl-datarest's shared `pgcommon`/`oraclecommon` files as flavors (not fixed; documented in the README).
6. Re-measure with `docs/TASK-chart-spec-comparison.md` once the PR is merged and the eosdev backups are
   refreshed.
