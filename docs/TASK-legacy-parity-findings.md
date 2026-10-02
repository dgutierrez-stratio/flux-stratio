# Task: act on the eosdev legacy-vs-Go parity findings

**Status: open.** For a fresh session. Everything needed is below; do not re-run the investigation.

## Background

On 2026-10-01/02 the legacy Python client (`/stratio/charts/charts/bin/migration`) and this plugin
were each run through the full migration of the `stratio` tenant on the eosdev cluster, and the two
resulting tenant `ResourceSetInputProvider` files were compared in meld. Every difference was then
traced through both code bases, `helm template`, `flux stratio apps diff`, and the live cluster
(read-only).

**Result: no Go regression.** Go's output matches live eosdev everywhere it was checked (re-running
`apps diff rocket` reports "nothing to migrate"). Every difference is (a) a legacy bug, (b) a
component legacy never computes patches for (its `config.json` `applications` has 16 entries;
`dg-hdfs-agent`, `genai-litellm`, `opensearch1-dashboards` are not among them, `tenant import` only
scaffolds them), or (c) a serializer difference. What remains are a few gaps that exist in the Go
tool too. This task lists exactly those.

## Do NOT change (decided; settled by analysis and by the user's standing rules)

- Do not exclude `dlcEntity.security.mutualTlsCnWhitelist` or `genaiUi…translateLitellmModel`
  empty-string diffs. Live has them blank; the migration must keep live config even over the GitOps
  overlay (user rule: *patch must keep legacy live config*). `translateLitellmModel: ""` therefore
  intentionally overrides the tenant's `llmModels.translate`.
- Keep the genai approlename excludes (`seed.go:258-264`). Legacy wrote genai-ui's role into
  genai-api's path (flat by-name merge) which would 403 genai-api on Vault login.
- Keep `virtualizerServer.general.identity.approlename: stratio-apps-virtualizer` in the output: it
  equals live and is a known accepted exception (user rule: GitOps approlename preferred, legacy role
  only where migration otherwise breaks).
- Keep `maxPlainInt` (`internal/diff/coerce.go`): ints >= 1e6 stay strings because an int renders
  `1e+06` (`facadeCacheSize`).
- Keep the `config.agent.patches` anchor for gosec agents; it is what the keos-use-cases template
  reads (`resourceset-apps-datastores.yaml:223,586`).
- Keep rendering the base without the existing same-kind patch (whole-patch semantics).

## Findings that explain the differences (for the record; goes into CONTEXT.md §9, change 3)

| Legacy behaviour | Evidence | Go behaviour |
|---|---|---|
| Manifest mode renders without resolving `substituteFrom` (`cmd_patch.py:37`; only chart mode passes `resolve_substitute_from=True`, `cmd_patch_chart.py:55`), so `${KERBEROS_REALM}` rendered blank and always diffed: spurious hdfs `/spec/security/kerberos/realm` op | live and rendered are both `EOSDEV.INT` | `internal/render/substitute.go` resolves it; no op |
| Flat env-name→`.Values`-path map, last write wins (`cmd_patch_chart.py:150-182`), flat live merge (`:355`), first-wins rendered (`:411`): rocket's `rocketCatalog.*` (unmounted by the `rocket` pod → no-op), genai api/ui `SSO_HOST`/`INGRESS_PROXY_TIMEOUT`/`VIRTUAL_HOST` vanish, genai-ui role written into genai-api | rendering rocket shows `rocket` mounts only common/hdfs/rocket ConfigMaps | per-workload attribution (`AttributeConfigMaps`) |
| No inline-container-env detection: rocket `rocketServer.environment.workers.*` is overridden by the keos-apps size-overlay's inline `SPARTA_BOOTSTRAP_*` env | overlay `components/rocket/app/overlays/hdfs/{S,M,L}/helm-patch.yaml` | patches the inline env list |
| Renders **with** the existing patch and replaces the whole patch by the residual (`cmd_steps.py:331-332`) | looks like a second-pass run (unconfirmed) | renders without it; whole patch |
| int `1000000` → `1e+06` | `helm template -f` | string `"1000000"` |
| Stale exclude `rocketCommon.settings.governanceIntegration.*` (real path is `rocketServer.settings…`, `values.yaml:1706`) | `config.json:176-178` | correct paths (`seed.go:326-328`) |
| `cluster.domain: eosdev.int` leaves `KERBEROS_REALM_NAME` wrong (realm is case-sensitive, live `EOSDEV.INT`) | one path feeds `KERBEROS_REALM_NAME` (upper) and `PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN` (lower) | `cluster.domain: EOSDEV.INT` + inline pin of the Pekko var (`inline.go:85-93`) |
| Drops valueless live env vars (`backup.py:237`): no `mutualTlsCnWhitelist: ""` / `translateLitellmModel: ""` | live env entry has no `value` | maps to `""` (`envvars.go:137-147`) |
| Writes gosec-agent patches to the parent entry's top-level `patches` (`cmd_steps.py:303,328-331`): matches nothing (parent kustomization renders only OsCluster/PgCluster) | `/tmp/apps-opensearch1-gosec-agent.yaml` had `patches: []` | `config.agent.patches` |

Unconfirmed (do not state as fact in docs): why legacy emitted virtualizer
`virtualizerServerConfigSparkExecutorDockerImage` (inferred: registry var unresolved at render time),
and that the legacy run was a second pass. The chart default equals live, so Go correctly emits nothing.

## Changes to make

### 0. Preflight
- `make fmt-check vet lint test build` is green on the current tree before starting (79 files are
  modified and uncommitted over HEAD 38c8f2c; don't commit unrelated work).
- Confirm the binary under test is this tree's: rebuild and reinstall to `~/.fluxcd/plugins/flux-stratio`.

### 1. Report (don't silently drop) differences hidden by `exclude`
Problem: `internal/diff/chartdiff.go:287` does `if isExcluded(path, in.Exclude) { continue }` and the
final `removeDotPath` loop, so a live value that differs from the GitOps default and is excluded
(genai's Vault roles) disappears with no message. Policy is right; invisibility isn't.

- `internal/diff/chartdiff.go`: add `type ExcludedDiff struct{ Workload, Name, Rendered, Live, Path string }`
  and `ChartDiffResult.Excluded []ExcludedDiff`. In the loop at `:287`, before `continue`, append one
  entry per `mapped[path]` element (`mappedValue` has workload, name, rendered value, live). Also do it
  for the moot-ambiguity case at `:271-279` only if trivial; otherwise skip. Sort deterministically
  (reuse `sortUnmapped` style, by workload, name).
- `internal/appdiff/chart.go:~69`: add `Excluded: result.Excluded` to the returned `Result` (add the
  field to `appdiff.Result` in `appdiff.go`, next to `LiveOnly`).
- `internal/cli/apps.go`: add `Excluded []diff.ExcludedDiff` to `chartReview` (`:424`), set it where
  `chartReview` is built (`:309` and the migrate equivalent near `apps_migrate.go:212`), print it in
  `reportChartReview` (`:445`) with `logger.Actionf` (info), one line each:
  `excluded by the catalog: <workload>/<name> live %q, GitOps default %q (<path>)`.
  **Do not** add it to `hasWarnings()` (`:436`): it is informational and must not make `--yes` stop
  (`apps_migrate.go:242`).
- Tests: `internal/diff/chartdiff_test.go` (an excluded mapped diff lands in `Excluded`, not in
  `Patch`; a non-excluded one doesn't); `internal/cli/apps_report_test.go` (printed via Actionf, and
  `hasWarnings()` stays false when only `Excluded` is set). The `internal/cli` package is normally
  untested by design, but `apps_report_test.go` already exists for `reportChartReview`, so extending
  it is consistent.

### 2. Warn about a legacy-placed gosec-agent patch left on the parent entry
Problem: a tenant file previously written by the legacy client has a `target: kind: HelmRelease`
patch named `<agent-object>` (e.g. `opensearch1-gosec-agent`, `psql-gosec-agent`) in the parent
entry's top-level `patches:`. It is inert (matches nothing) but misleading, and Go's patch for the
agent goes to `config.agent.patches`, so nothing removes it.
- In the migrate planning path (`internal/appmigrate/appmigrate.go` `plan()`), when the app's resolved
  anchor is `config.agent.patches` (`catalog.ResolvedAnchor`, see `tenantfile.ResolveAnchorNode`
  at `internal/tenantfile/anchor.go:178` and `OwnerOf` in `splice.go:112`), inspect the owner entry's
  top-level `patches` for a patch whose target kind is HelmRelease and whose `metadata.name` equals
  the agent object name. If found, add a plan warning (same mechanism `obsoletePatches` uses in
  `apps.go:415-417`: "…that Flux would apply on top — review or remove them"), phrased: "legacy
  client placed this agent patch at the parent entry's `patches`; it is ignored, the agent reads
  `config.agent.patches` — remove it". Warn only; never delete.
- Add a tenantfile-level helper + test in `internal/tenantfile` (comment-preserving `yaml.Node`
  reads only), and an appmigrate test using a fixture tenant file with the legacy shape.
- Stop and ask if `plan()`'s warning plumbing makes this more than ~40 lines of non-test code: it is
  the lowest-priority item.

### 3. Docs
- `CONTEXT.md` §9: add rows for the legacy behaviours in the table above that are not already there
  (manifest-mode `substituteFrom`, flat-map mis-attribution incl. unmounted `rocketCatalog.*`,
  inline-env override of `workers.*`, render-with-existing-patch residual, stale rocket excludes,
  `cluster.domain` casing, valueless env vars). Keep each to one line; link
  `docs/TASK-multi-workload-env-var-collision.md` for the flat-map one.
- `README.md` / `docs/migration-runbook.md`: add one sentence that excluded differences are listed
  (change 1) and are not warnings.
- `CHANGELOG.md`: entries for changes 1 and 2.
- If you add the reporting in change 1, mention it where `docs/config-reference.md` describes `exclude`.

## Out of scope for this repo (report to the user, don't implement)
- **`ROCKET_POD_NAME` is lost**: the keos-apps rocket size overlay (`components/rocket/app/overlays/hdfs/{S,M,L}/helm-patch.yaml`)
  replaces the chart's whole `controllers.rocket.containers.rocket.env` list (live has it as a
  fieldRef). Go already names it in the "N variables aren't rendered by the chart" warning. Fix is in keos-apps.
- **Chart**: `KERBEROS_REALM_NAME` derived from `cluster.domain` forces the casing pin; `| upper` in
  `rocket/config/storage/rocket_hdfs_env_vars.yaml:5`, or reading `KERBEROS_REALM` from
  `keos-runtime-info`, would remove the pin and the cosmetic ingress `auth-url` casing change.
- **Container `resources` are not compared by either tool** (rocket live cpu 4 / mem 4.096E9 vs
  overlay M cpu 2 / 4092Mi). A real post-migration behavior change; needs a design decision (new
  comparison surface) — don't build it in this task, raise it.
- **opendashboards**: seed keeps live host `admin.stratio.k8s…` (other GitOps URLs use `admin.k8s…`)
  and pins image `1.1.3` while the OsCluster type excludes `spec.image`; versions could diverge if
  the OsCluster later takes the operator default. Intentional per the seed comment (`seed.go:98-105`);
  just flag to the user.
- Legacy `cmd_patch.py:37` should pass `resolve_substitute_from=True` — legacy is frozen, not ours.

## Verification
- `make fmt-check vet lint test build` (prefix `GOWORK=off` if calling `go` directly).
- Unit tests from changes 1 and 2.
- Live (read-only), eosdev: `flux stratio apps diff genai --baseline latest` now prints the
  `excluded by the catalog:` lines for the three genai Vault roles and still ends with no warnings
  caused by them; `flux stratio apps diff rocket --view patch` is unchanged; `apps migrate genai
  --dry-run --yes` is not stopped by the new lines.
- Run the same diff for `litellm`, `dg-hdfs-agent`, `dlc-entity` to confirm no output regression.
