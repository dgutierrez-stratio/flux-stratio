# Migration history and context (legacy CCT to GitOps, eosdev run of 2026-10-13/14)

> Context for reviewers and maintainers, human or LLM. It is **not a bug tracker**. It records what was
> done, why, where it lives, and what is non-obvious, so the large set of changes made in September
> and October 2026 across the Stratio GitOps repositories can be reviewed and maintained.

## Outline

1. Purpose and how to use this file
2. The big picture
3. Repositories and the changes made in each
4. Chart variables versus the CCT descriptor (why legacy values differ from chart values)
5. The migration process (runbook narrative, with commands)
6. Timeline of the eosdev run
7. Per-component notes
8. Lessons learned and gotchas
9. Decisions and conventions
10. Known open issues (context only)
11. Glossary and pointers

## 1. Purpose and how to use this file

**What it is.** One consolidated record of the legacy-to-GitOps migration work that ran from
2026-09-20 to 2026-10-14 on the `eosdev` test cluster (tenant `stratio`), written for a reader who
knows Kubernetes and Flux but has never seen this project. It replaces roughly twenty scattered
`docs/TASK-*.md` working notes (investigations, audit findings, decisions), which were pruned once
their content was folded in here. The path to recover any of them is in section 11.

**What it is not.** It is not a to-do list. Where something is still open it is described as context
(section 10). The only tracked pending work lives in three files that were deliberately kept:

| File | Subject |
|---|---|
| [`TASK-13-rocket-catalog-config-not-delivered.md`](TASK-13-rocket-catalog-config-not-delivered.md) | The rocket chart renders five catalog variables into a ConfigMap no workload mounts; the plugin's env diff did not notice |
| [`TASK-14-dg-hdfs-agent-gosec-hdfs-policy-error.md`](TASK-14-dg-hdfs-agent-gosec-hdfs-policy-error.md) | dg-hdfs-agent: GosecPolicy stuck in ERROR (gosec-services-daas NullPointerException) and, separately, empty HDFS discovery |
| [`TASK-15-prepare-steps-for-immutable-selector-apps.md`](TASK-15-prepare-steps-for-immutable-selector-apps.md) | Which apps need a component-scoped prepare step because their chart changes an immutable Deployment selector |

**How to use it as context.**

- To review a repository's changes, read section 3 for that repo (branch, PR, themes, key commits,
  what to look at), then the matching rows of section 7.
- To understand why a migrated app differs from its legacy self, read section 4 first.
- To repeat or audit the run, read sections 5 and 6.
- Before changing behaviour, check sections 8 and 9: several things that look like bugs are
  deliberate (the `dg-` to `governance-` rename, hyphenated Vault role names, legacy values kept in
  patches).
- Sources are cited as commit hashes and `file:line` where that helps. Line numbers were correct
  when written and drift; trust the commit.
- When two sources disagreed, the later one won (code and git over docs; the run ledger's
  2026-10-14 part over earlier notes). Real contradictions are called out in place and collected in
  section 8 ("Where the notes disagree").

**Conventions in this file.** Times are UTC (`Z`) unless stated; git shows them as `+0200`, so a
commit stamped `16:01 +0200` is `14:01Z`. Commands are shown as they were run, with the secrets
removed: no token, password or key value is reproduced anywhere in this document, only names and
lengths.

## 2. The big picture

### 2.1 What was migrated, and from what to what

Stratio applications used to be deployed by **Command Center (CCT)**, an Ansible-style engine driven
by JSON *descriptors* from the `kubernetes-universe` repo. Every object it created carries
`cct.stratio.com/*` annotations and the label `cct.stratio.com/application_id=<name>.<namespace>`.
The target is **GitOps**: a Flux tenant whose apps are Helm charts (or operator custom resources)
reconciled from git. The two worlds differ in ways that matter:

| | Legacy (CCT) | GitOps |
|---|---|---|
| Source of truth | Descriptor `parameters` + lifecycle scripts + a deployment repo (`apps-layer-automation`) + Command Center form inputs | Chart `values.yaml` + `keos-apps` overlays + tenant file patches |
| Workload selector | `cct.stratio.com/application_id=<name>.<ns>` | `app.kubernetes.io/{name,instance,component}` (immutable, so a same-named Deployment must be removed first) |
| Names | `dg-*` for governance components, `connectors-management`, `datamarket-agent`, ... | `governance-*`, `connectors`, `governance-datamarket-agent`, ... |
| Vault role | `<ns>-<app>` (hyphen) | `<ns>_<app>` (underscore, the GitOps `approlename`) |
| Hosts and paths | A host per app (`genai.<tenant>...`) | The admin host with a path (`admin.k8s.../genai-ui.stratio-genai`) |

The migration goal, stated by the user and applied to every decision: **keep each client's legacy
configuration as close as possible, accept genuine improvements, assume neither direction.** For
every difference one investigates whether the change makes sense and classifies it *preserve
legacy*, *accept new* or *needs a product decision*.

### 2.2 The tools and repositories

```
          Stratio/charts                 Stratio/keos-apps              Stratio/keos-use-cases
     (Helm charts, `common` lib)    (kustomize bases + S/M/L size   (Flux ResourceSets that render
                  |                  overlays, HelmReleases, CRs)       per-tenant Kustomizations)
                  |                              |                                 |
                  +------------+-----------------+---------------+-----------------+
                               |                                 |
                 Stratio/keos-system-services            Stratio/keos-fleet
                 (cluster-wide services: vault,       (per-cluster config; the eosdev tenant
                  gosec-operator, ingress, idp...)     file lives here and is what we edit)
                               |                                 |
                     flux-keos  (system services)       flux-stratio  (tenant apps)
                     `flux keos cluster migrate`        `flux stratio apps|tenant|doctor`
```

| Repo / tool | Role | Local path (this machine) |
|---|---|---|
| **flux-keos** | Go CLI. `flux keos cluster migrate` does the one-off cutover of the **system services** layer (governance, ingress, etcd/vault...). Idempotent, self-sufficient steps | `/stratio/gitops/flux-keos` |
| **flux-stratio** (this repo) | Go Flux CLI plugin (`flux stratio ...`). Migrates the **tenant apps**: scans, backs up, diffs and writes a patch into the tenant file; runs per-component *prepare* steps | `/stratio/gitops/flux-stratio` |
| **charts** | Helm charts for the apps, plus the Python `charts` migration CLI (`bin/migration/migrate.py charts analyze|values|environment|import|validate`) that turns CCT descriptor `parameters` into chart values | `/stratio/aws/flux/.worktrees/charts-align-15.1.0` (worktree of `/stratio/charts/charts`) |
| **keos-apps** | Flux "d1 reference architecture" repo: HelmReleases and CRs per component, with S/M/L size overlays and per-storage variants, using `${VAR:=default}` post-build substitution | `/stratio/aws/flux/.worktrees/keos-apps-env-list-overlays` |
| **keos-use-cases** | ResourceSets (`apps/components/resourceset-apps-*.yaml`) that turn each tenant-file entry into Kustomizations that point at keos-apps | `/stratio/gitops/keos-use-cases` |
| **keos-system-services** | GitOps definitions of cluster-wide services | `/stratio/gitops/keos-system-services` |
| **keos-fleet** | Per-cluster config. `clusters/eosdev/tenants/config/stratio.yaml` is the **tenant file** | `/stratio/gitops/keos-fleet` |
| **kubernetes-universe** | CCT descriptors (`packages/*`). 15.1.0 release commit `54ab1918` | `/stratio/p/kubernetes-universe` |
| **apps-layer-automation** | The legacy deployment repo: per-app `vars.env`, `*.json.tmpl` overrides, `pre-install.sh` | `/stratio/aws/apps-layer-automation` |

The split of responsibilities that is easy to get wrong: **flux-stratio does not do the system
layer** (that is flux-keos), **does not do descriptor tooling** (that stays in the Python `charts`
CLI) and **is not the cutover engine** for legacy workloads (see section 9, no generic delete step).

### 2.3 How one tenant app is rendered

```
tenant file (keos-fleet clusters/eosdev/tenants/config/stratio.yaml)
  = ResourceSetInputProvider, label keos.stratio.com/resourceset-type: tenant-config
    spec.defaultValues: tenantName, size (S|M|L), universeVersion, universePrereleaseSuffix,
                        components: { <key>: [ {name, enabled?, config, dependencies, patches} ] }
        |
        v   keos-use-cases  apps/components/resourceset-apps-*.yaml   (flux-operator build rset)
ResourceSet  -->  Kustomization  apps-<object>   in namespace <tenant>-<area>   (+ -postrequisites)
        |           postBuild.substitute: LITELLM_NAME, STRATIO_VIRTUALIZER_UI_ENABLED, ...
        v   keos-apps  components/<app>/app/{base,overlays/<storage>/<S|M|L>}
HelmRelease  (chart from HelmRepository helm-universe, version = universeVersion)
        |   or an operator CR (PgCluster, HDFSCluster, KafkaCluster, OsCluster, ...)
        v   + the tenant entry's  patches:  (a HelmRelease .Values patch, or an SMP / JSON6902 CR patch)
chart templates + config/<flavor>_env_vars.yaml  -->  ConfigMaps (envFrom) + Deployments
```

Points worth knowing:

- The **tenant entry's `patches:` block is where the legacy configuration is kept.** `apps migrate`
  computes and writes it. A component pushed **without** its patch is installed with GitOps defaults
  and silently changes the live app. Hence the rule of the run: *never push a component without its
  patch* (section 5).
- The render goes through **`flux build kustomization`** (Flux 2.9.2 semantics), which matters for
  empty substitutions (section 8, "Empty `${VAR}` is YAML null").
- Which repo revisions the cluster uses is set in `clusters/eosdev/default/resourcesetinputproviders.yaml`
  (`keosApps`, `keosUseCases`, `keosSystemServices` refs, `universe` version). During this work those
  refs pointed at **unmerged branches** (section 3.8).
- Tenant charts are pinned to the pre-release snapshot `universeVersion: 15.1.0-PR258-SNAPSHOT`
  (the Jenkins build of Stratio/charts PR #258, which includes the litellm work of the closed #257).
  Only the tenant's charts are pinned; the cluster's operators keep `15.1.*` from the cluster RSIP.

### 2.4 What the flux-stratio plugin does (one paragraph per verb)

Workflow: `config init` -> `doctor` -> `tenant import` -> `apps backup` -> `apps diff` -> `apps migrate`.
Nothing is persisted between runs; every command re-derives its state from the live cluster and the
rendered templates, so commands are idempotent. stdout is data, stderr is narration. Full command
reference: `README.md`, `CONTEXT.md` section 3, `docs/migration-runbook.md`, `docs/config-reference.md`.

- **`tenant import`** scans the live cluster (CRD instances, live workloads classified through the
  catalog, HelmReleases) and writes the tenant file skeleton, or *completes* an existing one by
  textual insertion only (comments, commented-out blocks and patches are never touched). It validates
  the result by rendering every ResourceSet against it.
- **`apps backup`** writes `<dir>/<app id>/<UTC timestamp>/` (`cr.yaml` or `deployment.yaml` plus
  `workload.<kind>.<name>.yaml`, merged `env-vars.env` and per-workload `env-vars.<kind>.<name>.env`).
  Secrets never enter env files (`<secret:NAME/KEY>` placeholders).
- **`apps diff <app>`** compares what the tenant file renders (existing patches applied) with live.
  `--baseline <backup>` replaces the live side with a backup (for apps Flux already reset);
  `--drift <backup>` compares live now with a backup, no rendering at all. It reports in sections
  `[ACTION]`, `[REVIEW]`, `[NOTE]`.
- **`apps migrate <app>|--all`** computes the same diff but renders the base *without* the entry's
  existing patch, so the patch it writes is always the whole patch; shows it; asks; **saves the patch
  first, then runs the app's prepare step** (a prepare step may delete the workload the patch was
  read from). It never commits or pushes. `--baseline` takes the legacy values from a backup.
- **Prepare steps** (`internal/prepare`) are component-scoped, read-only to plan, UID-pinned when
  applied, find their targets by CCT app id (never by hard-coded names), skip anything carrying a
  Flux label, and take a backup first. Registered: `prepare-datamarket-agent`, `prepare-datarest`,
  `prepare-dlc`, `prepare-genai`, `prepare-virtualizer`, `prepare-dg-agent`, `prepare-discovery`,
  `prepare-litellm`.
- **`doctor`** is the preflight: binaries, catalog, environment, repo layout and *revisions*, chart
  paths, cluster, Vault access, tenant file.
- The **catalog** (`~/.fluxcd/flux-stratio/catalog.yaml`, seeded by `config init`, 20 types) maps live
  legacy objects to GitOps components: `match` selectors, name templates, `chart.path`,
  `chart.siblings` (workloads CCT deployed as separate apps that the chart renders), `chart.uses`,
  `exclude` (paths deliberately **not** preserved), `prepare`, `memoryEnv`, `requiredConfig`,
  `configFromEnv`.


## 3. Repositories and the changes made in each

All work carries the Jira ticket **PLT-4838** (eosdev legacy-to-GitOps migration) or **PLT-4989**
(governance cutover), or `[NOJIRA]`. Every PR below is by `dgutierrez-stratio`, open, and had no
review at the time of writing. Several PRs are *umbrellas*: a smaller PR was merged into the branch
so eosdev could test with one ref, and the smaller PR was then closed unmerged. A reviewer reading
only a PR body will miss those parts, which are listed here.

| Repo | Branch | PR | Role in the run |
|---|---|---|---|
| flux-stratio | `feat/application-migration` | dgutierrez-stratio/flux-stratio#1 | The migration plugin |
| charts | `dgutierrez-stratio/align-15.1.0` (base `master`) | Stratio/charts#258 | Align charts with the 15.1.0 universe; litellm naming; dg-agent, virtualizer, genai fixes |
| keos-apps | `nojira/env-list-overlays` (base `main`) | Stratio/keos-apps#152 | Size overlays no longer replace the chart env list; litellm naming and post-requisites Job |
| keos-use-cases | `dgutierrez-stratio/PLT-4838-litellm-name` | Stratio/keos-use-cases#202 | litellm entry name reaches its Kustomizations |
| keos-system-services | `dgutierrez-stratio/gosec-operator-memory` | Stratio/keos-system-services#254 | gosec-operator memory limit |
| flux-keos | `dgutierrez-stratio/PLT-4989-governance-callbacks` | Stratio/flux-keos#44 | Governance cutover step |
| keos-fleet | `main` (direct pushes) | none | The eosdev tenant file; wave commits W1-W7 |

**Merge order that matters.** charts#257 (closed, folded into #258) -> keos-apps#147 (closed, folded
into #152) -> keos-use-cases#202. Until the keos-apps change lands, `LITELLM_NAME` is an unused
substitute key. After merging, eosdev's refs and the chart pin must be pointed back at `main`
(section 3.8).

### 3.1 flux-stratio (`/stratio/gitops/flux-stratio`, PR #1)

**What it is.** A Flux CLI plugin (`github.com/Stratio/flux-stratio`, binary `flux-stratio`), a Go port
of the application-migration half of the legacy Python client `charts/bin/migration/migrate.py`. The PR
is a greenfield 270 files / about +50k lines, 45 commits from 2026-09-22 to 2026-10-14. Its PR body is
stale in two places (says 19 catalog types and a four-step prepare list; the code has 20 types and eight
steps). Commit `8c84854` is a self-described large "safe checkpoint". Review **by package, not by
commit**; the tests (fake client, `runner.Fake`, fixtures captured from real keos-use-cases templates)
are the main review aid, and real-cluster behaviour (exec, Vault, `flux-operator build rset`) rests on
the eosdev runs.

**Themes and key commits** (see `CHANGELOG.md` for the full list):

| Theme | Commits | Why it exists |
|---|---|---|
| Foundation | `63b7f93` (initial plugin), `b6afcc0` (discovery, backup, drift, `--view`), `4e2fc63` (typed catalog and classifier) | Port of the Python client. Fixed along the way: wrong tenant label, gosec-agent patches misrouted by a sentinel, whole-file YAML re-dump destroying comments (now yaml.Node splice), float64 vs int64 decode, merged stdout/stderr |
| Chart-mode correctness | `b383e35` (per-workload env diff, siblings), `d2a757b`/`3cbff6b` (resolve an already-migrated app from its HelmRelease), `3adcf3f` (`ReleaseName` from `spec.releaseName`), `03340eb` (legacy values the ConfigMaps cannot carry), `934fa0f` (compare workload specs, carry legacy resources) | A chart's sibling workloads (genai-api/ui, virtualizer/monitor/ui) set the same env names; a flat name-to-path map wrote genai-api's `VAULT_ROLE` into genai-ui. Resources, replicas, image, selector were invisible |
| Safety | `064b588`, `2afc944` (plan and save the patch before a prepare step can delete its source), `01eb9d1`/`8c84854` (show exactly what a step does; `[ACTION]/[REVIEW]/[NOTE]` layout) | Destructive steps run only after the patch is saved and a backup exists |
| Prepare steps | `4729ab3` + `c7b85a0` (litellm, Vault moves), `3fcfb35` (genai SQL rewrite, confirmed twice), dg-agent, discovery, virtualizer, datarest, dlc, datamarket-agent | Component-scoped removal of legacy objects Flux cannot replace |
| Tenant import | `9b2d8ea` (dependency `enabled`, validation), `318b4f3` (nested config such as genai `llmModels`), `7bfbdfa` (file whose `components` is empty), `8c0b0b9` (legacy-cluster discovery via the catalog, 20th type `connectors`) | Importing on a still-legacy cluster missed components and produced a file that did not validate |
| Repo layout | `418b3c6` (breaking: `--repo name=path`; `chartsBase` removed), `23087cf` (doctor checks template paths) | Allow git worktrees and renamed clones |
| Last four commits of the run | `ee558f6`, `7078283` (docs), `8c0b0b9`, `b879e75` | See below |

**The four most recent commits (2026-10-14):**

- `b879e75` *a null in a rendered ConfigMap no longer empties the whole ConfigMap.* Rendered ConfigMap
  data was read with `unstructured.NestedStringMap`, which errors at the first non-string value and
  returns an **empty map**; both callers discarded the error (`internal/diff/envvars.go:45`,
  `chartvalues.go:133`). genai's HelmRelease renders `genaiLitellmModel: null`, so all 119 genai-ui
  variables were reported "lost" and the patch dropped 13 real legacy values. Fix: `stringMapAt` in
  `internal/diff/stringmap.go` reads like the API server (null becomes `""`, bool/number become their
  text, map/list is an error). `appdiff.chartDiff` fails on a ConfigMap the API server would reject and
  warns, once per ConfigMap, naming the null keys (never stops `--yes`). Also replaced at
  `inline.go:58,109`, `resourcepatch.go:122`, `workloadspec.go:85`. On eosdev the genai dry run went from
  120 "lost" to 1 (`genai-api/NOT_USED`, a placeholder with no setting). **Rule:** never use
  `NestedStringMap` on rendered data. A grep for other `unstructured.Nested*` calls that drop the error
  on rendered data would find the same bug class.
- `8c0b0b9` *tenant import scaffolds a legacy tenant file that validates.* Discovery now goes through
  the catalog classifier (entry names from each type's `entry` template: `connectors-management` becomes
  `connectors`, `genai-litellm` stays), `configFromEnv` derives an agent's `type` from
  `CUSTOM_SERVICE_EXTRACTOR_TYPE` and datarest's database from `JDBC_DATABASE`, a dependency whose
  component runs live is written `enabled: true` and flagged as assumed, and GitOps-only components that
  something waits on (`pgbackuprepository`) are added with a warning.
- `ee558f6` *probe variables the chart carries natively are not lost.* CCT exported descriptor probe
  settings as `{LIVENESS,READINESS,STARTUP}_PROBE_*` env vars; a chart sets them on the container probe.
  `internal/diff/nativeprobe.go` maps the variable to the rendered probe field; equal values are listed
  as "carried natively by the chart" (a note), different ones stay lost with the chart value in the WHY
  column. Removed false "lost" lists for rocket (9) and both gosec agents (8 each).
- `7bfbdfa` (2026-10-13) *tenant import completes a file whose `components` is empty*: `components: []`,
  `{}` or a bare key previously failed; fixed by a textual rewrite plus three side bugs in the insertion
  arithmetic (duplicate keys, mis-placed additions, blank lines).

**What to look at when reviewing** (the digest of risk areas):

1. Destructive paths: `internal/prepare/prepare.go` (`legacyObjects`, `fluxManaged`, `deleteOp`,
   `patchOp`), `step_virtualizer.go` (family selection through `Options.IsLegacy`: could it select a
   Helm-managed object that lacks a Flux label?), and the ordering in `internal/cli/apps_migrate.go`
   (`saveThenPrepare`, `backupBeforePrepare`, `ensurePrepared`). `--yes` semantics per step
   (`prepare-genai` is never answered by `--yes`).
2. Vault code: `internal/vault/vault.go`, `internal/prepare/vaultmove.go` (kv v1 only, redaction, token
   on stdin, `guardedWrite` narrowing but not closing the TOCTOU window, `releaseCutOver` early exit).
3. Diff correctness: `internal/diff/chartdiff.go`, `chartvalues.go` (per-workload attribution,
   `UnmappedDiffs`), `resourcepatch.go` (`VerifyResources`), `settle.go`, `stringmap.go`.
4. Tenant-file safety: `internal/tenantfile/edit.go`, `internal/tenantimport/merge.go` (textual
   insertion; `7bfbdfa` shows this area had several position bugs), `UpsertPatch`/`Splice`.
5. Catalog contents: `internal/config/seed.go`. Each `exclude` deliberately drops a legacy value, so each
   should be defended (section 9).
6. Resolution: `internal/components/resolve.go` and `classify.go` (post-cutover resolution through the
   HelmRelease, ambiguity prompts, `--all` ordering and skipping).
7. `doctor`: a check that wrongly passes would let a destructive step run against a stale checkout.

**Known rough edges** (context): `--baseline` is one-directional and cannot see GitOps-only keys added
by an overlay; `apps diff` of an operator CR prints about 127 lines of noise because nested `${...}`
substitutions are not expanded; `apps migrate` prints no NOTE for restarts an operator CR change will
cause; backup/drift id mismatch when an app is renamed (backup is under `connectors-management`, so
`--drift latest connectors` fails); `LiveManifestObject`/`LiveChartWorkloads` are exported but unused.
Working tree at the time of writing has uncommitted docs only (CHANGELOG, CONTEXT, TASK files); no Go
source is uncommitted.

### 3.2 charts (`/stratio/aws/flux/.worktrees/charts-align-15.1.0`, PR Stratio/charts#258)

Base branch is **`master`** (there is no `origin/main`). The PR is OPEN, `mergeable=CONFLICTING` (master
moved; CHANGELOG is the likely conflict), 30 files, 19 commits. Charts #257 (litellm: "grant the
configured database and support any release name") was merged into it (`7dc6d80`) and closed. The PR
body describes only the first 7 commits (2 Oct); the later ones are described only by commit message and
`CHANGELOG.md` under "15.1.0 (upcoming)". Worktree rule: do not touch `/stratio/charts/charts` itself
(someone's uncommitted `bin/migration` edits live there).

**Purpose.** Bring the charts in line with the **kubernetes-universe 15.1.0 release**, reference commit
`54ab1918` ("[BUILD] 15.1.0: New pre-release", 2026-09-11), final image tags only. The later 15.1.1 work
(rocket 4.1.1, SNAPSHOT and `-M` tags, new parameters) is deliberately **not** applied; do not "fix" by
bumping further. Otherwise a migrated app would lose variables that existed only live, or change image
versions.

**Theme A, image tags to the 15.1.0 release.** rocket 4.0.x to **4.1.0** (`e3b229b`); genai hash tags to
final (api and developer-proxy `0.8.0`, ui `0.9.0`, sandbox `0.2.0`, `7055bcc`); eureka-agent
`eureka-bdl-agent` 1.0.1 to 1.1.0 (`70f5a5c`); dg-agent `dg-custom-agent_2.12` 1.1.1 to **1.2.0**
(`bcc8e3c`; one image for all 23 agent types); spark-history 3.5.7 to 3.5.8 (`78c62ee`). Charts already
matching and untouched: governance, governance-datamarket(-agent), opendata, connectors, discovery,
search-engine, gosec-agent, intelligence, bdl-datarest.

**Theme B, track descriptor parameters the chart never carried** (env-var files plus defaults in
`values.yaml`, as `charts analyze` reports them): rocket 20 variables (`04d763e`: GenAI chain cache and
history limits, governance availability, catalog cache and JDBC pool, validator, workflow, scheduler),
genai-ui six governance parameters and removal of `BOOTSTRAP_GLOSSARY_ITEMS` (`c1e9f8c`), eureka-agent
`BDL_GOVERNANCE_ONTOLOGY_DROP_OPERATIONAL_COLLECTION`, dg-agent `AGENT_*_LOG_LEVEL`, `COMM_DS_PASSWORD`,
`DFS_TEXT_FOLDER_ENGINE`, `CUSTOM_SERVICE_SSCC_DRIVER_LOCATION` (`614ddaa`), virtualizer
`CONF_KV_spark_sql_warehouse_dir` (`7a151b5`), dlc-entity five stale CDC-Engine variables removed
(`c22f66e`). Deliberately untouched: CCT artefacts (`NOT_USED`, `CUSTOMER_SIZE`, `MIGRATION_STORAGE_PROFILE`,
`*_PROBE_*`, `VR_*`), resource and JVM-heap defaults (the keos-apps size overlays own them).
"Not rendered by the chart" counts on eosdev before to after: rocket 34 to 14, genai 7 to 1, virtualizer
4 to 3, eureka-agent 1 to 0, dg-hdfs-agent 7 to 0.

**Theme C, fixes found while migrating eosdev:**

| Commit | Change | Reason |
|---|---|---|
| `0a1e649` (Oct 9) | dg-agent `CUSTOM_STRATIO_CREDENTIALS` for hdfs and postgresql defaults to blank (was `hdfs-external-secret` / `postgres-external-secret`) | The 15.1.0 descriptor default is `""`. The old default made an internal-HDFS agent fetch a Vault secret that does not exist (non-fatal getpass ERROR at every start) and differ from its legacy value. External HDFS is unaffected (it sets the name explicitly) |
| `feb37d7` (Oct 13) | virtualizer `EXTRA_JARS` composed by `virtualizer.server.extraJars` (`virtualizer/templates/datastores/_extra-jars.tpl`): storage connector, decoupled HDFS connector, decoupled PostgreSQL connector, then free-form `connectorJdbcDrivers`. New values empty by default; also removes a leading space that `_adls.tpl`/`_gcs.tpl` emitted | The chart never ported the descriptor's JDBC-drivers slot, so an HDFS install could not get the connector jars the legacy deployment downloaded |
| `7eee574` (Oct 14) | dg-agent `COMM_SERVICE_NAME` = `agent.config.common.customServiceName \| default .Release.Name`; genai `genaiUi.serviceUrl` double slash fixed | A migrated agent must keep its legacy datastore name (`hdfs1.stratio-datastores`). Default render is byte-identical |

**Theme D, litellm under any release name** (from #257): `gosecPolicy.litellm-pgacl` grants the database
named by `liteLlm.settings.postgresql.postgresDatabase` instead of the release name; `nameOverride` on
controllers, ingress, `secretsBundle` and `secretsIdentity` so a release called `genai-litellm` works
(`9bd77ca`). The Vault secret names were first made configurable and then **reverted** (`ec2c6b9`)
because the secrets operator deletes every key in a bundle's folder that the bundle does not declare
(section 8, "Vault underscores"). `6e9c3ec`: the prerequisites job creates the Gosec groups from
`liteLlm.general.gosecIntegration.adminGroup/usersGroup` (single source), so a patch that keeps the legacy
group names does not leave them uncreated and spawn two unused defaults.

**Reviewer pointers.** (1) The PR body is stale; ask for a refresh. (2) `COMM_DS_PASSWORD` is added with
the descriptor's literal default `stratio` (also the live value); the PR offers to drop it. (3) The blank
`CUSTOM_STRATIO_CREDENTIALS` changes behaviour for a *non-migrated* external agent that relied on the old
defaults. (4) `bin/check_descriptors.sh` is unreliable for charts with Vault agent annotations and treats
`pgcommon`/`oraclecommon` as flavors (documented in `bin/migration/README.md` section 7.1; do not rename
the files). (5) Verification claimed in the PR: `charts validate` (no new failures), `helm template` old
vs new for each chart's examples, `charts analyze` (every added variable MATCHED), `apps diff --baseline`
counts above. (6) Open and unrelated to the PR scope: findings in section 10 (H4 rocket catalog wiring,
H6 rollout strategy, M11 intelligence ingress, M1 virtualizer-ui port name).

### 3.3 keos-apps (`/stratio/aws/flux/.worktrees/keos-apps-env-list-overlays`, PR Stratio/keos-apps#152)

Base `main`. OPEN, mergeable, 19 files, 8 commits ahead. keos-apps#147 (litellm instance name
configurable) was merged in (`41e4fd0`) and closed; the purpose was a single `gitReferences.keosApps` ref
for the eosdev test.

**Theme A, size overlays must not replace the chart's `env` list** (`ea8ac89`, the PR title). Helm
replaces lists wholesale. The rocket overlays `components/rocket/app/overlays/{hdfs,s3}/{S,M,L}/helm-patch.yaml`
set `controllers.rocket.containers.rocket.env` (13 entries) against a chart default of 2, so the chart's
`ROCKET_POD_NAME` fieldRef was silently lost for every tenant. Now the `env:` list is deleted and the 12
`SPARTA_BOOTSTRAP_*` values are set through the chart's own
`rocketServer.environment.workers.{catalogService,debugService,externalService,validatorService}.*` keys
(note the plural `externalServiceCpusRequest/Limit`). The nine virtualizer overlays carried a `VR_*` list
identical to the chart default (a latent trap); the list was deleted. Verification: rendered all overlays
before and after and compared the effective container env (ConfigMap via `envFrom`, then `env`). Rocket:
only difference is the added `ROCKET_POD_NAME`; virtualizer: none. **Consequence:** rocket pods roll on
the next reconcile (the `checksum/configMaps` annotation changes). Do **not** make the plugin copy
live-only env entries to compensate (a fieldRef would flatten to a literal, Secret-backed values cannot be
copied) and do not turn the chart env list into a map (per-item `enabled:` gating works only on list
items).

**Theme B, litellm instance name** (`b9cab17`, `9eef72f`, `3e19ccc`). A litellm migrated from CCT must
keep its legacy name `genai-litellm`, because the chart derives identity bound to its data (certificate
CN = Postgres user, Vault path, gosec user) from the release name. Introduces
`releaseName: ${LITELLM_NAME:=litellm}`. `metadata.name` of the HelmRelease and Kustomization **stays
`litellm`**; renaming the object broke genai's `dependsOn: apps-litellm`. Do not "tidy" this. The second
variable (master-key secret name) was reverted: litellm keeps the Vault secrets its own chart generates.

**Theme C, litellm post-requisites Job** (`components/litellm/config/talk-to-your-data/postrequisites/job.yaml`):

- `fad47b6` the Job hard-coded `<tenant>-litellm_admin_users/_users`; with a release called
  `genai-litellm` nothing creates those, so the Gosec PATCH answered 404 and the model reconcile never ran.
  Now it reads the groups from the release ConfigMap (`${LITELLM_NAME}-config`,
  `GENAI_LITELLM_GOSEC_ADMIN_GROUP/_USERS_GROUP`) and creates them if missing (409 = exists).
- `d39ed69` (Oct 14, **only on this branch, not on main**) the `sk-` prefix fix. The Job built
  `"sk-$(cat key)"`, but the carried-over legacy key already starts with `sk-`, so it authenticated with
  `sk-sk-...`; every call failed silently and the Job ended green. Now it prepends only when missing (the
  genai-litellm image entrypoint's rule), fails on non-2xx and missing JSON (`litellm_get` helper),
  checks the cleanup calls, ends with `exit 0`, and turns `set -x` off (`{ set +x; } 2>/dev/null`)
  because `/etc/stratio/helpers.sh` enables tracing and would print the master key to the pod log.

**Reviewer pointers.** The PR body covers only theme A. `${LITELLM_NAME:=litellm}` must resolve
identically across base, overlays and the Job. The Job relies on the chart exposing the two Gosec group
variables (a fallback exists). `vet.sh` was not run locally; `apps migrate rocket` was not run for real
from this branch. Optional hardening not done: `${LLM_MODEL_*:-""}` in `components/genai/app/base/helm.yaml:71-75`
(quoting does not help; section 8).

### 3.4 keos-use-cases (`/stratio/gitops/keos-use-cases`, PR Stratio/keos-use-cases#202)

Branch `dgutierrez-stratio/PLT-4838-litellm-name`, 2 files, +7/-5:
`apps/components/resourceset-apps-genai.yaml` (the `$litellm` block and genai's `dependsOn`) and
`resourceset-apps-intelligence.yaml`. Commits `36cbf7b`, `b2e9f9d`, `030c375` (+ merges).

It adds `LITELLM_NAME: << get $component "name" >>` to the `substitute` block of both litellm
Kustomizations (`apps-litellm`, `apps-litellm-postrequisites`, namespace `<tenant>-genai`), and makes the
Kustomization **names a fixed literal** `apps-litellm[-postrequisites]`, rewriting the `dependsOn` in genai
and intelligence. The last commit exists because of a live bug: after the first, a tenant migrating
litellm as `genai-litellm` got a Kustomization `apps-genai-litellm` while genai still depended on
`apps-litellm` (`dependency 'stratio-genai/apps-litellm' not found`).

Reviewer notes: the PR body does not mention the fixed-name change. It is safe for native installs only
because their entry is already called `litellm`; a native tenant with a differently named litellm entry
would have its Kustomization renamed and the old one pruned. Native installs otherwise render identically
(`LITELLM_NAME: litellm` is the keos-apps default). `vet.sh`/`render.sh` pass (160 rendered, 0 failed).

### 3.5 keos-system-services (`/stratio/gitops/keos-system-services`, PR Stratio/keos-system-services#254)

One file (`components/gosec-operator/app/base/sync.yaml`, +3/-3): gosec-operator memory request 75Mi to
128Mi, limit 75Mi to **256Mi**, VPA `maxAllowed` to 256Mi. The manager was OOMKilled (exit 137) at 75Mi
when a migrated component's Gosec resources arrived (the genai install on eosdev, 2026-10-06). While it
crash-loops, `gosec-operator-webhook-service` has no endpoints and the `mgosecuser.stratio.com` mutating
webhook fails (`connection reset`/`EOF`), so the component's Helm install fails, and Helm's remediation
uninstall removed the running legacy genai-api/ui (downtime). The chart range `0.2.*` had moved the
operator from 0.2.0 to 0.2.1-rc.2 on 2026-10-02; whether rc.2 uses more memory than 0.2.0 was not
measured. Not in this PR: pinning the chart to 0.2.0. Reviewer note: the VPA block has `updateMode: "Off"`,
so the 256Mi `maxAllowed` only caps recommendations; the effective limit is the fixed `resources` block.

### 3.6 flux-keos (`/stratio/gitops/flux-keos`, PR Stratio/flux-keos#44, PLT-4989)

The **governance cutover**: system-services `dg-*` to `governance-*`. After `flux keos cluster migrate`,
governance tenant initialization still called legacy `dg-*` Services and failed with
`Remote client dg-discovery tenant initialization error ... Connection refused: dg-discovery-manager.keos-core`.
Root cause: governance keeps a registry of its apps in its own database (`dg_metadata.profile_*` in
`keos-core/postgreskeos`) with an owner id and absolute callback URLs per app; the cutover scales the legacy
Deployments to 0 but their Services stay with no endpoints; discovery's owner id changed
(`dg-discovery` to `governance-discovery`), so the new pod registered a second owner and failed halfway
(`duplicate key ... dictionary.discovery`); datamarket and ontology-graph kept their owner but refused to
re-register, so their callbacks stayed on `dg-*`. The cutover was also not safely re-runnable.

Commits: `862cdf6` (callbacks step), `32906e0` (end-to-end, idempotent, `pg_dump` backup, refuse while
legacy has replicas), `f3ca478` (Flux-label guard, registrants scaled to 0 during the rewrite with the
replica count saved in an annotation so an interrupted run recovers, tolerant verification),
`fdfe58b` (one wait-aware `migrate-governance` step; nine steps in all).

What `migrate-governance` does now: (1) checks the fleet declares governance (Kustomization
`keos-core/system-services-governance`); (2) removes the legacy stack (Ingresses, the immutable-selector
Deployments `governance-ui`, `governance-map`, `governance-datamarket-ui` unless Flux owns them, scales the
rest to 0 and keeps them for rollback); (3) waits up to 45 min per release for the HelmReleases
`governance`, `governance-datamarket`, `search-engine`; (4) reconciles and waits for Ready; (5) registry
migration only when rows need it: `pg_dump --data-only` of every `dg_metadata` table with an `owner`
column to `~/.fluxcd/flux-keos/backups/<cluster>/governance-registry-<ts>.sql`, scale the registrants to 0,
one `psql --single-transaction` that drops partial new-owner rows, renames the owner and rewrites callback
host prefixes (`dg-discovery-manager` to `governance-discovery-manager`, `dg-datamarket-api` to
`governance-datamarket-api`, `dg-ontology-graph-api` to `governance-ontology-graph-api`, **`dg-fs-api` to
`governance-datamarket-fs-api`**, a non-obvious rename), scale back up and wait; (6) read-only
verification on every run. `migrate-ingress` and `migrate-etcd-vault` now skip objects Flux recreated.

Reviewer notes: the rewrite is raw SQL against another team's schema, gated by `to_regclass(...)`, a
pre-count, the backup, one transaction, and refusal to merge a pre-existing new-owner row. Scale-to-0 of
the registrants is stateful (recovery relies on the annotation). On eosdev: 28 rows changed in the first run,
a re-run changed nothing, the final build ran the whole cutover with the one step. CI showed Jenkins and
`[UT]` failing when read (not investigated) and `main` has since moved, so a rebase may be needed. The
keos-fleet commit `36c16aa9` enabled governance `ontologyGraphApi` and `map` for this.

### 3.7 keos-fleet (`/stratio/gitops/keos-fleet`, direct pushes to `main`)

The eosdev tenant file is `clusters/eosdev/tenants/config/stratio.yaml`. The checkout may be shared with
other sessions: commit only your own hunks. Pushes to `main` print a branch-protection bypass notice
("Changes must be made through a pull request").

History in two passes. **Pass 1** (2026-09-22 to 10-09): iterative one-component commits (`migrate
postgres`, `migrate genai`, ...) and many "prepare bootstrapping" resets. **Pass 2** (10-13 to 10-14): after
`31f5af36` "prepare eosdev" reset the tenant file (about 716 lines to about 20, `components: []`) together
with the runtime secrets, the migration was redone in controlled waves W1-W7 using the tooling (section 5).
Wave commits, all touching only the tenant file:

| Wave | Commit | Apps | Notes |
|---|---|---|---|
| W1 | `3dae55d4` | psql (+gosec agent), pool-psql, hdfs1, kafka1, opensearch1 (+gosec agent), opensearch1-dashboards, pgbackuprepository | Operator-CR patches (JSON6902 or SMP) and two agent HelmRelease patches |
| W2 | `b6353ca1` | connectors, dg-hdfs-agent, eureka-agent, dg-datarest-pgi | |
| W3 | `af6df3f6` | virtualizer, discovery | |
| W4 | `ca7b768b` | litellm (entry name `genai-litellm`) | |
| F1 | `5bb7ee11` | dg-hdfs-agent `agent.config.common.customServiceName: hdfs1.stratio-datastores` | Needs charts `7eee574` |
| W5 | `64427b77` | genai | `llmModels` chat/governance `gpt-5.4-mini` |
| W6 | `779f83e1` | rocket, intelligence | Largest patch (rocket) |
| W7 | `8861e3cc` | governance-datamarket-agent, dlc-entity | |

Other eosdev facts: `31f5af36` is also where the earlier `dedafdf1` (virtualizer keeps the decoupled
HDFS and PostgreSQL connector jars) was wiped, so the virtualizer entry has no `connectorJar` config
(decision D6, section 10). `36c16aa9` enables governance ontology-graph and map. The tenant-level defaults
are `tenantName: stratio`, `size: S`, `universeVersion: 15.1.0-PR258-SNAPSHOT`,
`universePrereleaseSuffix: ""`.

Reviewer pointers: the patches deliberately keep legacy values over GitOps overlay values (section 9);
the diffs contain no secrets; the cluster-side manual steps (Vault copies, DB row rewrites, deleted legacy
Deployments) are invisible in git and recorded only in the commit bodies and in section 5/7 below. W5's
entry has `governanceOpenSearchEnabled: false` in `config` while its patch sets
`genaiUi.settings.generalProperties.governanceOpenSearchEnabled = True`; that is the legacy value kept on
purpose (decision D1/D2) but looks inconsistent. The rocket patch contains a Helm template string
(`enabled: '{{ eq .Values.server.type "rocket" }}'`) as an env entry copied from chart values and adds env
entries by list index; check how the chart renders them.

### 3.8 What eosdev depends on that is not merged

| Pinned in `clusters/eosdev/default/resourcesetinputproviders.yaml` | Value | Revert after |
|---|---|---|
| `keosApps` | `refs/heads/nojira/env-list-overlays` | keos-apps#152 merge |
| `keosUseCases` | `refs/heads/dgutierrez-stratio/PLT-4838-litellm-name` | keos-use-cases#202 merge |
| `keosSystemServices` | `refs/heads/dgutierrez-stratio/gosec-operator-memory` | keos-system-services#254 merge |
| tenant `universeVersion` (`stratio.yaml`) | `15.1.0-PR258-SNAPSHOT` | charts#258 release |
| `universe` | `version 15.1.*`, `prereleaseSuffix "-0"` | unchanged |

Other layers also moved during this period but are not part of this work: keos-system-services (Alloy and
Tempo replace fluentd, drift detection, size S/M/L for sizeless components, idp backup/restore, EKS items),
keos-use-cases (size suffix in component paths, IdpBackup restore wiring, vllm, EKS items), flux-keos (audit
items FK-01..07, AWS KMS, source-controller memory 2Gi), and an `eosdev2` tenant created by `admin-console`.

## 4. Chart variables versus the CCT descriptor (why legacy values differ from chart values)

The question that drove a day of investigation: *the chart's `values.yaml` is generated from the CCT
descriptor, so why do legacy variables go missing or change after migration?*

**Answer.** It is the same descriptor build; the chart simply covers less than the legacy install did.

```
legacy install  =  descriptor `parameters`
                 + lifecycle upserts (model/parameter/upsert in lifecycles.install, e.g. EXTRA_JARS)
                 + general.custom
                 + apps-layer-automation overrides (vars.env, *.json.tmpl, pre-install.sh)
                 + Command Center form inputs
chart           =  descriptor `parameters` only      (the `charts` CLI "only covers parameters")
```

**No version skew.** The legacy labels only say 15.1.0 / universe "15.1", so the build was fingerprinted
from env-key sets and image tags against the seven 15.1.0 builds (`b5e4b439`, `49ff2051`, `fb64d7f6`,
`bede7905`, `3c39ac44`, `80e63ea3`, `54ab1918`) plus the branch tip (15.1.1-SNAPSHOT). genai, virtualizer
and rocket legacy fall in `fb64d7f6`..`54ab1918`; dg-hdfs-agent in `80e63ea3`..`54ab1918`; intelligence in
`3c39ac44`..`54ab1918`. Every chart matches `54ab1918`. The eosdev HelmReleases use chart
`15.1.0-PR258-SNAPSHOT` from HelmRepository `helm-universe` (qa.int.stratio.com/repository/helm), whose
sha256 equals what Flux pulled on 2026-10-13 and the worktree templates including `0a1e649` and `feb37d7`.
The eosdev CCT image is `cct-universe:3.0.10`. Every `Chart.yaml` in the charts repo is `0.0.0`, so the
repo itself tracks no descriptor version.

**Mapping chain** for any variable: descriptor field -> `charts` CLI -> line in
`config/<flavor>_env_vars.yaml` -> `values.yaml` path -> keos-apps overlay / tenant file -> rendered
ConfigMap and env. The plugin renders from the **charts worktree** (`~/.fluxcd/flux-stratio/environment.yaml`
`repos.charts`), not from the chart the cluster pulled; `doctor` does not compare chart versions, which is
how a pushed-but-unpublished chart fix once looked like a diff.

**Root-cause classes** for the roughly 80 inventory rows (about 40 distinct names):

| Class | Meaning | Examples |
|---|---|---|
| H5 | Install input or deployment-repo default, not the descriptor default | genai-ui `OPENSEARCH_ENABLED` (descriptor false; legacy true from `apps-layer-automation genai-ui/vars.env:31`), `VIRTUAL_HOST`, `HA_PROXY_PATH` |
| H3 | Chart expresses it differently | dependency toggles, native probes, explicit `:5432` in `COMM_DS_URL`/`DATASTORE_HOST` |
| H2 | Descriptor concept the `charts` CLI skips (lifecycle upserts, `application:false` parameters) | virtualizer `EXTRA_JARS*`, rocket `CUSTOMER_SIZE`, `MIGRATION_STORAGE_PROFILE`, `NOT_USED` |
| H4 | keos-apps overlay | `GOVERNANCE_OPENSEARCH_*` (`helm.yaml:76-78`) |
| H6 | Deliberate | `VAULT_ROLE`, `dg-` to `governance-`, URLs |
| H7 | Chart did not map an existing parameter | `COMM_SERVICE_NAME` (`customServiceName`), `CUSTOM_STRATIO_CREDENTIALS` |
| H8 | Different workload altogether | `connectors` (16 rows): legacy `connectors-management` is a REST app (SQLite, JWT, Vault, image `connectors:2.2.0`); the chart `connectors` is a `connectors-loader` init plus a busybox httpd on port 80 |

(H1, version skew, was excluded. These H-numbers are the investigation's hypothesis classes, not the
H1-H6 finding numbers of the run report in section 10.)

**Per-variable conclusions worth keeping:**

- **`NOT_USED`** is a sink `internalName` shared by unrelated descriptor parameters. It carries no setting.
- **`CUSTOMER_SIZE`** is used only by `lifecycles.upgrade/pre` (`rocketbase.json:9855-9900`) to upsert the
  startup probe `failureThreshold` (120/240/480/720); the chart has a fixed native startup probe (120 x 5 s,
  `rocket/values.yaml:654-667`); `apps-layer-automation rocket/pre-upgrade.sh:67` sets 1000.
  `MIGRATION_STORAGE_PROFILE` is referenced by no lifecycle. The earlier claim that these and `*_PROBE_*`
  are "application-level" was wrong: they are real `application:false` parameters (`rocketbase.json:891-902`,
  `:943-1063`).
- **`*_PROBE_*`** variables are CCT parameters the descriptor turns into probes; the charts set the same
  values natively. Only gosec-agent readiness `failureThreshold` differs (chart 1, legacy and descriptor 3;
  history: `49fcc09` had 3, `4e33159` rewrote it as 1 with no stated reason).
- **`EXTRA_JARS`** is not a `parameters` entry: it is a `model/parameter/upsert` in
  `virtualizer-default.json` `lifecycles.install.start[26]` (and `update.start[16]`, `upgrade.start[16]`);
  legacy value `,,,,<sscc-hdfs url>,<sscc-postgresql url>,` (five slots). The connector-JDBC part was filled
  by `apps-layer-automation apps/virtualizer/pre-install.sh:32-66` only when
  `VIRTUALIZER_DECOUPLED_CONNECTOR_{POSTGRES,HDFS}=true` (default false; true in the `narnia` profile eosdev
  uses). The image entrypoint downloads the URLs onto the driver classpath
  (`/docker/lib/environments/context.sh:69`).
- **`SERVER_MAXHTTPHEADERSIZE=65536`** (virtualizer-monitor) is in no descriptor and not in the chart; it
  comes from `apps-layer-automation virtualizer-monitor.json.tmpl:24-28` (all clients since 14.8). Probable
  reason: large oauth2-proxy cookies. Whether Spring Boot 3 honours the property name was not verified.
- **`DISCOVERY_DATABASE`** is an integer Metabase database id (descriptor default 1; eosdev 3, depends on
  creation order). The catalog excludes it (`seed.go:319`, no recorded reason), so the patch drops it:
  check the live id by hand when migrating genai. The user decided to leave this exclude.
- **`COMM_SERVICE_NAME`** (dg-hdfs-agent): descriptor parameter `customServiceName`
  (`agent-abstract-connectors-default.json:335`); the chart hard-coded `{{ .Release.Name }}`
  (`dg-agent_common_env_vars.yaml:38`, since `29a4347`). Effect of the rename: Governance saw
  `datastoreName=dg-hdfs-agent` while GoSec and HDFS stayed `hdfs1.stratio-datastores`. Fixed by charts
  `7eee574` plus the tenant patch (F1).
- **`GOVERNANCE_OPENSEARCH_*`**: legacy enabled by descriptor default; keos-apps sets `${...:=false}` and
  `<cluster>-coordinator.keos-core` from the use-cases cluster name `opensearcher`. Both `opensearch1*` and
  `opensearcher*` exist in keos-core on eosdev. Chart quirk: `governanceOpenSearchServiceName` (no suffix,
  used by a GosecPolicy) vs `...Host` (with `-coordinator`, used in env).
- **Hosts and paths.** Legacy gave genai its own host (`genai.<tenant>...`, `/genai-ui`); GitOps puts every
  app on the admin host under `/<app>.<namespace>`. The plugin's patches for discovery, virtualizer, genai
  and dlc-entity keep the legacy public hosts, so they depend on the legacy Ingresses surviving (section 8).
- **Descriptor facts** are read at the release commit, never the branch tip:
  `git -C /stratio/p/kubernetes-universe show 54ab1918:packages/<...>`.

**Product decisions the investigation left open (D1-D6).** None was decided; each can be carried by the
migration patch (which always keeps legacy values), so they concern the tenant file and dependency graph.
Leanings are the investigation's, not decisions.

| # | Question | Options | Leaning |
|---|---|---|---|
| D1 | genai tenant entry: enable the `opensearch` and `discovery` dependencies? (genai-ui renders `OPENSEARCH_ENABLED=false`, hosts empty; legacy true) | A keep off and rely on patch values (current); B enable both (also changes `dependsOn`, a GosecPolicy, discovery postrequisites, and gives the host a `-coordinator` suffix) | none |
| D2 | Which OpenSearch cluster holds the governance indices on eosdev (`opensearch1` legacy vs `opensearcher-coordinator` GitOps)? | A accept GitOps, B preserve legacy via patch (current), C enable with `opensearcher` | none |
| D3 | Preserve virtualizer-monitor `SERVER_MAXHTTPHEADERSIZE=65536`? | A preserve (chart extra-env plus plugin mapping), B drop after confirming, C raise the chart default | A |
| D4 | rocket `CUSTOMER_SIZE`, `MIGRATION_STORAGE_PROFILE` and upgrade-time startup probes | A accept the chart, B express via keos-apps size overlays, C add the variables if the server reads them | open (ask the rocket team) |
| D5 | genai host and path (`genai.<tenant>` + `/genai-ui` vs admin host + `/genai-ui.stratio-genai`) | A accept new, B preserve via patch (needs ingress and IdP redirect work) | open |
| D6 | virtualizer connector jars on eosdev (chart side done in `feb37d7`; the tenant file sets no `connectorJar`) | A set both in the tenant file, B accept running without | open |

Implemented from the investigation: F1 (dg-agent `customServiceName`) and F2 (genai `serviceUrl` double
slash) in charts `7eee574`; F4 (native probe variables) in flux-stratio `ee558f6`. F3 (the
`DISCOVERY_DATABASE` exclude) was deliberately not done. Not done: a catalog "accepted differences" field,
a `doctor` chart-version check, a descriptor-release annotation in `Chart.yaml`.

**Alignment rules for the charts repo.** Use only the 15.1.0 release commit `54ab1918` (its parent
`7324e952` is identical except version strings); record both SHAs; never mix with the branch-15.1 tip. The
output of the `charts` CLI subcommands (`analyze`, `values`, `environment`, `validate`) is the
highest-accuracy source for paths, defaults and missing/removed classification; hand edits are acceptable
only where they coincide with the tool. Use the worktree; helm needs `helm dependency build <chart>` to
render; never commit the universe ZIP. Post-release (15.1.1) deltas deliberately **not** applied: rocket
4.1.1, governance 1.17.3, datamarket and opendata 2.4.x, connectors 2.2.1, genai 0.8.1-SNAPSHOT, dlc-entity
4.0.4-SNAPSHOT, intelligence 5.1.2-SNAPSHOT, virtualizer 1.1.1-SNAPSHOT, dg-custom-agent 1.2.1-M1/M2.


## 5. The migration process (what was actually done)

### 5.1 Starting state and roles

- eosdev was **reinstalled as a legacy (CCT) cluster** on 2026-10-13. Its system services were migrated
  first with `flux keos cluster migrate` (about 11:05Z), so the apps were already running next to a
  GitOps system layer. Backups taken at **11:00Z** (`2026-10-13T11-00-17Z`) are *pure legacy*: they predate
  that system-services migration.
- Earlier (2026-10-09) a first pass used `apps migrate --all` with every prompt answered `y`. An audit of
  that pass (12 findings) drove most of the plugin and chart fixes in section 3. Its results: 6 operator
  apps were identical to their backup after the patch, every value loss found had been announced by
  `apps migrate`, and the problems were around the patches (a prepare step that deleted objects it had
  not backed up, legacy agents left running next to their replacements, diff paths that broke on a name
  collision). The tenant file was then reset (`31f5af36`) and the migration redone in waves.
- Roles: the orchestrating model (Opus) ran **every state-changing command** and made go/no-go calls;
  read-only Sonnet subagents did dry-run reviews, verification and drafting, writing under the session
  scratchpad. Cluster actions were taken only with the user's approval and kept reversible.

### 5.2 Preflight (once)

```
git -C /stratio/gitops/keos-fleet pull --rebase            # origin had moved: 3 commits behind
flux stratio doctor                                          # binaries, catalog, env, repo revisions, vault, tenant file
flux stratio apps migrate --all --dry-run                    # survey: 20 apps OK
```

- `doctor` flagged **repo revision skew** (keos-fleet 3 commits behind, pulled; keos-use-cases and
  keos-system-services checkouts at other revisions, judged no app impact) and a stale catalog exclude
  (`genai: ...genaiGateway.general.identity.approlename`, which excludes nothing).
- The `--all --dry-run` survey prompted interactively for `dg-postgresql-internal-agent(-op)` (type
  `POSTGRESQL`, no GitOps component); the answer was *skip*. `--yes` fails on these, so a real run needs
  `--continue-on-error` or per-app invocations.
- **Vault inventory** (names only, no values): 26 tenant folders under `userland/passwords`; only two held
  underscored keys (section 8, "Vault underscores").
- **Legacy baseline noted**: eureka-agent was already crash-looping (35 restarts), caused by the
  system-services governance rename (section 8).

### 5.3 Scaffold and gate the tenant file

1. `flux stratio tenant import` scaffolded the tenant file in the working tree of keos-fleet: **19
   entries, no patches**. A copy was saved to
   `~/.fluxcd/flux-stratio/tenant-file-backups/stratio.yaml.imported-20261013T1300Z`. Origin still had
   `components: []`.
2. **Every entry was gated with `enabled: false`.** A render check
   (`flux-operator build rset` against the file) confirmed 0 tenant Kustomizations were produced.
3. A wave removed `enabled: false` from its entries only **together with** the patch written by
   `apps migrate`. The rule: **never push a component without its patch.** (`apps migrate` on an entry
   still `enabled: false` fails with "flux-operator build rset produced no Kustomization objects"; remove
   the gate first. The error does not say so.)

### 5.4 One wave

Waves were chosen by dependency order: data layer first, then governance agents and connectors,
virtualizer and discovery, litellm, genai, rocket and intelligence, datamarket and dlc.

```
# 1. review each app of the wave with the legacy workload still alive
flux stratio apps migrate <app> --dry-run -v
# 2. write the patch and run the app's prepare step (backup first, then destructive step)
flux stratio apps migrate <app> --yes --accept-warnings
#    (an app Flux already replaced: take the legacy values from the backup)
flux stratio apps migrate <app> --baseline <backup dir | latest>
# 3. manual legacy cutover where there is no prepare step: copy, then delete the legacy Deployment
kubectl -n <ns> get deploy <name> -o yaml > <copy>; kubectl -n <ns> delete deploy <name>
# 4. render check and push
flux-operator build rset ...                      # git diff must show only enabled lines and patches
git pull --rebase && git commit && git push       # keos-fleet main, ONE commit per wave
# 5. force the reconcile instead of waiting for the interval
flux reconcile source git flux-system
flux reconcile ks tenants                         # then the ResourceSet / HelmRelease
# 6. verify (read-only)
flux stratio apps diff <app>                                  # desired vs live
flux stratio apps diff <app> --drift latest                   # live vs newest backup
flux stratio apps diff <app> --drift <...>/2026-10-13T11-00-17Z
```

Notes on the comparison references: a prepare step takes its own backup, which moves `latest`. For apps
with a prepare step `--drift latest` therefore points at the moment before cutover (still legacy state), so
each app was also compared with the 11:00Z directory explicitly. After migration, `apps diff <app>` printing
"no differences" means the tenant file's existing patch already covers every difference; for genai,
rocket, intelligence and dlc-entity it adds a REVIEW that the live workload is Flux-managed, so the drift
comparison is the real check. Fourteen apps ended with "no differences"; six print residual items that
are all explained (psql operator defaults only, the two gosec agents' probe variables, connectors' legacy
workload comparison, dg-datarest-pgi's `HOST` fieldRef, datamarket-agent's scaled-down legacy workload).

### 5.5 The waves

| Wave | Commit | Apps | Prepare step / manual legacy action |
|---|---|---|---|
| W1 | `3dae55d4` | psql (+gosec agent), pool-psql, hdfs1, kafka1, opensearch1 (+gosec agent), dashboards, pgbackuprepository | None. Legacy `psql-agent` and `opensearch1-agent` scaled to 0 by hand after the replacements were Ready |
| W2 | `b6353ca1` | connectors, dg-hdfs-agent, eureka-agent, dg-datarest-pgi | `prepare-dg-agent` and `prepare-datarest` ran (backups 14-07-28Z, 14-07-31Z). Legacy `eureka-agent` and `dg-datarest-pgi` Deployments deleted by hand |
| W3 | `af6df3f6` | virtualizer, discovery | `prepare-virtualizer`, `prepare-discovery` (backups 14-14-29Z, 14-14-30Z) |
| W4 | `ca7b768b` | litellm | `prepare-litellm` first (Vault copy; backup 14-15-53Z), then push |
| F1 | `5bb7ee11` | dg-hdfs-agent `customServiceName` | `apps migrate --baseline` from backup 14-07-28Z; chart republish gotcha (section 8) |
| W5 | `64427b77` | genai | `prepare-genai` (SQL rewrite of 7 chain rows, **no backup taken first**); legacy genai-api/ui Deployments deleted by hand |
| W6 | `779f83e1` | rocket, intelligence | No prepare step; legacy Deployments deleted by hand |
| W7 | `8861e3cc` | governance-datamarket-agent, dlc-entity | `prepare-datamarket-agent` (suspend and scale legacy; backup 08-16-59Z), `prepare-dlc` (remove legacy Deployment and Ingress; backup 08-17-33Z; Ingress copy kept) |

Totals: 20 apps migrated in 7 waves plus one fix push; `dg-postgresql-internal-agent(-op)` are unmapped;
`pgbackuprepository` exists only in GitOps and was created new. Copies of every manually deleted legacy
object were kept in the session scratchpad (`logs/W2`, `W5`, `W6`, `W7`).

### 5.6 Verification after each wave

Delegated to read-only subagents, each returning a compact table: all Kustomizations and HelmReleases
Ready (Helm `v1` install, no rollback), pod restarts, `apps diff` and both drift comparisons, relevant
logs, HTTP checks of the new and legacy Ingress hosts, Vault state by hash only, database row counts.
Every wave passed; the findings are in section 10.

### 5.7 End state (2026-10-14, about 08:20Z)

All tenant Kustomizations and HelmReleases Ready; no non-Running pod in `stratio-*`; no `enabled: false`
left in the tenant file. Legacy deliberately left: `connectors-management` (jar URLs still point at it),
`rocket-catalog-standalone`, and at 0 replicas `psql-agent`, `opensearch1-agent`, `datamarket-agent`.
Legacy Ingresses for discovery, virtualizer, genai and intelligence keep serving. The operator-CR apps
(psql, pool-psql, hdfs1, kafka1, opensearch1, dashboards) **keep their `cct` labels and annotations**:
they are the same CRs, now Flux-managed, not leftovers.

### 5.8 Reusing the process on another cluster

1. `flux keos cluster migrate` first (system services, governance cutover); then wait for the governance
   Services before migrating apps that talk to `governance-*` (section 8).
2. `config init`, `doctor`, `tenant import` (create or complete), gate every entry `enabled: false`.
3. Back up everything (`apps backup --all`) and keep the directory: it is the only record of pure legacy
   state, and the input to `--baseline` and `--drift`.
4. Waves in dependency order; per app `--dry-run -v`, then `--yes --accept-warnings`; manual cutover where
   section 7 says so; one commit per wave; forced reconcile; verify against both backups.
5. Treat anything the plugin cannot carry as a decision, not a loss: classify it preserve / accept /
   decide, and record it.

## 6. Timeline of the eosdev run

| When (UTC) | Event |
|---|---|
| 2026-10-09 | First `apps migrate --all` pass and its audit (12 findings); governance cutover tooling in flux-keos (PLT-4989); keos-fleet `a3cc8605` "migrate batch" |
| 2026-10-13 ~08:29 | virtualizer connector-jar pins in the tenant file (`dedafdf1`), later wiped by the reset |
| 2026-10-13 11:00 | Backups `2026-10-13T11-00-17Z`: pure legacy |
| 2026-10-13 ~11:03 | `31f5af36` "prepare eosdev": tenant file reset |
| 2026-10-13 ~11:05 | System services migrated by flux-keos; `dg-businessglossary-api` scaled to 0 by the governance rename (legacy eureka and genai degrade, section 8) |
| 2026-10-13 ~14:01 | **W1** data layer pushed (`3dae55d4`; first push rejected because origin moved, rebased). 9 Kustomizations and 2 HelmReleases Ready at 14:04:49Z. 14:07Z legacy gosec agents scaled to 0 |
| 2026-10-13 14:07:47 | **W2** (`b6353ca1`). All four Ready about 3.5 minutes later. From 14:09Z the dg-hdfs-agent registers under the new name and HDFS discovery starts failing (TASK-14) |
| 2026-10-13 14:14:52 | **W3** (`af6df3f6`). Ready by 14:16:08Z, Helm v1, no rollback |
| 2026-10-13 14:15:53 | `prepare-litellm` backup; **W4** pushed 14:16:25Z (`ca7b768b`); `apps-litellm` Ready 14:18:07Z, post-requisites 14:24:33Z |
| 2026-10-13 ~14:35 | **Stop before genai** (user decision) because of the ConfigMap-null plugin bug (H1). 19 Kustomizations and 10 HelmReleases Ready. genai, rocket, intelligence, datamarket-agent and dlc-entity held (`enabled: false`) |
| 2026-10-14 early | H1 fixed in flux-stratio `b879e75`; H3 fixed in keos-apps `d39ed69` (Flux applied it; the Job was not re-run) |
| 2026-10-14 ~07:23 | charts `7eee574` snapshot republished; Flux kept the cached chart until the HelmChart was deleted; `apps migrate --baseline` then **F1** (`5bb7ee11`, 07:37Z); the rollout deadlocked and was cleared by deleting the old pod |
| 2026-10-14 08:04 | **W5** genai (`64427b77`); genai-ui restarted 5 times at startup (api not ready yet), then stable |
| 2026-10-14 08:15 | **W6** rocket and intelligence (`779f83e1`); rocket restarted once (transient KDC error); its child workloads were recreated |
| 2026-10-14 08:17 | **W7** governance-datamarket-agent and dlc-entity (`8861e3cc`) |
| 2026-10-14 ~08:20 | Final sweep: everything Ready. Run closed. The report was published (section 11) |

## 7. Per-component notes

Format: **GitOps name** (legacy name, catalog type) - prepare step / manual action; what changed against
legacy and why; quirks. "TASK-nn" refers to the audit tasks whose content is folded into this file
(section 11 explains how to recover them); TASK-13/14/15 still exist.

### Data layer (operator CRs; patch is SMP or JSON6902 on the CR)

- **psql** (PgCluster, `postgres`) - no prepare. The patch keeps instances, images, resources and storage
  sizes (the overlay-S sizes 40Gi/20Gi/10Gi are overridden back to the legacy ones). The GitOps size overlay `S` still
  adds five Postgres settings the patch cannot remove (`work_mem` 4MB to 2427kB, `max/min_wal_size`,
  `huge_pages` try to off, `default_statistics_target`); `huge_pages` leaves the cluster with status
  "needs restart" (`patroni_needs_restart`), no restart was done. **Backup is a hybrid**: the patch blanks
  the pgbackrest name and namespace, sets `walArchiver: false` and a posix repository, but the GitOps
  `backup.type: pgbackrest`, stanza and bootstrap S3 config remain; `psql` does not use
  `pgbackuprepository`, so GitOps physical backups are effectively off and the repository sits idle (the
  operator webhook accepting this combination is unverified). The patch carries about 15 operator-defaulted
  empty fields (noise).
- **psql-gosec-agent** (`psql-agent`, `postgres-gosec-agent`, chart `gosec-agent`) - legacy `psql-agent`
  scaled to 0 (names differ, so Helm does not clash and both would run). The legacy agent was already
  broken (pointed at the renamed `dg-businessglossary-api`). Eight `*_PROBE_*` variables (readiness
  `failureThreshold` 3 legacy vs chart 1, TASK-02). The `[ACTION]` selector message is misleading here.
- **pool-psql** (PgBouncer) - clean. Unrelated pre-existing log: FATAL database `testatdlc` does not exist
  for user `dg-postgresql-internal-agent`.
- **hdfs1** (HDFSCluster) and **kafka1** (KafkaCluster) - the overlay-S values `dfs.replication "1"` and
  `offsets.topic.replication.factor "1"` reach the CRs because a patch back-ports differing values, not the
  absence of a key (TASK-05). Both rolled once. Verdict recorded: reject on eosdev (preserve legacy
  defaults of 3), fix kafka's S overlay to `"3"` in keos-apps, keep hdfs S for a real 1-datanode tenant;
  not applied, user decision pending. Neither value affects existing data today (the kafka topic already
  exists at RF 3; HDFS clients get `hdfs-site.xml` without the key and HDFS is empty), but a fresh install or
  recreated `__consumer_offsets` would be RF 1. kafka `exposition.domain` is blank (legacy) instead of the
  GitOps domain, inert while not exposed. A transient datanode `DisallowedDatanodeException` during the
  namenode roll cleared on its own.
- **opensearch1** (OsCluster) - catalog excludes `spec.image` (only for this operator; others pin), so the
  image follows the operator default. The patch uses JSON6902 with positional `nodeGroups/N` indices, which
  is fragile if keos-apps reorders groups. **opensearch1-gosec-agent** (`opensearch1-agent`): legacy scaled to
  0. Before that, both agents ran together and flapped the OpenSearch metrics role (`prometheus` vs
  `metrics`, about 40 times in 10 minutes) until the legacy one was stopped. Gains
  `GOSEC_CONFIG_DECOUPLED=true`, `AGENT_CONFIG_NUMBER_THREADS=300`, `AGENT_CONFIG_DOMAINS_*` (the legacy psql
  agent already had them).
- **opensearch1-dashboards** - clean. **pgbackuprepository** - GitOps-only, created new, unused by psql.

### Governance agents and connectors

- **connectors** (`connectors-management`) - **different workload**: legacy is the `connectors:2.2.0` REST API
  (SQLite PVC, Vault, JWT), the chart is a busybox httpd plus the `connectors-loader` init. Sixteen
  variables are N/A. The plugin carried the legacy resources (250m/1536M requests) onto the tiny httpd pod
  whose keos-apps base sets 10m/16Mi on purpose (kept, wasteful). The artifacts directory held only loader
  jars, so nothing custom was lost. **Legacy `connectors-management` must keep running**: dg-hdfs-agent's
  `CUSTOM_SERVICE_SSCC_DRIVER_LOCATION` and eureka's `bdlAgentAdditionalJars` still point at its URL (the new
  service serves byte-identical jars: md5 equal, both return 200). `--drift latest connectors` is impossible
  because the backup is stored under `connectors-management`.
- **dg-hdfs-agent** (`dg-agent` chart, `agent.type=hdfs`) - `prepare-dg-agent` deletes the legacy Deployment.
  Values: `COMM_SERVICE_NAME` (kept legacy through `customServiceName`, F1), data-source driver URL moves to
  the connectors service, explicit `:5432`, `dg-` to `governance-` URIs, `VAULT_ROLE` underscore (all
  accepted by design); chart adds `DFS_*` filters and `SSCC_STRATIO_MULTI_HDFS_ENABLED=false`;
  `/var/stratio` downwardAPI mount dropped; `priorityClass` `default-nonpreempting` to
  `applications-normal`. `CUSTOM_STRATIO_CREDENTIALS` blank (charts `0a1e649`). At W2 `COMM_SERVICE_NAME`
  became `dg-hdfs-agent` (the chart hard-coded the release name), which created an empty duplicate
  Governance datastore row; F1 reverted the name; see section 10 and TASK-14. **Operational traps**: single replica with a 4 CPU request deadlocks a rolling
  update on a full cluster (section 8); the chart republish cache (section 8).
- **eureka-agent** - legacy crash-looped before migration (it called `dg-businessglossary-api`, scaled to
  0 by the governance rename); migrating fixed it. Legacy Deployment deleted by hand. `bdlAgentAdditionalJars`
  keeps the legacy connectors-management URL with its trailing comma.
- **dg-datarest-pgi** (`bdl-datarest`) - `prepare-datarest` deletes **only the legacy Ingress**, not the
  Deployment, yet the `[ACTION]` message implies it does; the Deployment was deleted by hand. The deleted
  Ingress is not in the plugin backup (TASK-01 defect A); a copy was saved by hand. New Ingress: same host
  and path, `pathType ImplementationSpecific`, adds `auth-tls-verify-client: "on"`. One `HOST` fieldRef
  cannot be written to the patch.

### Virtualizer, discovery, litellm, genai

- **virtualizer** (server, monitor, ui = one release; catalog `chart.siblings`) - `prepare-virtualizer`
  deletes the three legacy Deployments (found by family selectors; Ingresses, Services and the
  `virtualizer-loadbalancer` Service are left). Without it the HelmRelease hits RollbackFailed (seen on
  10-05). Losses announced and accepted for now: `EXTRA_JARS` (no `connectorJar` in the tenant file, D6),
  `EXTRA_JARS_SPARK_*` (empty no-ops), monitor `SERVER_MAXHTTPHEADERSIZE=65536` (D3), `VR_*` fieldRefs
  become literals (the plugin cannot resolve annotation fieldRefs; same value). Server and ui deliberately
  **keep the legacy Vault approlename** (the rocket and virtualizer exception to the GitOps role naming rule).
  New: SecretsBundles (kerberos principal, hazelcast, keystore), `PgDatabase` CRs, `imagePullPolicy` to
  `Always`, `revisionHistoryLimit` 0 to 3, `/var/stratio/namespace` mount dropped. **The legacy Ingress
  host of the UI returns 503** because its backend port name is `virtualizer-ui` while the chart's Service
  names it `ui`; the admin host works and the patch's `logoutAPI`/`monitorAPI` still use the legacy host.
- **discovery** - `prepare-discovery`. The patch keeps the legacy public host, so it depends on the legacy
  Ingress. Legacy discovery has a Metabase connection id 1 "Virtualizer"; the tenant dependency
  `virtualizer` is not `enabled: true`, so `apps-discovery-postrequisites` is not rendered, deliberately:
  enabling it would add a second connection ("Data Collections") because the keos-apps job name differs.
- **litellm** (`genai-litellm`) - `prepare-litellm` (section 8, Vault underscores). Entry name `genai-litellm`;
  `SERVER_ROOT_PATH` goes from `/genai-litellm` to `/genai-litellm.stratio-genai` and the host to the admin
  host (the legacy `genai.` Ingress no longer matches). The legacy database is kept
  (`genai-litellm.stratio-genai`); the chart also creates an unused empty `genai-litellm` database. Gosec
  groups keep the legacy names via the config map. The backup of a litellm holds only the PgDatabase CR, so a
  `pg_dump` was taken before W4. Post-requisites Job: the `sk-sk-` bug (fixed in code, section 10). Image
  `0.3.0-b7465f3` vs legacy `0.3.0`; chat returns 400 `use_xai_oauth` (open, section 10).
- **genai** (api, ui, developer-proxy) - `prepare-genai` rewrites 7 chain rows in database `genai`
  (`dg-businessglossary-api` to `governance-businessglossary-api`) after two stdin confirmations that
  `--yes` never answers; it took **no backup** first. `llmModels` taken from the legacy genai-ui env
  (chat and governance `gpt-5.4-mini`, translate empty). Legacy genai-api and genai-ui Deployments deleted by
  hand. After the H1 fix the patch carries the legacy genai-ui values (haproxy host and path, serviceUrl,
  opensearch, discovery) and the legacy resources (2048M and 512M). Catalog excludes for genai-ui:
  `GOVERNANCEAPI_FULL_NAME`, `GOVERNANCEAPI_URI`, `GOVERNANCE_URL`, `VAULT_ROLE`, `DISCOVERY_DATABASE`.
  genai-ui restarted 5 times at startup (api not ready), then stable. genai-api authenticates to LiteLLM by
  mTLS; the Service `genai-litellm` 4040/4041 selects the new pod.

### Rocket, intelligence, datamarket, dlc

- **rocket** - no prepare step; the legacy Deployment was deleted by hand. Its runtime-created children
  (`rocket-catalog`, `-debug`, `-externalservice`, `-validator`) are owned by it, were garbage-collected and
  recreated by the new server (the plugin maps none of them). Review items accepted: `SSCC_S3A_GLOBAL_CREDENTIALS`
  (harmless, HDFS storage), `CUSTOMER_SIZE` and `MIGRATION_STORAGE_PROFILE` (D4), tenant size S vs live M.
  The patch pins `PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN` because `cluster.domain` is shared with the Kerberos
  realm. Approle kept as the legacy one. **Five `SPARTA_CONFIG_CATALOG_*` variables are not delivered to any
  workload** (TASK-13). One transient KDC startup error; port name `pekkoseed` to `akkaseed` (cluster still
  forms). `rocket-catalog-standalone` (legacy) stays, orphaned.
- **intelligence** - no prepare step; Deployment deleted by hand. CPU request 100m to 1, memory limit 512M
  vs unset live (not written to the patch). **Admin Ingress returns 404**: rewrite target
  `/intelligence.stratio-intelligence/$2` vs `CONFIG_BASE_PATH=/intelligence`; the legacy Ingress on the
  rocket host works.
- **governance-datamarket-agent** (`datamarket-agent`) - `prepare-datamarket-agent` suspends the legacy
  HelmRelease and scales the legacy Deployment to 0. The HelmRelease name is pinned to
  `governance-datamarket-agent` whatever the entry is called (`23087cf`); the namespace changes to
  `stratio-apps`, so the selector `[ACTION]` is misleading. Gosec authorizes the new CN; seven
  `GosecRegisterCN` OK. The legacy `jwt_secret` is orphaned (section 8). The gosec-services-daas allow-lists
  still name `datamarket-agent.*`.
- **dlc-entity** - `prepare-dlc` deletes the legacy Deployment **and its Ingress** (the Python client used a
  hard-coded Ingress name and left the colliding one). Served on the admin host `/dlc-entity.stratio-dlc`
  (swagger 200). The old `dlc-entity.stratio.k8s...` URL now 404s, while `VIRTUAL_HOST`/`haProxyHost` in the
  patch still hold it (decision D5 class). History: on 10-09 `prepare-dlc` ran while `rocket` was undeclared
  and left DLC down; the Ingress was never backed up (TASK-01). In the 10-13/14 run dlc-entity was migrated
  normally in W7, after rocket.
- **dg-postgresql-internal-agent(-op)** - unmapped: type `POSTGRESQL` has no GitOps component (keos-apps
  `dg-agent` has only hdfs and s3 overlays; the chart can model it). They keep running as legacy.

## 8. Lessons learned and gotchas

### 8.1 Flux, Helm and kustomize

- **An empty `${VAR}` post-build substitution renders as YAML null, even when quoted.** Flux 2.9.2
  `flux build kustomization` turns `"${X}"`, `${X:-}`, `${X:=}` and `!!str` into null; only `${X:-""}`
  yields `""`. The chart then renders `KEY: {{ ... | quote }}` and sprig `quote` drops nil, so the
  ConfigMap key is null. The API server decodes a null into `map[string]string` as `""` (legal) but rejects a
  bool or number. This is what hid genai's real diff (keos-apps `components/genai/app/base/helm.yaml:71-75`,
  `${LLM_MODEL_*}` with eosdev `llmModels` empty). Other optional substitutions that could null the same way:
  `DISCOVERY_NAME`, `OSCLUSTER_NAME`, `GENAI_NAME`, `HDFS_NAME` (the plugin now warns which really do).
- **A republished chart with the same version is not re-pulled.** After charts `7eee574` was republished as
  `15.1.0-PR258-SNAPSHOT`, Flux kept the cached artifact (the HelmRelease uses `reconcileStrategy:
  ChartVersion`), even when forced to reconcile. Fix: delete the HelmChart object
  (`flux-system/<ns>-<name>`, here `stratio-datastores-dg-hdfs-agent`); helm-controller recreates it with the
  fresh chart. A pushed commit is not a published chart.
- **After Flux has replaced a live app, `apps migrate` sees nothing to migrate**: live already equals GitOps.
  Use `--baseline <backup dir>|latest` to take the legacy values from the backup. On an entry still
  `enabled: false` it fails with "flux-operator build rset produced no Kustomization objects"; remove the
  gate first.
- **Deadlock on a single replica with a large CPU request.** RollingUpdate with `maxUnavailable: 0` on a
  full cluster leaves the new pod Pending, Helm hits its 5-minute timeout, and the rollback deadlocks for
  the same reason (dg-hdfs-agent, 4 CPU). Resolved by deleting the old pod at the right moment. The chart
  should use `Recreate` or `maxUnavailable: 1` for single-replica agents.
- **Helm replaces lists wholesale.** An overlay or patch that sets a container `env` list shadows every
  chart default in that list (rocket `ROCKET_POD_NAME`). Atomic CRD lists (`nodeGroups`) look "unowned" in
  `managedFields` for the same reason. A kustomize merge patch also drops a `null` for a key not already in
  the HelmRelease values, so a patch cannot unset a chart default (intelligence's memory limit).
- **Helm adopts same-named legacy Services and ServiceAccounts**, so the Service selector flips to the new pod
  at install. **Same-named Deployments cannot be adopted** (immutable selector: CCT selects on
  `cct.stratio.com/application_id`, charts on `app.kubernetes.io/*`) and must be deleted first, otherwise
  the install fails and Flux's rollback then fails too, hiding the real error. Eureka-agent, dg-datarest-pgi,
  genai-api/ui, rocket and intelligence were deleted by hand (copies kept). Owned children of rocket are
  garbage-collected and recreated by the new server. Differently named duplicates (psql-agent vs
  psql-gosec-agent) do not clash and just run twice, so they were scaled to 0.
- **Legacy Ingresses keep serving after migration** and break when a chart renames a Service port
  (virtualizer-ui returned 503 because the legacy backend names the port `virtualizer-ui`, the chart `ui`).
  Patches that keep legacy hosts depend on those Ingresses surviving; litellm excludes its host (an
  inconsistent policy).
- **A Job whose spec did not change is not re-created by Flux.** The fixed litellm post-requisites script
  reached the cluster in a ConfigMap but the completed Job did not re-run; deleting the Job makes Flux
  recreate it.
- **ConfigMap `envFrom` order matters**: the last one wins on a duplicate key (`<release>-<storage>-config`
  after `common-config`). Two ConfigMaps defining one key depend on that order.
- **The HelmRelease/Kustomization object name and `spec.releaseName` are different things.** Renaming the
  object breaks every `dependsOn`; only `releaseName` carries identity (litellm).
- **Helm remediation can take a running legacy app down.** When the gosec-operator webhook had no endpoints,
  the genai HelmRelease install failed and Helm's remediation uninstall removed the running legacy
  genai-api/ui.

### 8.2 Vault and secrets

- **Underscored Vault keys and the SecretsBundle.** The chart's `SecretsBundle` declares **hyphenated**
  names (the secrets operator rejects underscores) and **deletes every key in its Vault folder that it does
  not declare** (subfolders excepted; `secrets-operator/pkg/bundle/passwords.go:36-51`). `userland/` is kv v1:
  a deleted value is gone. The operator keeps an existing value only if its length matches the declared length
  and it has no `user` field (`pkg/password/generic.go:96-121`). Of 26 tenant folders only two had
  underscored keys:
  - `genai-litellm.stratio-genai`: `master_api_key` (13), `db_salt_key` (32), `keos_key_secret` (32). The chart
    declares hyphenated names of the same lengths. `prepare-litellm` backs each original verbatim to a
    `legacy/` subfolder (which the bundle never prunes) and copies the value to the hyphenated name before the
    Deployment is deleted; it never writes or deletes the originals. After the first reconcile the bundle
    **did** prune the underscored originals, exactly as predicted, but the hyphenated copies had equal hashes, so
    the salt survived: no `Error decrypting`, `/credentials` 200, the DB still held its 1 model, 1 credential, 2
    tokens, 1 team, 2 users. Without the step the salt would have been regenerated and every credential
    encrypted with the legacy salt would stop decrypting. Earlier (10-06) the same failure happened on eosdev
    and was repaired by hand per row, because the originals were already gone.
  - `datamarket-agent.stratio-datastores`: `jwt_secret`. The GitOps release writes a different folder
    (`governance-datamarket-agent.stratio-apps`, key `jwtsecret`, fresh random value). No consumer of the old
    value was found and no jwt error appeared after W7, so the loss was judged harmless; the old folder is
    orphaned.
- **When an app's id or namespace changes, its Vault folder changes and secrets are generated fresh**
  (datamarket-agent to governance-datamarket-agent; connectors-management to connectors, which uses no Vault).
  Folders were unchanged for dg-hdfs-agent, eureka-agent and dg-datarest-pgi. Check this per app.
- **`VAULT_ROLE` changes from hyphen to underscore** on six apps (`stratio-datastores-dg-hdfs-agent` to
  `stratio-datastores_dg-hdfs-agent`) by design; pods authenticated fine. Virtualizer server and ui and
  rocket deliberately keep the legacy role (section 9).
- **The `sk-` rule.** The genai-litellm image entrypoint adds `sk-` to the master key **only when missing**
  (genai-litellm commit `7b695f5`, which also added `GENAI_LITELLM_VAULT_*_SECRET_NAME`). The keos-apps
  post-requisites Job prepended unconditionally (finding H3), so with the carried legacy key it sent
  `sk-sk-...`, got 401, `jq` printed "Cannot iterate over null", and the Job still ended Complete. Fixed in
  keos-apps `d39ed69`. Stripping the prefix in `prepare-litellm` was rejected: it breaks the byte-identity of
  the key and the length-13 check would make the operator regenerate (rotate) it.
- **Secret hygiene.** Backups hold raw manifests that can contain inline credentials (0700/0600 files). The
  trace of the old Job printed the master key into a pod log (the 10-13 pod's log still holds it; rotation
  undecided). A read-only subagent once printed a Postgres password file into its transcript and left a helper
  script in a pod (removed); rotate if transcripts are kept. This file contains no secret values.
- **LiteLLM specifics**: it caches credentials, users and teams in memory (every DB-side delete needs a pod
  restart before a register/job sees it). genai-api authenticates to it by mTLS; its keos layer maps the
  certificate identity to a key derived under `keos-key-secret`, which is why a rotated secret produced 401
  `token_not_found_in_db` (hypothesis, not verified in source).

### 8.3 The governance rename

The rename of governance components (`dg-*` to `governance-*`, including discovery's owner id
`dg-discovery` to `governance-discovery`) is a **deliberate decision**; leftovers are migration bugs,
fixed in the migration tooling, never reverted and never filed as governance-team bugs.
Consequences seen on eosdev:

- The system-services migration (about 11:05Z) scaled `dg-businessglossary-api` (keos-core) to 0 and left its
  Service without endpoints. **Legacy apps that still pointed at it degrade until each is migrated**: legacy
  eureka-agent crash-looped (35 restarts at preflight, 51 later; `BDL_GOVERNANCE_BASE_URI` and
  `GOVERNANCE_DEPLOYMENT` pointed at it), legacy genai-api logged about 20 "Error calling Get Glossary Items
  API" per chain every 30 minutes, legacy dg-hdfs-agent logged health-check errors. Migrating fixed each (the
  charts render `governance-*`). Ordering lesson: migrate governance-dependent apps right after the rename, or
  keep the old Service alive.
- Governance's own registry (`dg_metadata.profile_*`) stored callback URLs on the old names: handled by
  flux-keos (section 3.6). For genai the stored chain configuration is rewritten by `prepare-genai`.
- Governance data is changed only through the Governance API, never in its database (except the registry
  rewrite above, which is backed up and gated).

### 8.4 The plugin: what its output does and does not tell you

- **`--baseline` and the diff are one-directional.** Keys present only in the GitOps render are skipped, so
  "no differences" means "nothing needed to be back-ported", not "legacy config was kept". GitOps-only
  additions from an overlay (the `S` profile on hdfs, kafka, psql) are invisible to `apps diff`,
  `apps migrate` and `--yes`; `--drift` shows them.
- **Env-diff false negative (TASK-13).** `internal/diff/envvars.go:97-101` credits any ConfigMap no workload
  references to the chart's lone workload, so a variable in an *orphaned* ConfigMap counts as delivered.
- **Per-workload comparison.** Sibling workloads (genai-api/ui, virtualizer/monitor/ui) set the same
  variable names; a flat name-to-path map wrote genai-api's role into genai-ui (crash loop, Vault 403). Always
  attribute per workload (`AttributeConfigMaps` matches each rendered ConfigMap to the chart file with exactly
  its keys).
- **Never `NestedStringMap` on rendered data** (section 3.1).
- **Prepare ordering is a safety property**: plan, confirm, **save the patch first**, then run the destructive
  step (prepare-dlc deletes the Deployment the patch is read from). Steps back up first, but the backup covers
  the workload or CR, not the objects the step deletes (a deleted Ingress was not captured; prepare-genai
  took no backup before its SQL).
- **After cutover Helm strips the CCT annotations**, so an already-migrated app is resolved from its
  HelmRelease; classification (`--all`) uses catalog selectors only. A renamed app's backup lives under its
  legacy id.
- **Misleading `[ACTION]`.** "Selector is immutable, delete the legacy workload; only an app with a prepare
  step does it" is printed even when the new and legacy objects have different names (gosec agents,
  datamarket-agent) and is not accurate for dg-datarest-pgi, whose step deletes only the Ingress (TASK-15).
- The component-level `enabled` flag of a tenant entry is read by the keos-use-cases ResourceSets, not by Go
  code; `apps migrate --all` skips entries that are commented out or absent.

### 8.5 Operations

- Force the reconcile after a push instead of waiting for the interval.
- keos-fleet's checkout may carry another session's uncommitted hunks (a stray `genai enabled: true` with no
  patch appeared): commit only your own.
- GoSec: two CRs sit in ERROR 409 because the name already exists in GoSec as a *disabled* policy/role
  (`gosecpolicy genai-stratio-genai-opensearch1-stratio-datastores`, `gosecrole
  editor-genai-genai-api.stratio-genai`); the operator's lookup skips inactive ones.
- Pre-existing and unrelated to the migration: `capsule-system` HelmRelease "Helm uninstall failed ...
  pre-delete hook"; pool-psql FATAL database `testatdlc`; keos-core `postgreskeos-scheduled-physical-backup`
  failing since 12:00Z.
- The `gosec-operator` memory limit (75Mi) is too low for a migrated component's Gosec resources (PR #254).

### 8.6 Where the notes disagree (contradictions resolved)

| Topic | Older source | Resolution used |
|---|---|---|
| How many apps migrated | TASK-00 index: "14 apps", several held; memory note: five held pending the null fix | 20 migrated, none held (ledger 2026-10-14). The index was written for the 10-09 pass |
| dlc-entity | TASK-01 and a memory note: left down until rocket is enabled | Superseded: migrated in W7 after rocket |
| litellm Vault handling | `4729ab3` (09-29): legacy secrets not reused, rows cleaned and models re-registered | Reversed by `c7b85a0` (10-08): preserve legacy via `prepare-litellm` |
| `sk-` status | TASK-06 status line: "analysis only, no repo change" | Fixed in keos-apps `d39ed69`; the Job has not re-run |
| W1 push time | Ledger: about 14:05Z | git stamp `16:01 +0200` = 14:01Z; git used |
| Entries vs apps | 19 entries scaffolded by `tenant import`; "20 apps" migrated | Different counting (entries vs apps including nested gosec agents and pgbackuprepository) |
| `EXTRA_JARS` "not gaps" | `TASK-charts-universe-alignment`: not gaps | Wrong for the JDBC part; corrected (charts `feb37d7`) |
| PR #1 body | 19 catalog types, four prepare steps | Code has 20 types and eight steps |
| `CUSTOMER_SIZE`, `*_PROBE_*` | Alignment task: "application-level" | They are real `application:false` parameters (TASK-11) |
| `apps diff --patched` | `207e582` added it | Removed again in `8c84854`; `apps diff` compares the tenant file as it is, only `migrate` strips patches |

## 9. Decisions and conventions

These are the project's standing rules. Several look like bugs to a newcomer; they are not.

| Decision | Rule and where it lives |
|---|---|
| **Migration preserves legacy** | The patch must reproduce the legacy app's live configuration, **even where a GitOps size overlay sets another value** (e.g. rocket's `SPARTA_BOOTSTRAP_*` sizing 2/0.2/1024 vs overlay 1/0.5/1536). Aligning the product with GitOps conventions is a later, separate intervention on the client's environment. All resource differences are carried (requests and limits together, a limit the legacy never had is written `null`), verified by re-render; image tags, replicas, probes, priorityClass and selectors are warnings only and are never patched |
| **Evidence for each difference** | Classify every difference *preserve legacy / accept new / needs product decision*, never assume a direction. A long list of differing variables is a list to investigate, not a list of bugs |
| **Catalog excludes are deliberate** | `exclude` paths in `internal/config/seed.go` keep a GitOps value on purpose: `approlename`/`VAULT_ROLE` per workload, networking and SSO URLs, genai-ui `GOVERNANCEAPI_FULL_NAME`, `GOVERNANCEAPI_URI`, `GOVERNANCE_URL`, `DISCOVERY_DATABASE`, opensearch `spec.image`, the gosec-agent registration names. Excluded differences are listed as information, never a warning, so `--yes` is not stopped. Do not add litellm's `liteLlm.general.gosecIntegration.*` to excludes |
| **Vault role naming** | Use the GitOps `approlename` (underscore, `stratio-datastores_dg-hdfs-agent`), not the legacy hyphenated role; keep the legacy role only when migrating breaks something (e.g. database access) and changing it back is the only way out. Open exception, left as is on the user's instruction: rocket (`rocketCommon`/`rocketCatalog.security.approlename`) and virtualizer server and ui keep their legacy roles; do not add those excludes unprompted |
| **Governance rename is ours** | `dg-*` to `governance-*` (Services, Deployments, discovery's owner id) is kept. Leftovers are migration bugs fixed with data rewrites in the tooling (flux-keos `migrate-governance`, flux-stratio `prepare-genai`). Do not propose reverting chart names or file bugs with the governance team |
| **Per-component prepare steps, not a generic one** | The cutover of legacy workloads is the operator's process, outside the plugin's scope (CONTEXT.md section 2). The user rejected a generic "delete the legacy workload" step. Component-scoped steps (`prepare-dlc`, `prepare-virtualizer`, `prepare-litellm`...) are fine **when requested, app by app**; each must state which objects it deletes or scales and which it leaves. `replaceLegacyDeployment` builds one named step; it triggers nothing by itself. Which further apps get a step is TASK-15 |
| **`enabled` gating** | Three different senses, do not conflate: (1) a tenant-file dependency `config.dependencies.<dep>.enabled` (default false in the ResourceSets; `tenant import` writes `true` on positive evidence from `chart.uses` or an assumed live use; a hand-written `false` is kept with a warning); (2) `chart.enabledBy` such as `uiEnabled`, scaffolded `true` when a sibling workload runs live, so a missing key cannot make the next release remove the sibling workloads; (3) an entry-level `enabled: false`, used in the eosdev run to stage components, read by the ResourceSets. Never push a component without its patch |
| **Backups first** | Take `apps backup` before touching anything and keep the pure-legacy directory; prepare steps take their own; data rewrites need a backup first (flux-keos does a `pg_dump`; `prepare-genai` does not yet) |
| **flux-keos is self-sufficient** | `flux keos cluster migrate` steps must be idempotent and include every restart, scale-down, removal of a blocking legacy object, HelmRelease reconcile/wait and data migration the cutover needs, back up before a data rewrite, refuse unsafe preconditions, keep legacy workloads at 0 rather than deleted, and end with a read-only verification that fails loudly |
| **No persisted plugin state** | Every command re-derives from the cluster and the templates; `stdout` is data, `stderr` narration; `apps migrate` never commits or pushes; confirmations go to stderr |
| **Litellm naming** | Release name carries identity (`genai-litellm`); the HelmRelease/Kustomization object names stay `litellm` / `apps-litellm`; Vault secret names are not configurable (bundle pruning) |
| **Charts repo** | One branch, one worktree, one PR, one commit per chart and concern, `[NOJIRA] Align <chart> with kubernetes-universe 15.1.0 release (<what>)`, a CHANGELOG line under "15.1.0 (upcoming)". Reference = `54ab1918` only, final tags only. `charts` CLI output outranks hand edits |
| **Tenant file edits** | Textual insertion guided by the yaml.Node tree, never a re-encode: comments, indentation and commented-out blocks survive; `tenant import` on an existing file is add-only and idempotent (a second run writes zero bytes). `--force` regenerates and drops hand-written content |
| **Process during the run** | The orchestrator ran every mutating command; subagents were read-only; a push to keos-fleet is followed by a forced Flux reconcile; one commit per wave; secrets are never printed (hash prefixes and lengths only) |

## 10. Known open issues (context, not a tracker)

State at the end of the run (2026-10-14 about 08:30Z): **everything is migrated and Ready**. What remains
are fixes and decisions on the migrated state. Numbering H/M/L is the run report's (section 11).

**Tracked in the three kept task files**

- **TASK-13 (report H4)**: the rocket chart renders `SPARTA_CONFIG_CATALOG_{ASSUME_ALL_DATABASES_GOVERNED,
  CACHE_EXPIRE_AFTER_ACCESS,CACHE_REFRESH_INTERVAL,OPERATIONS_PARALLELISM,OPERATIONS_TIMEOUT}` into the
  ConfigMap `rocket-catalog-config` (`rocket/config/rocket_catalog_env_vars.yaml:17-21`), mounted only when
  `server.type=catalog`. The rocket server and the four children it spawns lack them (legacy had all five on
  the server and on every child). Fresh installs are affected too. The "Operation not authorized" error seen
  in the rocket log is unrelated: GoSec denies `SHOW DATABASES` to `execution-identity-rocket` on database
  `default`. Part B is the plugin false negative (section 8.4).
- **TASK-14 (H5, M9)**: dg-hdfs-agent. The GosecPolicy `dg-hdfs-agent.stratio-datastores-hdfs-acl` is in ERROR
  (HTTP 500) because gosec-services-daas throws a NullPointerException on an ACL that has only `actionsDeny`
  (the `/user` deny, `DtoUtils.java:343-344`; `dg-agent/values.yaml:97-117`). There is **no id clash**: the
  operator adopted the legacy policy. It does **not** cause the discovery failure: the namenode allows `/`
  but denies `/user`, the only child, so the extraction is empty and is refused because
  `DFS_ALLOW_EMPTY_EXTRACTION=false` (same as legacy). Why the legacy request at 10:12Z succeeded is
  unexplained. Two other CRs are in ERROR 409 (name exists in GoSec as a disabled object). The W2 name change
  created an empty duplicate Governance datastore (row 901 `dg-hdfs-agent` next to 101
  `hdfs1.stratio-datastores`); delete it through the Governance API once extraction works.
- **TASK-15 (M2)**: which apps need a component-scoped prepare step. Table of what was done by hand per app;
  proposals per app; the `[ACTION]` wording fix and a `prepare-genai` backup (M12) need no per-app approval.

**Open, not tracked in a task file**

| Item | Context |
|---|---|
| H2 litellm chat 400 `Unknown parameter: 'use_xai_oauth'` | The legacy model row (created by the legacy UI on image `0.3.0`) stores `use_xai_oauth=false` plus three more `litellm_params`; image `0.3.0-b7465f3` forwards them upstream. Likely an image build regression; not reproduced or ruled out for genai chat after W5 |
| H3 post-requisites Job not re-run | Fixed in code (`d39ed69`); the 10-13 Job pod's log still contains the master key; rotating it is undecided |
| H6 rollout strategy | Single-replica agents need `Recreate` or `maxUnavailable: 1` in the chart |
| genai-api chain workers OOM-killed (M10) | "exceeded the memory limit": 17 kills in 15 minutes, none after 08:15:29Z, pod at 1801Mi of 2048M (about 92%); the limit equals legacy; recheck, then decide |
| intelligence admin Ingress 404 (M11) | Rewrite `/intelligence.stratio-intelligence/$2` vs hub `CONFIG_BASE_PATH=/intelligence`; the legacy Ingress works |
| virtualizer-ui legacy host 503 (M1) | Port name `virtualizer-ui` vs `ui`; patch URLs still use the legacy host |
| dlc-entity legacy URL dead (L21) | `prepare-dlc` deleted the host Ingress; `VIRTUAL_HOST`/`haProxyHost` still hold it |
| connectors-management cannot be retired (M4) | Repoint `customSSCCDriverJar` and `bdlAgentAdditionalJars` at the connectors service (jars are identical) |
| connectors carries legacy resources onto an httpd pod (M5) | Exclude resources for `connectors` or detect a workload whose image changes role |
| discovery and virtualizer integration (M6) | Enabling the postrequisites would add a second Metabase connection; the keos-apps job should adopt the existing "Virtualizer" one |
| psql backup hybrid and pending restart (M7) | Choose full legacy or full GitOps backup; schedule the `huge_pages` restart |
| hdfs and kafka replication "1" (M8, TASK-05) | Verdict "reject" recorded, fix kafka S to `"3"` in keos-apps; hdfs S kept; JSON6902 `remove` ops for eosdev are available when scheduled; not applied |
| prepare-genai takes no backup (M12) | The 11:00Z backup is the only pre-cutover copy of the 7 chain rows |
| gosec allow-lists still name `datamarket-agent.*` (L22) | New CN is covered (`authorized=true`) |
| `rocket-catalog-standalone` orphaned (L24); legacy `psql-agent`, `opensearch1-agent`, `datamarket-agent` at 0 | Retire after confirming nothing uses them; remove the orphan `jwt_secret` folder |
| Decisions D1-D6 (section 4); `DISCOVERY_DATABASE` exclude; genai `governanceOpenSearchEnabled` consistency | Product decisions pending |
| Secret hygiene | Rotate the Postgres passwords a subagent printed (if transcripts are kept) and decide on the litellm master key |
| Unmerged dependencies (section 3.8) | Merge order and revert of the eosdev refs; charts#258 conflicts with master; flux-keos#44 CI and rebase |

**Plugin improvements proposed during the audit, not implemented** (for whoever picks them up):
serialise every object a prepare step deletes into the backup and fail if one has no file (TASK-01 A);
refuse or escalate a prepare step when the review reports an undeclared dependency (TASK-01 B);
optional two-phase `--all` (plan all, one confirmation, apply); a charts-version check in `doctor`
(TASK-03); report GitOps-only keys and say when a migration will restart workloads (TASK-05); resolve
`metadata.annotations[...]` fieldRefs and treat comma-only live variables as notes (TASK-04); map legacy
`EXTRA_JARS` URLs to structured `connectorJar` values; a catalog "accepted differences" field; backup
resolution by legacy id for renamed apps; litellm backup with a DB dump; classification collision with a
same-named `PgDatabase` (`apps diff dlc-entity` errors in every mode, TASK-00 row 06); non-interactive skip
of unmapped types; flags for `prepare-genai`'s two stdin questions; a clearer error on an `enabled: false`
entry; filter operator-defaulted fields from CR patches; match JSON6902 `nodeGroups` by name, not index.

## 11. Glossary and pointers

**Glossary**

| Term | Meaning |
|---|---|
| CCT / Command Center | The legacy Stratio deployment engine; objects carry `cct.stratio.com/application_{id,name,service,model,tenant,version,domain,created_by,generation}` |
| Universe, descriptor | `kubernetes-universe` repo; a descriptor is a CCT JSON with `parameters`, `data` and `lifecycles` for one `service:model`; 15.1.0 is the release the charts follow |
| Tenant, tenant file | A customer namespace set (`stratio` on eosdev); the file is a `ResourceSetInputProvider` (RSIP) listing `components` |
| ResourceSet | flux-operator object (keos-use-cases templates) that renders Kustomizations from an RSIP; `flux-operator build rset` renders it locally |
| Wave | One dependency-ordered batch of apps migrated, tested and pushed together (W1-W7, F1) |
| Prepare step | Component-scoped, plan-then-apply removal of legacy objects before Flux installs the chart |
| Catalog | flux-stratio's `catalog.yaml`: component types, selectors, excludes, prepare steps |
| `--baseline` / `--drift` | Diff a rendered desired state against a backup / diff live now against a backup |
| SecretsBundle / SecretsIdentity | Secrets-operator CRs that own a Vault folder of passwords and its identities; a bundle prunes undeclared keys |
| GoSec | Stratio security stack: `gosec-services-daas` (API), `gosec-operator` (CRs `GosecUser/Policy/Group/Role/Binding/RegisterCN`), per-datastore agents (`gosec-agent` chart) |
| Governance | Stratio Governance (`dg_metadata` registry in `keos-core/postgreskeos`); `dg-*` legacy, `governance-*` GitOps |
| Dyplon | Name that appears in the namenode's `getListing` denial for the agent's HDFS access (TASK-14) |
| SSCC, DS driver | Connector jars (sscc-hdfs, sscc-postgresql) the agents and virtualizer load; served by `connectors` |
| Operator CRs | `PgCluster`, `PgBouncer`, `HDFSCluster`, `KafkaCluster`, `OsCluster`, `OsDashboards` |
| H1-H6, M1-M12, L1-L27 | Finding numbers of the published run report (below), by severity |
| `Z` | UTC |

**Paths**

| What | Where |
|---|---|
| Plugin backups | `~/.fluxcd/flux-stratio/backups/<app>/<UTC timestamp>/`. Pure legacy: `2026-10-13T11-00-17Z`. Prepare-time ones: `14-07-28Z` dg-hdfs-agent, `14-07-31Z` dg-datarest-pgi, `14-14-29Z` virtualizer, `14-14-30Z` discovery, `14-15-53Z` litellm (all 2026-10-13); `08-16-59Z` datamarket-agent, `08-17-33Z` dlc-entity (2026-10-14) |
| Tenant file before staging | `~/.fluxcd/flux-stratio/tenant-file-backups/stratio.yaml.imported-20261013T1300Z` |
| Catalog and environment | `~/.fluxcd/flux-stratio/catalog.yaml`, `environment.yaml` (repo paths under `repos:`) |
| flux-keos backups | `~/.fluxcd/flux-keos/backups/<cluster>/` (`governance-registry-<ts>.sql`) |
| Tenant file | `/stratio/gitops/keos-fleet/clusters/eosdev/tenants/config/stratio.yaml` |
| Cluster-wide refs | `/stratio/gitops/keos-fleet/clusters/eosdev/default/resourcesetinputproviders.yaml` |
| Published run report | https://claude.ai/artifact/HZ2EP4bH7ve4abZ7w8VAkm (per-app table, findings H/M/L, vault hash table, evidence locations) |
| Run ledger and logs | session scratchpad (`findings.md`, `logs/W1..W7`, `vault/`, `litellm/genai-litellm-pre-W4.dump`); temporary, not in any repo |
| Product docs | `README.md`, `CONTEXT.md` (design and package reference), `CHANGELOG.md`, `docs/migration-runbook.md`, `docs/config-reference.md` |
| Add a component type | skill `.claude/skills/add-catalog-component/SKILL.md` (use when `apps diff <name>` says "none is selected by any catalog type") |
| Removed audit tasks | Committed versions: `git show HEAD:docs/<file>` (and `git log --all -- docs/<file>`). Uncommitted edits to TASK-00, 03, 11, 12 and the two tenant-import tasks were folded into this file before removal |

**Useful commands**

```
flux stratio doctor
flux stratio apps backup --all                                   # or <name>; --dir to choose the root
flux stratio apps diff <app> [-v] [--view unified|patch|meld]
flux stratio apps diff <app> --baseline <backup dir|latest>
flux stratio apps diff <app> --drift    <backup dir|latest>
flux stratio apps migrate <app> --dry-run -v | --yes --accept-warnings | --baseline <dir> | --skip-prepare
flux stratio apps migrate --all --dry-run --continue-on-error
flux stratio tenant import --output <file> [--dry-run] [--size S|M|L]
flux-operator build rset ...                                      # render a tenant file against the ResourceSets
flux reconcile source git flux-system; flux reconcile ks tenants  # then the HelmRelease
kubectl -n flux-system delete helmchart <ns>-<name>               # force a same-version chart re-pull
git -C /stratio/p/kubernetes-universe show 54ab1918:packages/<...> # a descriptor at the 15.1.0 release
python3 bin/migration/migrate.py charts analyze --zip-file <zip> -s <svc> -m <model> -v <ver> [--scaffold|--data|--lifecycles]
make fmt-check vet lint test build                                # flux-stratio
```

Never print secrets from backups or Vault; compare by length and hash prefix.
