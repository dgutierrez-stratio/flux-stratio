# TASK-12 — product decisions left by the chart-variables investigation (TASK-11)

**Status: open, nothing decided. Written 2026-10-14 so the decisions can be studied and settled in another session.** Each decision is
independent; none blocks the others. No cluster change and no repo change is part of writing this file.

Origin: `TASK-11-chart-variables-vs-cct-descriptor.md` ("Investigation results" at its end has the evidence). Short version: the legacy
install and every chart come from the **same** 15.1.0 descriptor build (`54ab1918` is consistent with all of them), so the variables that
differ are not descriptor skew. They are legacy values that came from other channels (apps-layer-automation defaults, Command Center form
inputs, lifecycle upserts) or chart/keos-apps choices. For the ones below the migration principle (`TASK-00`) cannot answer by itself:
either side is defensible, so someone who owns the environment or the product has to pick.

## How to use this file

For each decision: read "What differs", check the "To verify" list (all read-only), pick an option, and record the choice and the reason
in the "Decision" line. The "Option leaning" is a recommendation from the investigation, **not** a decision. After deciding, the "If
chosen" column says what has to change and where; open a `TASK-NN` per change if it is more than a line or two.

Context for whoever picks this up (do not re-derive):

- eosdev, tenant `stratio`. Legacy values are in the pre-migration backups `~/.fluxcd/flux-stratio/backups/<app>/2026-10-13T11-00-17Z/`
  (never print secret values from them). genai, rocket and intelligence are held (`enabled: false`) until TASK-10's plugin fix lands.
- Charts: worktree `/stratio/aws/flux/.worktrees/charts-align-15.1.0` (branch `dgutierrez-stratio/align-15.1.0`). keos-apps:
  `/stratio/aws/flux/.worktrees/keos-apps-env-list-overlays`. Legacy deployment repo: `/stratio/aws/apps-layer-automation`. Descriptors:
  `git -C /stratio/p/kubernetes-universe show 54ab1918:packages/<...>` (never the branch tip).
- The migration patch must keep legacy live values even over GitOps overlay values (memory `migration-preserves-legacy`). A decision below
  that says "carry the legacy value" is therefore about the *tenant file and dependency graph*, not about whether the patch may drop it.
- Deliberate renames (`dg-` to `governance-`, Vault role hyphen to underscore) are not part of any decision here.

---

## D1 — genai: should the tenant file enable the `opensearch` and `discovery` dependencies?

**What differs.** genai-ui renders `OPENSEARCH_ENABLED=false`, `OPENSEARCH_HOST/PORT=""`, `DISCOVERY_ENABLED=false`,
`DISCOVERY_URL/INTERNAL_URL=""`; legacy eosdev had `true`, `opensearch1.stratio-datastores:9200`, `true`,
`https://discovery.stratio.k8s.eosdev.../discovery`, `discovery.stratio-apps`.

**Why (verified).** The descriptor default for `OPENSEARCH_ENABLED` is `false` (`genai-ui-default.json:948`, `application:false`); the
legacy `true` was an install input (`apps-layer-automation/apps/genai-ui/vars.env:31`, `GENAI_OPENSEARCH_ENABLED`). The chart turned the
variable into a dependency toggle: `genai/values.yaml:1054` = `include "common.dependencies.opensearch.enabled"`, fed by keos-apps
`components/genai/app/base/helm.yaml:50-53` (`dependencies.opensearch.enabled: ${OPENSEARCH_ENABLED}`) and the use-cases default
(`resourceset-apps-genai.yaml:308`, false). Discovery follows the same pattern (`helm.yaml:47-49`, `dependencies.discovery.*`; the descriptor
default is `true`, G:1920). The migration patch already carries the legacy values through `genaiUi.settings.chainChat.*` and
`genaiUi.settings.externalDashboards.*`, so the rendered ConfigMap ends up right without the toggle.

**Options.**
- A. Keep the dependencies off and rely on the patch values (current state).
- B. Enable `dependencies.opensearch.enabled` and `dependencies.discovery.enabled` in the tenant file, so the dependency graph says what is real.

**What B changes besides the two variables** (check each before choosing): `dependsOn` of genai (it would wait for opensearch and discovery);
the GosecPolicy `genai-api-governance-opensearch` and keos-apps `gosec.yaml:7` (`enable: ${OPENSEARCH_ENABLED}`); the discovery
postrequisites; the rendered OpenSearch host would be `<name>-coordinator.<ns>` (the common helper adds `-coordinator`), whereas legacy was
`opensearch1.stratio-datastores` with no suffix, so the patch value and the helper value would then disagree and the patch would still win.

**To verify.** Which OpenSearch Services genai-ui must reach (see D2: `opensearch1` in `stratio-datastores` and `keos-core`, `opensearcher` in
`keos-core`); whether `dependsOn` on discovery creates an ordering problem with the Metabase `DISCOVERY_DATABASE` id (legacy `3`, descriptor
default `1`, depends on database creation order); whether any consumer reads `dependencies.*.enabled` of genai besides the helpers.

**Option leaning.** None. A is the lower-risk one for a migrated cluster; B is closer to the intended GitOps design for a fresh install.

**Decision:** _(blank)_

## D2 — genai: which OpenSearch cluster holds the governance indices on eosdev?

**What differs.** `GOVERNANCE_OPENSEARCH_ENABLED` legacy `true` vs GitOps `false`; `GOVERNANCE_OPENSEARCH_SERVICE/URL` legacy
`opensearch1.keos-core` / `https://opensearch1.keos-core:9200` vs GitOps `opensearcher-coordinator.keos-core`.

**Why (verified).** Legacy was `true` only through the descriptor default (G:587): `vars.env:32` `GENAI_GOVERNANCE_OPENSEARCH_ENABLED` is dead,
the `.json.tmpl` never uses it. keos-apps flips it: `helm.yaml:76` `${GOVERNANCE_OPENSEARCH_ENABLED:=false}`, use-cases default false
(`resourceset-apps-genai.yaml:319`), and builds the host from the use-cases cluster name (`helm.yaml:77-78`, `searcher.osClusterName:
opensearcher` in keos-fleet `clusters/eosdev/default/resourcesetinputproviders.yaml`). **Both clusters exist on eosdev** (`kubectl get svc -A`
on 2026-10-14: `opensearch1*` and `opensearcher*` in `keos-core`; `opensearch1*` also in `stratio-datastores`). Chart quirk to keep in mind:
`governanceOpenSearchServiceName` (no suffix, used by the GosecPolicy) differs from `governanceOpenSearchHost` (with `-coordinator`, used by
the env).

**To verify.** Which cluster actually holds the governance indices the platform writes (`opensearcher` is described in keos-fleet as "the
real OpenSearch cluster CR"; `opensearch1` is the legacy one; `eosdev2` uses `osClusterName: opensearch1`); whether the legacy genai-ui
really queried governance OpenSearch (it was enabled by default); whether `opensearcher-coordinator` serves 9200 over TLS with the same
credentials/GosecPolicy.

**Options.** A. Accept GitOps (`opensearcher`, enabled per use-cases default). B. Preserve legacy (`opensearch1.keos-core`, enabled) through the
patch (it already carries it today). C. Make it enabled with `opensearcher` if that is where the indices are.

**Decision:** _(blank)_

## D3 — virtualizer-monitor: preserve `SERVER_MAXHTTPHEADERSIZE=65536`?

**What differs.** The legacy monitor pod has `SERVER_MAXHTTPHEADERSIZE=65536`; the GitOps chart renders nothing (and the plugin reports it
lost, TASK-04).

**Why (verified).** It is in no descriptor: `virtualizer-monitor.json` has no such parameter. It comes from the legacy deployment repo,
`apps/virtualizer-monitor/virtualizer-monitor.json.tmpl:24-28` (`general.custom`, the generic `CONFIGURATION` parameter of `stratiobase:default`)
and again as a top-level key at `:74`. Introduced in the 14.8 bulk import (`9c9460a`), removed from virtualizer-api in `9b214d5` but kept for
the monitor, with no explanation in history. `virtualizer-monitor_env_vars.yaml` has no free-form env path.

**To verify.** Whether the monitor reads it: Spring Boot relaxed binding maps `SERVER_MAXHTTPHEADERSIZE` to `server.max-http-header-size`
(default 8 KB), but the monitor image was not available locally (`virtualizer-monitor` 0.6.0); ask the virtualizer team or inspect the image.
Whether the monitor sits behind oauth2 with large JWT/group cookies on eosdev (then 431/400 through the ingress is the risk of dropping it);
look in the monitor's logs for `431`/`Request header is too large` since migration.

**Options.** A. Preserve: add an extra-env value to the monitor in the chart (generic, like the env lists of other charts) and make the plugin
map legacy `general.custom` keys, or carry just this one. B. Drop it (accept new) after confirming the monitor does not need it. C. Raise the
default in the chart if the need is general rather than eosdev-specific.

**Option leaning.** A (preserve) until someone confirms B, because the cost of keeping a header limit is nil and dropping it can fail only
in production traffic.

**Decision:** _(blank)_

## D4 — rocket: `CUSTOMER_SIZE`, `MIGRATION_STORAGE_PROFILE` and upgrade-time startup probes

**What differs.** The dry run reported `CUSTOMER_SIZE=M` and `MIGRATION_STORAGE_PROFILE=medium` as lost; the chart renders neither. (The nine
`*_PROBE_*` variables are not part of this decision: the chart sets the same values as native probes. They are no longer reported as lost once
TASK-11's F4 is in.)

**Why (verified).** Both are real descriptor parameters, **not** application-level artefacts as `docs/TASK-charts-universe-alignment.md` said:
`rocketbase.json:891-902` and `:912-923`, `application:false`. `CUSTOMER_SIZE` is used only by `lifecycles.upgrade/pre`
(`rocketbase.json:9855-9900`), which upserts `STARTUP_PROBE_FAILURE_THRESHOLD` to 120/240/480/720 for S/M/L/XL, so a large DB migration gets a
longer startup budget. The chart sets a fixed native `startupProbe.failureThreshold: 120` (`rocket/values.yaml:654-667`, period 5 s, a 10-minute
budget) and has no size-based escalation. `apps-layer-automation/apps/rocket/pre-upgrade.sh:67` sets 1000 during an upgrade when
pause-executions is on and `post-upgrade.sh` reverts it. `MIGRATION_STORAGE_PROFILE` has a description about estimating DB-migration downtime
and no lifecycle references it. Chart `terminationGracePeriodSeconds` is 30 for startup and liveness while the legacy probes had 60 (not part
of the variable list, found on the way).

**To verify.** Ask the rocket team: does the server read `MIGRATION_STORAGE_PROFILE`? Does a rocket upgrade on a large database exceed the 10
minutes startup budget on any real client? Does GitOps rocket have a pre-upgrade hook equivalent to `pre-upgrade.sh`?

**Options.** A. Accept the chart as is (installs fine; upgrades rely on the fixed budget). B. Expose the startup probe threshold in the tenant
size overlays (keos-apps) so a bigger tenant gets a bigger budget; this is the GitOps way to express `CUSTOMER_SIZE`. C. Add the two
variables to the chart if the server reads them.

**Decision:** _(blank)_

## D5 — genai: keep the legacy host and path, or move to the admin host?

**What differs.** `VIRTUAL_HOST` legacy `genai.stratio.k8s.eosdev.labs.stratio.com` vs `admin.k8s.eosdev...`; `HA_PROXY_PATH` legacy `/genai-ui` vs
`/genai-ui.stratio-genai`; `SERVICE_URL` follows both (and had a double slash, fixed by TASK-11 F2).

**Why (verified).** The descriptor default for the host is `genai.${tenantId}.${external_domain}` (G:185) and for the path `/${Application.name}`
(G:200). The legacy install used apps-layer-automation's `vars.env:24-25` (`${CLUSTER_NAME}.${EXT_DOMAIN}` and `/${APP_NAME}.${APP_NAMESPACE}`)
with eosdev overrides. The chart copies the apps-layer-automation shape: `haProxyPath: /<release>-ui.<ns>` (`genai/values.yaml:1022`) and
`haProxyHost: global.cluster.url`, i.e. every GitOps app is served on the `admin` host under a path.

**To verify.** Whether anything outside the cluster bookmarks or links `genai.stratio...` (SSO redirect URIs registered in the IdP, the Stratio
panel menu, documentation, customers' firewalls/DNS); whether the oauth2 proxy / ingress of the GitOps stack serves a dedicated genai host at
all; what other migrated apps did (they all moved to the admin host).

**Options.** A. Accept new: admin host and `/genai-ui.stratio-genai`. B. Preserve legacy through the patch (it can carry
`general.networking.haProxyHost` and `haProxyPath`); then the ingress and the IdP redirect URIs must also support that host.

**Decision:** _(blank)_

## D6 — virtualizer on eosdev: set the decoupled connector jars or not?

**What differs.** Legacy virtualizer had `EXTRA_JARS=,,,,<sscc-hdfs jar url>,<sscc-postgresql jar url>,`; GitOps renders none.

**Why (verified).** `EXTRA_JARS` is not a descriptor parameter: it is a `model/parameter/upsert` in `virtualizer-default.json`
`lifecycles.install.start[26]` (also `update.start[16]`, `upgrade.start[16]`). Its decoupled-connector part was filled by
`apps-layer-automation/apps/virtualizer/pre-install.sh:30-66` only when `VIRTUALIZER_DECOUPLED_CONNECTOR_{POSTGRES,HDFS}=true`
(`vars.env:24-25`, default false; eosdev opted in). The chart side is done and published: charts commit `feb37d7`
(`virtualizer.server.extraJars`, `dependencies.connectors.config.{hdfs,postgresql}.connectorJar`) is in the `15.1.0-PR258-SNAPSHOT` that eosdev
runs (checked 2026-10-14: same sha256 as the deployed artifact). eosdev renders no `EXTRA_JARS` because the tenant file does not set those jars.
The legacy URLs point at `connectors-management`, which is still running in `stratio-apps`; GitOps has the new `connectors` service
(busybox httpd serving jars; the `connectors` chart is not a port of the legacy REST app).

**To verify.** Whether the eosdev virtualizer really uses the HDFS and PostgreSQL SSCC connectors (queries against HDFS/PostgreSQL datastores);
which jar names the new `connectors` service serves (`dg-hdfs-agent` already uses `sscc-hdfs-0.4_2.12-1.1.1-922b277.jar` from it). The
analysis, with the solution recommended, is in `TASK-virtualizer-decoupled-connector-jars.md`; read it first.

**Options.** A. Set `dependencies.connectors.config.{hdfs,postgresql}.connectorJar` in the tenant file so `EXTRA_JARS` is rendered, then confirm
virtualizer loads them. B. Accept running without them if nothing uses the decoupled connectors.

**Decision:** _(blank)_

---

## Smaller open points (no decision needed, only a check)

- **Live Discovery database id.** genai-ui `DISCOVERY_DATABASE` is legacy `3`, descriptor default `1`; the chart value is patchable
  (`dependencies.discovery.database`). The plugin catalog excludes it (`internal/config/seed.go:319`, commit `b6afcc0`, no recorded reason) so the
  patch drops `3`. TASK-11's F3 (drop the exclude) was **deliberately not done**: the decision was to leave the exclude. Anyone migrating genai
  should check the live Discovery database id (Metabase) by hand and set it if it is not `1`.
- **`CUSTOM_SERVICE_SSCC_DRIVER_LOCATION` (dg-hdfs-agent).** The patch carries the legacy `connectors-management` URL (chart default `""`, path
  `agent.config.hdfs.customSSCCDriverJar`). Unknown whether the agent reads it at all (the agent log never mentions it, TASK-03 section C).
  Revisit when `connectors-management` is retired.
- **genai-api `chainDevelopment` parameters** (`exposeMtlsPortInLB`, `loadBalancerK8sServiceName`, `extraSANToAddToCertificate`,
  `exposeInsecurePyPiPort`) have no counterpart in the genai chart; they are application-level parameters, not env vars.

## Acceptance criteria

- Each of D1-D6 has a recorded decision (or an explicit "defer until X") and, where the decision changes something, a `TASK-NN` or a commit
  reference in the "Decision" line.
- Nothing in a chart, keos-apps or the plugin changes without its decision being recorded here first.
- `TASK-00` index row for this task is updated to "decided" when D1-D6 are closed.

## Related

`TASK-11` (evidence and the fixes already applied: F1 dg-agent `customServiceName`, F2 genai `serviceUrl` slash, F4 native-probe reporting),
`TASK-00` (migration principle), `TASK-04` and `TASK-virtualizer-decoupled-connector-jars.md` (D3, D6), `TASK-03` (dg-hdfs-agent),
`TASK-10` (genai ConfigMap null false positive; blocks verifying D1, D2, D5 on a real genai migration), `docs/TASK-charts-universe-alignment.md`.
