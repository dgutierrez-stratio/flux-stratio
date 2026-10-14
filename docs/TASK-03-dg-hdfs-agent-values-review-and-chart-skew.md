# TASK-03 — dg-hdfs-agent: review every value the migration changes, and make `doctor` see chart skew

**Status: open. Charts-side credentials fix pushed (commit `0a1e649`); the rest is investigation + plugin work.**
Found in the eosdev batch-migration audit of 2026-10-09 (see `TASK-00-eosdev-audit-index.md`).
Plugin to change: `flux-stratio` (this repo, branch `feat/application-migration`). Charts repo:
`Stratio/charts`, branch `dgutierrez-stratio/align-15.1.0` (local worktree
`/stratio/aws/flux/.worktrees/charts-align-15.1.0`).

## Requirement for this task (and for every task in the index)

Read "Migration principle" in `TASK-00`. For **each** variable and setting below the implementer must
investigate whether the GitOps value makes sense in the new GitOps-with-charts scenario **and why**,
and record the conclusion with evidence in this file *before* changing any tool. Classify each as
**preserve legacy**, **accept new** (neutral or positive change) or **needs a product decision**.
Do not assume either direction: the aim is to keep each client's original configuration as close as
possible, while welcoming changes that are genuine improvements. The audit found a long list of
differences; the list is a starting point for investigation, not a list of bugs.

## What happened

`apps migrate dg-hdfs-agent` (Governance agent for HDFS, chart `dg-agent`, namespace `stratio-datastores`)
reported, before the user applied it:

```
[REVIEW] 11 variable(s) differ and can't be written to the patch
    The patch leaves them as GitOps renders them. If the live value is the right one, set it by hand.
```

After Flux reconciled the HelmRelease (10:55Z, chart `dg-agent@15.1.0-PR258-SNAPSHOT` from HelmRepository
`helm-universe`), the live agent runs with the GitOps values. Separately, the default diff shows one more
difference the migrate output never listed: `CUSTOM_STRATIO_CREDENTIALS` is `hdfs-external-secret` live
but blank in the legacy backup and in the plugin's own render.

Pre-migration backup: `~/.fluxcd/flux-stratio/backups/dg-hdfs-agent/2026-10-09T10-51-40Z`
(the later `10-53-57Z` capture, taken by the prepare step, is equivalent).

## Audit facts (collected read-only by a subagent; verify what you rely on)

### A. CUSTOM_STRATIO_CREDENTIALS (chart snapshot older than the fix)

- Flux deploys `dg-agent` `15.1.0-PR258-SNAPSHOT` from `helm-universe` (`http://qa.int.stratio.com/repository/helm`),
  artifact digest `sha256:f8077f19…`. The plugin renders from the local worktree instead
  (`~/.fluxcd/flux-stratio/environment.yaml` `repos.charts`), which at the time held an uncommitted fix.
- Deployed chart: `dg-agent/values.yaml:642` default `hdfs-external-secret` and
  `config/connectors/dg-agent_hdfs_env_vars.yaml:10` `| default "hdfs-secret"`. `helm template` of both
  charts with the HelmRelease values: `common-config` blank in both; `hdfs-config` is
  `hdfs-external-secret` (deployed) vs blank (worktree). `hdfs-config` is loaded last by the Deployment's
  `envFrom`, so it wins.
- Decision (`docs/TASK-dg-hdfs-agent-custom-stratio-credentials.md`, B1): blank default in the chart,
  as in the 15.1.0 descriptors. **Done in charts: commit `0a1e649` pushed** (hdfs and postgresql
  defaults, CHANGELOG line).
- Runtime effect today: the agent logs a non-fatal ERROR at 10:56:15 retrieving
  `passwords/dg-hdfs-agent.stratio-datastores/hdfs-external-secret` from Vault (secret does not exist).
- **Still to do (investigate who owns it):** the pushed commit is not a published chart. The HelmRelease
  uses `reconcileStrategy: ChartVersion`, so a republish needs a version bump. Find out whether the charts
  CI publishes a new `PR258-SNAPSHOT` (or another version) from this branch, how that reaches
  `helm-universe`, and how eosdev should pick it up (`flux reconcile hr dg-hdfs-agent -n stratio-datastores
  --with-source`, or a version change in the tenant file / ResourceSet). Do not run it; document it.

### B. The 11 values listed as "can't be written to the patch"

For each: legacy value, GitOps value, what the audit saw, and what the implementer must investigate.
"Why" strings are the plugin's own reason column (`apps diff dg-hdfs-agent --baseline <backup> -v`).

| Variable | Legacy | GitOps | Plugin reason | Audit evidence | Investigate (and conclude) |
|---|---|---|---|---|---|
| `COMM_SERVICE_NAME` | `hdfs1.stratio-datastores` | `dg-hdfs-agent` | follows the release's name or namespace | chart hard-codes `{{ .Release.Name }}` (`dg-agent_common_env_vars.yaml:38`), no `.Values` path can change it. governance-discovery-manager logs: 1384 of 1384 requests as `datastoreName=dg-hdfs-agent`; request #9 is this agent's first. GoSec policy and the HDFS service are still named `hdfs1.stratio-datastores`. | **Highest priority.** What is this name used for (datastore identity in Governance discovery? GoSec policy/service key? lineage?). Does the new name create a second datastore in Governance and orphan assets catalogued under `hdfs1.stratio-datastores` for clients that already have them? Is a stable, release-independent name better in the new architecture? Not verified: Governance DB contents (needs DB access); ask the user whether eosdev Governance holds assets under the legacy name. Options if legacy must be kept: add a chart value (e.g. `agent.config.common.customServiceName`, default `{{ .Release.Name }}`) then patch it. |
| `CUSTOM_SERVICE_DS_DRIVER_LOCATION` | `http://connectors-management.stratio-apps/v1/api/artifact/Connector_sscc-hdfs_2.12_1.1.1-922b277.jar` | `http://connectors.stratio-apps/sscc-hdfs-0.4_2.12-1.1.1-922b277.jar` | built from `dependencies.connectors.config.hdfs.connectorJar` and `dependencies.connectors.(name,namespace)` | Agent fetched the new URL (108.7M) and logged "Driver successfully fetched!"; same commit `922b277`. `connectors` is a declared dependency (`stratio.yaml` lines ~456-458, becomes `dependsOn apps-connectors`); new `connectors` Deployment created 10:55:38Z; legacy `connectors-management` still running. | Is the new `connectors` service the intended successor of `connectors-management` in 15.1.0, and is the new URL scheme an improvement (path, versioning)? Does anything still need `connectors-management` for HDFS? Probably "accept new", but confirm against the descriptor/charts. |
| `COMM_DS_URL`, `COMM_DS_URL_LL` | `jdbc:postgresql://pgbouncer-postgreskeos-governance.keos-core/postgreskeos?prepareThreshold=0` | same with `:5432` | built from `dependencies.postgres.*`, `pgBouncer.*` | pgbouncer Service port is 5432; chart builds host from `common.dependencies.postgres.host/.port`; discovery request #9 went through this path. | Probably neutral (explicit default port). Confirm the legacy descriptor omitted the port only because 5432 is the default, and that no client overrides the port. |
| `DATASTORE_HOST` | `pgbouncer-postgreskeos-governance.keos-core` | `…keos-core:5432` | same | same | Same as above; check if any consumer parses this as a bare host (a `host:port` value in a `*_HOST` variable can be wrong for some clients). |
| `DISCOVERY_BASE_URI`, `DISCOVERY_MANAGEMENT_BASE_URI`, `GOVERNANCE_BASE_URI`, `GOVERNANCE_DEPLOYMENT`, `GOVERNANCE_MANAGEMENT_BASE_URI` | `dg-discovery…`, `dg-businessglossary…` | `governance-discovery…`, `governance-businessglossary…` | fixed in the chart: no value sets it | Deliberate `dg-` to `governance-` rename (user memory `governance-rename-is-ours`). Agent requests to the new names return 200. | Confirm the new hostnames exist on every environment the chart targets, and that no client keeps the old service names. Expected conclusion: accept new. |
| `VAULT_ROLE` | `stratio-datastores-dg-hdfs-agent` | `stratio-datastores_dg-hdfs-agent` | follows the release's name or namespace | Deliberate (user memory `vault-role-naming`: prefer the GitOps approlename). Vault login with the new role succeeded. | Confirm the new role is created by the chart/SecretsIdentity on a fresh install and that the legacy role can be retired. Expected conclusion: accept new. |

### C. Other things found on this app

- **Not a defect (corrected during the audit):** the plugin wrote `agent.config.hdfs.customSSCCDriverJar:
  http://connectors-management…` into the patch. That key feeds a *different* variable,
  `CUSTOM_SERVICE_SSCC_DRIVER_LOCATION` (`dg-agent_hdfs_env_vars.yaml:9`), not
  `CUSTOM_SERVICE_DS_DRIVER_LOCATION`. Live matches the backup for it, so the plugin is right.
  Investigate whether the SSCC variable is used at runtime at all (the agent log never mentions it) and
  whether keeping a `connectors-management` URL there makes sense once that service is retired.
- **GosecPolicy in ERROR (chart bug):** `GosecPolicy dg-hdfs-agent.stratio-datastores-hdfs-acl` is in
  `ERROR` (Gosec 500; `gosec-services-daas` logs `NullPointerException: "aclsAllow" is null`). Cause:
  `dg-agent/values.yaml:113-117`, the `/user` ACL has only `actionsDeny` and no `actions`. It is the only
  GosecPolicy on the cluster in ERROR. Not in the pushed commit. Investigate what the legacy descriptor's
  ACL was (and which GoSec policy is effective for this agent today), then fix in the charts repo (e.g.
  `actions: []` or drop the deny-only entry).
- **Discovery has not succeeded yet:** the only discovery (#9, 10:56:26Z) failed with WARN "credentials
  may not read '/user'" and ERROR `refuseTheEmptyCatalogue … Can't open /`. The full cron runs at 00:00.
  Not verified whether the legacy agent behaved the same or whether the HDFS root only holds `/user`.
  Check after the next full cron.
- Health otherwise fine: pod `1/1 Running`, 0 restarts, Vault login with the new role OK, governance
  requests return 200.

## Plugin work (flux-stratio)

1. **`doctor` must see chart skew.** `internal/doctor/doctor.go` `checkRepoRevisions` (~lines 579-646,
   `dirs` map ~606-608) only compares git-sourced repositories (apps, use-cases, fleet, system-services).
   Charts reach the cluster through a HelmRepository with no git SHA, so it is never compared; the
   worktree was also dirty with `Chart.yaml` version `0.0.0`. Add a charts check: for each HelmRelease
   in the tenant's apps, compare the chart version Flux resolved with the worktree's `Chart.yaml` version
   and warn when they differ, when the worktree is dirty, or when the version is `0.0.0` (so the check
   cannot work). If an exact comparison is possible (render the deployed chart from the HelmChart artifact
   and the worktree's chart and compare the rendered env for the app), consider it as an opt-in
   `doctor --deep`; otherwise warn and say charts are not verified.
2. **Identity-changing values deserve more than a `[REVIEW]` row.** When a differing variable is
   hard-wired to the release name or namespace (reason "follows the release's name or namespace" —
   `COMM_SERVICE_NAME`, `VAULT_ROLE`), say what it is used for when the catalog knows, and make
   `apps migrate --yes` stop on it unless `--accept-warnings` is given. Decide with the investigation
   above which variables qualify (do not blanket-flag `VAULT_ROLE`, which is deliberate everywhere).
3. **Let the catalog record accepted differences** (investigate first): today an operator who accepts
   the 11 values still sees them as `[REVIEW]` on every `apps diff`/`migrate` run. Consider a catalog
   field listing variables whose GitOps value is accepted (with a `why` string), shown as a `[NOTE]`
   like the existing `exclude` entries. Check how `exclude` works (`docs/config-reference.md`) before
   adding a second mechanism; extending `exclude` may be enough.
4. If the investigation concludes `COMM_SERVICE_NAME` must be preserved: the plugin cannot patch a
   hard-wired value, so the chart change comes first; then make the plugin carry the new value path
   (see how `ChartDiff`/`MapDiffs` attribute a variable to a `.Values` path) and add a test.

## Acceptance criteria

- This file's table (B) and section C each end with a recorded conclusion per item (preserve / accept /
  decision needed), with the evidence that supports it. Items needing the user are listed at the end.
- `doctor` tests (use `runner.Fake` patterns in `internal/doctor`): chart version mismatch, dirty
  worktree and `0.0.0` produce warnings; matching versions pass; a missing HelmRepository chart is a Note,
  never a pass.
- The `--yes` behaviour for identity-changing variables is covered by a test in `internal/cli`.
- `CONTEXT.md`, `docs/config-reference.md` (if the catalog changes) and `CHANGELOG.md` updated;
  `go test ./...` passes.
- A republished chart and its rollout on eosdev are verified by `flux stratio apps diff dg-hdfs-agent`
  (default) showing no `CUSTOM_STRATIO_CREDENTIALS` difference and the ERROR gone from the agent log.

## How to reproduce

```
flux stratio apps diff dg-hdfs-agent                                     # CUSTOM_STRATIO_CREDENTIALS difference
flux stratio apps diff dg-hdfs-agent --baseline ~/.fluxcd/flux-stratio/backups/dg-hdfs-agent/2026-10-09T10-51-40Z -v
kubectl -n stratio-datastores get cm dg-hdfs-agent-hdfs-config -o yaml | grep CUSTOM_STRATIO
kubectl -n stratio-datastores logs deploy/dg-hdfs-agent | grep -i 'getpass\|hdfs-external-secret'
kubectl get hr dg-hdfs-agent -n stratio-datastores -o jsonpath='{.spec.chart.spec}'
helm template … # render the deployed chart (pull from helm-universe) and the worktree with the HelmRelease values
```

## Open questions for the user

- Does eosdev Governance already hold assets under datastore `hdfs1.stratio-datastores`? (Decides how
  serious the `COMM_SERVICE_NAME` change is.)
- Who publishes the next `dg-agent` snapshot to `helm-universe`, and when?

## Related

- `TASK-00` index (issue 08: the same doctor revision check, for `keos-system-services`).
- `docs/TASK-dg-hdfs-agent-custom-stratio-credentials.md` (the credentials decision).
- Audit artifact with all raw outputs: https://claude.ai/artifact/7TbG1ew9A6CGCMUDShFDx4
