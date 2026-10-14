# TASK — virtualizer chart drops the decoupled HDFS/PostgreSQL connector jars (`EXTRA_JARS`) that the legacy deployment carried

**Status: analysed, solution recommended, nothing implemented. Waiting for the user to confirm the design (see "Decisions to confirm").**
Split out of `TASK-04-virtualizer-lost-variables-and-annotation-fieldref.md` (row `EXTRA_JARS`), which stays the umbrella
for the other virtualizer differences. Found in the eosdev batch-migration audit of 2026-10-09 (`TASK-00-eosdev-audit-index.md`).
Written so that another agent can pick it up without the originating conversation. Every fact below was verified on
2026-10-13 unless it is listed under "Not verified".

Repositories and where things live:

| What | Path |
|---|---|
| Plugin (this repo, branch `feat/application-migration`) | `/stratio/gitops/flux-stratio` |
| Charts repo, worktree used by the plugin (branch `dgutierrez-stratio/align-15.1.0`) | `/stratio/aws/flux/.worktrees/charts-align-15.1.0` (virtualizer chart in `virtualizer/`, library chart in `common/`) |
| GitOps component definitions (`keos-apps`) | `/stratio/aws/flux/.worktrees/keos-apps-env-list-overlays/components/<app>/app/...` |
| Tenant file for eosdev | `/stratio/gitops/keos-fleet/clusters/eosdev/tenants/config/stratio.yaml` |
| Legacy deployment automation | `/stratio/aws/apps-layer-automation` |
| Legacy descriptors (universe) | `/stratio/p/kubernetes-universe/packages/virtualizer/virtualizer-default.json` |
| Pre-migration backup of the legacy Deployment | `~/.fluxcd/flux-stratio/backups/virtualizer/2026-10-09T10-51-40Z` (`deployment.yaml`, `env-vars.deployment.virtualizer.env`) |

## The problem in one paragraph

After `flux stratio apps migrate virtualizer`, the new virtualizer server no longer has `EXTRA_JARS`. The legacy
Deployment had `EXTRA_JARS=,,,,<sscc-hdfs 1.1.1 url>,<sscc-postgresql 1.6.1 url>,`, which makes the image
entrypoint download two decoupled connector jars onto the driver classpath at start-up. The `virtualizer` Helm chart
has no value that can produce them for an HDFS installation (it only sets `EXTRA_JARS` for the S3, ADLS and GCS
storage flavours), so the migration cannot carry them: the plugin reports them as "not rendered by the chart, lost after
migration" and a tenant-file patch cannot express them without copying a whole `env` list. This is a **chart gap
introduced while translating the legacy descriptor**, not a choice recorded anywhere. Queries that go through the
decoupled HDFS or PostgreSQL SSCC connectors can fail on a migrated environment that had them. The same two jars are
carried correctly for `eureka-agent`, which does have a chart knob.

## Evidence: what the legacy deployment did

1. **Descriptor** (`virtualizer-default.json`):
   - Lines 4920-4950: block `connectors.connectorJdbc` ("Enable integration with other datastores") with two parameters:
     `connectorJdbcEnabled` (boolean, `internalName CROSSDATA_SERVER_SPARK_DATABASE_ENABLE`, `application:false`) and
     `connectorJdbcDrivers` (string "Artifacts URL List", `internalName CONNECTOR_JDBC_DRIVER`, `application:true`,
     default `""`, visible only when the switch is on).
   - Line ~5308 (repeated at ~5753 and ~6224 for the other two flavours): the install lifecycle task
     `parameter/upsert` sets `EXTRA_JARS` to
     `${environment.artifacts.extensionsList},${connectors.connectorS3.envS3StorageArtifactURL},${connectors.connectorAzureGen2.envAdls2StorageArtifactURL},${connectors.connectorGCS.envGCSStorageArtifactURL},${connectors.connectorJdbc.connectorJdbcDrivers}`.
     That is five slots; with four empty ones and a two-URL JDBC list you get exactly the observed `,,,,<hdfs>,<postgresql>,`.
   - Lines ~5282-5298: `EXTRA_JARS_SPARK_CLASSPATH` = `sparkExtraClasspathList` + S3/ADLS2/GCS URLs + the external-HDFS plugin URL, and
     `EXTRA_JARS_SPARK_EXECUTOR_CLASSPATH` = `sparkExecutorExtraClasspathList` + the external-HDFS plugin URL. On eosdev all parts are empty
     (`,,,,` and `,`), so these two are a no-op. Nothing to carry for them.
2. **Who fills `connectorJdbcDrivers`**: `apps-layer-automation/apps/virtualizer/virtualizer.json.tmpl:158-160`
   (`"connectorJdbcEnabled": true, "connectorJdbcDrivers": "${CONNECTORS_EXTRA_JARS}"`).
   `apps/virtualizer/pre-install.sh:32-66` builds `CONNECTORS_EXTRA_JARS`: when `VIRTUALIZER_DECOUPLED_CONNECTOR_POSTGRES=true` it queries the
   connectors-management API (`findByName?name=sscc-postgresql`) and prepends
   `http://<connectors>.<tenant>-<suffix>/v1/api/artifact/<artifactURL>,`; the same for `VIRTUALIZER_DECOUPLED_CONNECTOR_HDFS` / `sscc-hdfs`.
   Both flags default to `false` (`apps/virtualizer/vars.env:24-25`) and are `true` in the `narnia` profile
   (`narnia/extensions.yaml:76-77`), the profile eosdev uses. The same pair of flags exists for rocket, rocket-catalog-standalone,
   eureka-agent and intelligence (`apps/*/vars.env`, `narnia/extensions.yaml`).
3. **Takeaway**: the jars belong to an opt-in "decoupled connectors" feature, independent of the storage type. eosdev is `storage.type: hdfs`
   and had both flags on.
4. **Backup**: `deployment.yaml:1443-1445` shows `EXTRA_JARS` as an inline `env` entry of the server container.
   The image is `qa.int.stratio.com:11443/stratio/virtualizer:1.1.0` in the backup and live: the container did not change.

## Evidence: what the chart does today

Chart `virtualizer` in the worktree (byte-identical to the deployed `15.1.0-PR258-SNAPSHOT`, checked earlier with `helm pull` + `diff -rq`).

- `virtualizer/config/storage/virtualizer_{s3,adls,gcs}_env_vars.yaml` set `EXTRA_JARS` and `EXTRA_JARS_SPARK_CLASSPATH` to the storage connector URL
  (through `virtualizer.dependencies.connectors.config.<s3|adls|gcs>.connectorJarUrl`, defined in `virtualizer/templates/datastores/_<x>.tpl`,
  each guarded by `server.storage.<x>.defaultConnector=false`).
- `virtualizer_hdfs_env_vars.yaml` has **no** `EXTRA_JARS` and no value feeds it.
- `virtualizer/values.yaml:1301-1305`: `virtualizerServer.connectors.connectorJdbc` only has `connectorJdbcEnabled: true`
  (rendered as `CROSSDATA_SERVER_SPARK_DATABASE_ENABLE` in `config/virtualizer_common_env_vars.yaml:125`). **`connectorJdbcDrivers` does not exist** in the chart.
- Env files reach the server through two ConfigMaps (`<release>-common-config`, `<release>-<storage.type>-config`) listed in `envFrom`
  (`virtualizer/values.yaml:~290`); the storage ConfigMap comes second so it wins on a duplicate key.
- `controllers.virtualizer.containers.virtualizer.env` is a **list** of six `VR_*` entries (`values.yaml:257-285`). A value override of a list replaces it
  (Helm merge rule). Verified with `helm template` (hdfs, `/tmp` scratch): a patch with only `EXTRA_JARS` renders `env = [EXTRA_JARS]` and drops
  `VR_ACCOUNT, VR_DEPLOYMENT, VR_IP, VR_NAMESPACE, VR_POD, VR_SERVICE`; a patch that copies the six entries plus `EXTRA_JARS` renders all seven.
- The `common` library chart already has the building blocks and they are fine: `common/templates/dependencies/connectors/_hdfs.tpl`,
  `_postgresql.tpl`, `_jdbc.tpl` define `common.dependencies.connectors.config.<x>.connectorJar` and `.connectorJarUrl`
  (`http://<connectors.name>.<connectors.namespace>/<connectorJar>`) and a `validate` helper. `dg-agent` uses them
  (`dg-agent/config/connectors/dg-agent_{hdfs,postgresql,jdbc}_env_vars.yaml` -> `CUSTOM_SERVICE_DS_DRIVER_LOCATION`).
  Virtualizer simply never calls the hdfs/postgresql ones. They have **no empty-value guard**: the caller must guard.
- Precedent in the same repo: `eureka-agent/values.yaml:490` exposes `eureka.settings.bdlAgentAdditionalJars` and the eosdev patch sets it to the same two jars
  (`stratio.yaml` ~line 581, still with the legacy `connectors-management` URLs). `discovery/values.yaml:435` exposes `discovery.settings.metabase.extrajars`.

## Evidence: git history (why the chart lacks it)

`git -C /stratio/aws/flux/.worktrees/charts-align-15.1.0 log` on the virtualizer chart and `common`:

| Commit | Date | What it did |
|---|---|---|
| `bf33c1e` PLT-1828 "Add virtualizer chart" | 2025-04-21 | First chart: `EXTRA_JARS: ',,,,<S3 connector url>'`, `EXTRA_JARS_SPARK_CLASSPATH: ',,,'`. Only the S3 storage connector was modelled; the JDBC-drivers slot was never ported. |
| `80f1c5e` PLT-2508 | 2025-09-24 | Adds `common` helpers `_hdfs.tpl`, `_postgresql.tpl`, `_jdbc.tpl` for the Connector Agents (dg-agent) chart. Not wired into virtualizer. |
| `194782e` PLT-2505/3161 | 2025-10-22 | Splits the env file by storage type (`virtualizer_{adls,gcs,hdfs,s3}_env_vars.yaml`). `EXTRA_JARS` goes to s3/adls/gcs only; hdfs gets none (correct for the *storage* connector, but it also drops the JDBC-drivers slot for HDFS installs). |
| `407fd64` PLT-3620.. | 2025-12-16 | Removes `EXTRA_JARS_SPARK_EXECUTOR_CLASSPATH` (harmless, value was empty). |
| `5fed353` PLT-3009 "Descriptor level parameterization" | 2026-03-24 | Parameterises the env files from the descriptor. `connectorJdbcEnabled` is ported as `CROSSDATA_SERVER_SPARK_DATABASE_ENABLE`. `connectorJdbcDrivers` has no env var of its own (it only feeds the `EXTRA_JARS` lifecycle upsert), so the file-driven port skipped it. |
| `bf2a108`, `efa7cc5`, `a1d280e` | 2026-04 | HDFS follow-ups (`CROSSDATA_STORAGE_PATH`, `CROSSDATA_SSCC_S3_DEFAULT=false`, quotes). None touches jars. |
| `1b5a224` | 2026-09-02 | "Fix jar reference for 15.1": new S3 jar name, and a whitespace fix in `_s3.tpl` (`{{ include` without `-` produced `EXTRA_JARS=" \nhttp://..."`, which silently broke the entrypoint download). Relevant lesson for the new helper. |
| (doc) `docs/TASK-charts-universe-alignment.md:315-318` | 2026-10-02 | Classified `EXTRA_JARS*` as "not gaps to track" because they are lifecycle `parameter/upsert` tasks and "the chart wires the storage connector per flavor already". True for S3/ADLS/GCS, wrong for the JDBC part. **Must be corrected.** |

Pickaxe (`git log --all -S`) for `connectorJdbcDrivers`, `sscc-postgresql`, `DECOUPLED` finds only the agent charts, never virtualizer. No commit message says the jars were dropped on purpose.
Conclusion: an omission while translating a lifecycle-computed parameter, not a decision. (Commit messages are terse, so intent cannot be proven either way.)

## Evidence: the new connectors service serves the same artifacts

eosdev, from the live virtualizer pod (`kubectl -n stratio-apps exec`, read-only GETs):

- Legacy service `connectors-management.stratio-apps/v1/api/artifact/Connector_sscc-hdfs_2.12_1.1.1-922b277.jar` -> 200 on GET (401 on HEAD, the endpoint only allows GET).
- New service `connectors.stratio-apps` (a busybox static server, chart `connectors`, 1 replica) serves
  `sscc-hdfs-0.4_2.12-1.1.1-922b277.jar` and `sscc-postgresql-0.4_2.12-1.6.1-a8eef0c.jar`.
- **md5 of each legacy file equals the md5 of its new counterpart** (`14b3811f1adb02c3e459e99f660d4059` hdfs, `16728ad3e49637716bf4672ab5a0496f` postgresql). Same artifact, new naming (`Connector_sscc-<x>_2.12_<ver>.jar` -> `sscc-<x>-0.4_2.12-<ver>.jar`).
- `sscc-s3-0.4_2.12-1.5.0.jar` -> 404 there (only to show the listing is jar-specific; not relevant to this task).
- The keos-apps `dg-agent` HDFS overlay already uses the new names:
  `components/dg-agent/app/overlays/hdfs/base/helm-patch.yaml` sets `dependencies.connectors.config.hdfs.connectorJar: "sscc-hdfs-0.4_2.12-1.1.1-922b277.jar"`.
- The `connectors-management` Deployment still exists on eosdev, so the legacy URLs still resolve today, but the new GitOps setup is meant to use `connectors`.

## Recommended solution

### Chart (`Stratio/charts`, `virtualizer`) — the main work

1. New values, all default empty, so installs that do not opt in render exactly what they render today:
   - `dependencies.connectors.config.hdfs.connectorJar` and `dependencies.connectors.config.postgresql.connectorJar` (same keys and `common` helpers as `dg-agent`);
   - `virtualizerServer.connectors.connectorJdbc.connectorJdbcDrivers` (string, free-form comma separated URL list; mirrors the descriptor parameter 1:1 and covers any other driver).
2. One helper, e.g. `virtualizer.server.extraJars`, composing in this order: storage-connector URL (existing s3/adls/gcs wrappers, unchanged guards), hdfs URL (if set), postgresql URL (if set), `connectorJdbcDrivers` (if set), joined by commas with no empty entries.
   Emit `EXTRA_JARS` from `config/virtualizer_common_env_vars.yaml` only and **remove it from the four storage files** (two ConfigMaps defining the same key make the result depend on `envFrom` order). Keep `EXTRA_JARS_SPARK_CLASSPATH` carrying only the storage connector, as in legacy (the jdbc jars were never in it).
3. Guard with `dig`/`if`: `.Values.dependencies.connectors.config` may be undefined (nil pointer) and the `common` URL helpers do not check for an empty jar name. Use `{{-` / `-}}` trimming (lesson of `1b5a224`).
4. Add the validation and an example to `virtualizer/README.md` / `helm-template-examples.md`, and a CI values case in `virtualizer/ci/`.
5. Verify with `helm template` for `type: hdfs`, `s3`, `adls`, `gcs`, each with and without the new values; the s3/adls/gcs output must be byte-identical to today when the new values are empty.

### Tenant file (eosdev) and keos-apps

- eosdev: add to the virtualizer HelmRelease patch `dependencies.connectors.config.hdfs.connectorJar: sscc-hdfs-0.4_2.12-1.1.1-922b277.jar` and
  `...postgresql.connectorJar: sscc-postgresql-0.4_2.12-1.6.1-a8eef0c.jar`. This keeps the legacy opt-in semantics (each environment chooses).
- keos-apps: recommend **not** enabling them by default in the virtualizer overlays: in legacy both flags were `false` unless an environment opted in
  (`dg-agent`'s HDFS overlay hardcodes the jar only because that agent requires it). Revisit if product wants the `narnia` profile as the default.

### Plugin (`flux-stratio`)

- When migrating a chart app whose legacy workload has `EXTRA_JARS` containing `sscc-hdfs` / `sscc-postgresql` URLs, write the structured values into the patch instead of
  reporting a loss. The jar names change between services (`Connector_sscc-hdfs_2.12_1.1.1-922b277.jar` -> `sscc-hdfs-0.4_2.12-1.1.1-922b277.jar`), so the mapping needs the
  connectors service listing or a catalog entry; do not guess the `0.4` part. Until then, report precisely and point to the value to set.
- `apps diff --baseline` will keep showing `EXTRA_JARS` as different (host and file names change). Either normalise known renames or document comparing the md5, as above.
- Do not list a variable whose legacy value is only commas as a loss (that part is TASK-04 plugin item 2).

### Docs

- Correct `docs/TASK-charts-universe-alignment.md:315-318`, update `TASK-04` row `EXTRA_JARS`, `CHANGELOG.md`, and `CONTEXT.md` / `docs/config-reference.md` if the catalog changes.

## Decisions to confirm with the user (before implementing)

1. Structured keys (`dependencies.connectors.config.{hdfs,postgresql}.connectorJar`) **plus** the free-form `connectorJdbcDrivers`, as recommended, or structured only?
2. Opt-in per tenant (recommended) or enabled by default in the keos-apps overlays?
3. Is an interim eosdev patch wanted before the chart ships? The only patch-only route copies the six `VR_*` entries plus `EXTRA_JARS`
   under `controllers.virtualizer.containers.virtualizer.env` (verified with `helm template`); it pins today's chart defaults and drifts when the chart changes, so it is not recommended unless something needs the jars now.

## Acceptance criteria

- `helm template` of the new chart for `type: hdfs` with the two eosdev jar values renders `EXTRA_JARS` containing both new-name URLs and keeps the six `VR_*` entries; with empty values the output is identical to today for all four storage types.
- After republish and rollout on eosdev: `kubectl -n stratio-apps get deploy virtualizer -o yaml` shows `EXTRA_JARS` (through the ConfigMap or env), the pod is Ready, and the jars download without errors in the server log.
- `apps diff virtualizer --baseline <backup>` no longer lists `EXTRA_JARS` as lost.
- The correction in `TASK-charts-universe-alignment.md` is made and `go test ./...` passes if the plugin changed.

## Not verified

- Whether anything on eosdev actually queries through these two connectors. The legacy flags were on, but usage is not visible; a missing jar only fails when a query needs it.
- How the image entrypoint treats empty entries in the list (the legacy value had four and worked; the S3 default flavour already renders `EXTRA_JARS: ''`). The entrypoint source (`/docker/lib/environments/context.sh:69`, cited in TASK-04) is not available locally.
- Whether rocket and intelligence charts have the same gap (they share the `*_DECOUPLED_CONNECTOR_*` flags; `eureka-agent` has the knob, the other two were not inspected).

## Related

- `TASK-04-virtualizer-lost-variables-and-annotation-fieldref.md` (umbrella; the monitor `SERVER_MAXHTTPHEADERSIZE` and the fieldRef false positive stay there).
- `TASK-03` (same kind of per-variable review), `TASK-keos-apps-env-list-overlays.md` (why a patch must not shadow chart `env` lists).
- Audit artifact: https://claude.ai/artifact/7TbG1ew9A6CGCMUDShFDx4
