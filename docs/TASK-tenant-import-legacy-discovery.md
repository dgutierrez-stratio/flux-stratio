# Task: `tenant import` must discover the tenant from the legacy (CCT) cluster, not from Flux objects or chart names

**Status (2026-10-13): implemented, see CHANGELOG ("`tenant import` scaffolds a legacy tenant file that validates"). On eosdev,
`tenant import` from an empty `components:` now produces a file the validator accepts (27 Kustomizations render).** What was done,
against this doc's "What to build": 1 (discovery through `components.Classify`, `internal/tenantimport/classifiedscan.go`), 2 (entry names from
the catalog's `entry` template), 3 (seeded `connectors` type; `doctor` warns when `catalog.yaml` lacks a seed type), 5 and 6 (dependencies resolved
against the scan *and* the file; `dgAgent` `type` and the governance dependencies from the legacy env through the new `configFromEnv` catalog field; the two
`POSTGRESQL` agents are left out with a warning), 7 (`pgbackuprepository` scaffolded as GitOps-only, with a warning), 8 (additions only, idempotent).
Item 4 was done differently: with no HelmRelease, a dependency whose component runs on the cluster is assumed used (`enabled: true`, said so), not read
from the legacy env (the env-var evidence this doc describes in section 4 is still the more precise source, and still open). Also found and fixed: the
Complete merge wrote new entries without the dependencies the scan could not resolve even when the file declared the target (`merge.go`
`completeUndeclared`), and the sixth validator error (`apps-use-case-postrequisites-*` waiting on `apps-rocket-postrequisites`) was rocket's undecided
`virtualizer` `enabled`: `resourceset-apps-rocket.yaml` renders its postrequisites only when it is on.

Still open: open questions 1 (datamarket component key, `governanceDatamarketAgent` vs `datamarketAgent`: Complete would add a duplicate block beside an existing
`governanceDatamarketAgent`), 3 (how the `POSTGRESQL` agents are meant to be declared in GitOps), 5 (storage `type` of rocket/virtualizer from the legacy env), the
env-based `enabled` evidence above, and a half-migrated cluster's `litellm` (its HelmRelease is `litellm`, its entry `genai-litellm`: the HelmRelease pass would add a second entry).
Not reviewed: the seeded `connectors` type has no `exclude` list and no `prepare` step (its notes say so); run the `add-catalog-component` Step 5 review before `apps migrate connectors`
(`apps diff connectors --view patch` shows the GitOps image as `busybox:1.31.1` against the live `connectors:2.2.0`, and an immutable-selector action).

*Original status: open, not started.* For a fresh session. Every fact below was verified on 2026-10-09 against the running eosdev cluster
(read-only), the `keos-use-cases` / `keos-apps` / `charts` checkouts named in `~/.fluxcd/flux-stratio/environment.yaml`, and this repo's code at
`feat/application-migration`. What is *not* verified is listed under "Open questions". Do not re-run the whole investigation: the commands to
regenerate the evidence are in the appendix; re-check only what a decision needs.

eosdev state when the evidence was captured: **system-services migrated to GitOps, every tenant app (`stratio` tenant) still the legacy
CCT deployment**. This is exactly the state `tenant import` is documented to run in ("scan a live, *not-yet-migrated* cluster",
`docs/migration-runbook.md` §3, `CONTEXT.md` workflow step 1).

Related: `docs/TASK-tenant-import-dependency-enabled.md` (its "Still open" paragraph is the `enabled` half of this task: it asked for the legacy
Deployment env and said it needed a real not-yet-migrated environment; eosdev is one), `docs/TASK-tenant-import-idempotent-merge.md` (Complete
mode: only additions, never overwrite; keep it), `docs/TASK-tenant-import-nested-config-scaffolding.md`, `docs/config-reference.md` (the
`catalog.yaml` `match` / `entry` / `chart.uses` fields this task builds on).

## The problem in one paragraph

`tenant import` finds the tenant's components with three passes (`internal/tenantimport/tenantimport.go` `Scan`): CRD instances, Deployments
whose **name equals a chart name**, and **HelmReleases**. Before the migration there are no app HelmReleases, and most legacy Deployment names are
not chart names, so the scan misses whole components, falls back to placeholders, and cannot decide any dependency `enabled` switch. The
resulting tenant file lacks components the ResourceSets need, and `tenant import`'s own validation then fails ("Flux would block it forever").
Meanwhile the plugin already has a classifier that recognises every legacy object from its CCT annotations (`internal/components`, driven by the
`catalog.yaml` that `config init` seeds). `tenant import` just doesn't use it.

## The symptom (user run on eosdev, 2026-10-09, completing the existing `stratio.yaml`)

```
✚ added components.eurekaAgent[eureka-agent]: config.dependencies.connectors.name: connectors   (x4: eurekaAgent, intelligence, rocket, virtualizer)
⚠️ components.eurekaAgent[eureka-agent]: dependency "dgAgent" could not be resolved; not added, fill it in by hand
⚠️ components.genai[genai]: dependency "litellm" could not be resolved; not added, fill it in by hand
⚠️ components.intelligence[intelligence]: dependency "litellm" could not be resolved; not added, fill it in by hand
⚠️ components.postgres[psql]: dependency "pgbackuprepository" could not be resolved; not added, fill it in by hand
⚠️ components.rocket[rocket]: dependency "dgAgent" could not be resolved; not added, fill it in by hand
⚠️ components.intelligence[intelligence]: cannot tell whether it uses dependency "genai" / "opensearch" / "virtualizer" (no live HelmRelease to read its configuration from)
⚠️ components.rocket[rocket]: cannot tell whether it uses dependency "genai" / "virtualizer" (no live HelmRelease to read its configuration from)
✗ apps-psql waits on stratio-datastores/apps-, which no ResourceSet renders from this file ...
✗ apps-eureka-agent waits on stratio-datastores/apps- ...      ✗ apps-rocket waits on stratio-datastores/apps- ...
✗ apps-genai waits on stratio-genai/apps-litellm ...            ✗ apps-use-case-postrequisites-authorize-customer waits on stratio-rocket/apps-rocket-postrequisites ...
Error: the tenant file has 5 problem(s)
```

Four of the `✗` are a missing component entry: `apps-psql` waits on the missing `pgbackuprepository`, `apps-eureka-agent` and `apps-rocket` on the missing
`dgAgent`, `apps-genai` on the missing `litellm`. The fifth (`apps-use-case-postrequisites-authorize-customer` waits on `apps-rocket-postrequisites`) is **not explained by
a missing entry I could identify**: it is probably downstream of rocket's unresolved `dgAgent` / unknown `enabled` inputs, but that was not verified. Each `⚠️ ... cannot tell` is
a dependency `enabled` the legacy environment can answer (section 4), and each `could not be resolved` is a component that exists on the cluster (section 2)
except `pgbackuprepository`, which has no legacy object (What to build 7).

## Root cause (verified)

### 1. What each scan pass needs

| Pass (`internal/tenantimport`) | Needs | Pre-migration |
|---|---|---|
| `scanCRDs` (`crdscan.go`) | the CRDs `internal/catalog` found (`pgclusters`, `pgbouncers`, `osclusters`, `osdashboardses`, `hdfsclusters`, `kafkaclusters`, `osbackups`, `pgbackuprepositories`) | works |
| `scanDeployments` (`deploymentscan.go`) | Deployment **name == a key of `cat.Charts`** (chart name), or, per namespace, the suffix after `<tenant>-` is a chart name | works only when the legacy name happens to equal the chart name |
| `scanHelmReleases` (`helmreleasescan.go`) | Flux `HelmRelease` objects | **no-op**: there are none |
| `scanUses` (`uses.go`) | the entry's live HelmRelease `spec.values` | **never decides**: every entry has `HelmReleaseSpec == nil`, so `DepUnknown` is set and `undecidedUses` warns |
| `expandMandatoryComponents` (`expand.go`) | Create mode only (`Run`) | adds placeholder entries named after the component key |
| `completeUndeclared` (`merge.go`) | Complete mode adds only what `Scan` found | cannot add what `Scan` missed |

### 2. Evidence: what the import finds vs. what the plugin's own classifier recognises

Same cluster, same moment (reproduce with the appendix). **A** is `tenantimport.Scan` (Complete mode, no skeleton expansion); **B** is
`components.Classify` with the config catalog (`~/.fluxcd/flux-stratio/catalog.yaml`, 19 seeded types).

**A, 13 entries:** `discovery`, `dlcEntity[dlc-entity]`, `eurekaAgent[eureka-agent]`, `genai`, `hdfs[hdfs1]`, `intelligence`, `kafka[kafka1]`,
`opendashboards[opensearch1-dashboards]`, `opensearch[opensearch1]`, `pgbouncer[pool-psql]`, `postgres[psql]`, `rocket`, `virtualizer`.

**B, 22 instances** (type → component key → entry → live objects). The ones missing from A are marked **✗**:

| Type | Component key | Entry | Live object(s) | In A |
|---|---|---|---|---|
| postgres | postgres | psql | PgCluster `psql` | yes |
| pgbouncer | pgbouncer | pool-psql | PgBouncer `pool-psql` | yes |
| postgres-gosec-agent | postgres (nested) | psql | Deployment `psql-agent` | n/a (not a component) |
| opensearch | opensearch | opensearch1 | OsCluster `opensearch1` | yes |
| opendashboards | opendashboards | opensearch1-dashboards | OsDashboards | yes |
| opensearch-gosec-agent | opensearch (nested) | opensearch1 | Deployment `opensearch1-agent` | n/a |
| hdfs / kafka | hdfs / kafka | hdfs1 / kafka1 | HDFSCluster / KafkaCluster | yes |
| **dg-agent** | **dgAgent** | **dg-hdfs-agent** | Deployment `dg-hdfs-agent` | **✗** |
| **dg-agent** | **dgAgent** | **dg-postgresql-internal-agent** | Deployment `dg-postgresql-internal-agent` | **✗** |
| **dg-agent** | **dgAgent** | **dg-postgresql-internal-agent-op** | Deployment `dg-postgresql-internal-agent-op` | **✗** |
| eureka-agent | eurekaAgent | eureka-agent | Deployment `eureka-agent` | yes |
| **bdl-datarest** | **bdlDatarest** | **dg-datarest-pgi** | Deployment `dg-datarest-pgi` | **✗** |
| virtualizer | virtualizer | virtualizer | Deployments `virtualizer`, `-monitor`, `-ui` | yes |
| discovery | discovery | discovery | Deployment `discovery` | yes |
| **datamarket-agent** | **datamarketAgent** | **governance-datamarket-agent** | Deployment `datamarket-agent` | **✗** |
| genai | genai | genai | Deployments `genai-api`, `genai-ui` | yes (via the namespace-suffix fallback) |
| **litellm** | **litellm** | **genai-litellm** | Deployment `genai-litellm` | **✗** |
| rocket / intelligence / dlc-entity | same | same | Deployments | yes |

Plus one legacy app **no catalog type recognises**: `stratio-apps/connectors-management` (image `stratio/connectors:2.2.0`,
`application_service=connectors-management`, `application_model=connectors-management-service-default`, tenant `stratio`). It is the
legacy counterpart of the GitOps `connectors` component (chart `connectors`, `keos-apps/components/connectors`, consumed by
`resourceset-apps-apps.yaml`, `-intelligence.yaml`, `-datastores.yaml`, `-rocket.yaml`).

Why A misses them, all verified: `cat.Charts` keys are `bdl-datarest`, `connectors`, `dg-agent`, `governance-datamarket-agent`, `eureka-agent`, ...
(no `litellm`, no `datamarket-agent`) while the live names are `dg-hdfs-agent`, `dg-postgresql-internal-agent(-op)`, `dg-datarest-pgi`,
`datamarket-agent`, `genai-litellm`, `connectors-management`. A name match cannot work for them.

### 3. Evidence: the CCT annotations carry everything needed, no Flux object does

Every legacy workload in `stratio-*` namespaces has the same nine annotations: `cct.stratio.com/application_{id,name,service,model,tenant,version,domain,created_by,generation}`.
Example (`genai-litellm`): `application_id=genai-litellm.stratio-genai`, `application_service=genai`, `application_model=genai-litellm`,
`application_tenant=stratio`, `application_version=15.1.0`. `internal/components` already turns them into instances (B above).

Flux objects in `stratio-*` namespaces on eosdev: **1 HelmRelease** (`stratio-use-case/talk-to-your-data-postrequisites-authorize-customer`) and
2 Kustomizations (`default-secrets-stratio`, `apps-use-case-postrequisites-authorize-customer`). **No app HelmRelease exists.**

### 4. Evidence: every `enabled` warning is answerable from the legacy Deployment env

The seeded `chart.uses` rules (`internal/config/seed.go` ~348, ~368) read HelmRelease values paths. Each path renders one env var in the chart
(`charts/rocket/config/rocket_common_env_vars.yaml`, `charts/intelligence/config/intelligence_env_vars.yaml`), and the legacy Deployment carries
that env var. Observed on eosdev (all non-secret):

| Entry | Dependency | `chart.uses` path (HelmRelease) | Env var on the legacy Deployment | Live value | Decision |
|---|---|---|---|---|---|
| rocket | genai | `rocketCommon.general.genAI.genaiEnabled` | `GENAI_ENABLED` | `true` | enabled |
| rocket | virtualizer | `rocketCommon.general.catalog.virtualizerHost` | `SPARTA_CONFIG_VIRTUALIZER_HOST` | `virtualizer.stratio-apps` | enabled |
| intelligence | genai | `configuration.general.genaiSettings.genaiEnabled` | `GENAI_ENABLED` | `true` | enabled |
| intelligence | virtualizer | `configuration.settings.AnalyticSettings.crossdataServer.CrossdataServerHost` | `CROSSDATA_SERVER_HOST` | `virtualizer.stratio-apps` | enabled |
| intelligence | opensearch | `...sqlChainConf.sqlChainOpensearchEnabled` | `GENAI_CHAIN_SQL_OPENSEARCH_ENABLED` | `true` | enabled |
| intelligence | litellm | `...litellmIntegration.litellmUrl` | `GENAI_LITELLM_URL` | `https://genai-litellm.stratio-genai:4040` | enabled |

That is the whole set of "cannot tell" warnings. The apps-diff machinery already compares chart-rendered env vars with the live workload's
resolved env (chart mode, `internal/appdiff`), so reading a legacy Deployment's env is existing, tested capability.

### 5. Other guesses the import makes today (evidence for the design, not separate bugs)

- **Storage `type`** of an entry is inferred from the mere existence of an `hdfs` component in the tenant (`enrichEntry`, `schemaHasStorageType`): rocket and
  virtualizer come out `type=hdfs`. The app's own env was never read. Counter-signal on eosdev: the legacy virtualizer has
  `SPARK_SECURITY_EXTERNALHDFS_ENABLE=false` while rocket has `EXTERNAL_HDFS_*`. Treat the inferred value as unverified.
- **Create mode placeholders** (`flux stratio tenant import` with no existing output, the path the runbook prescribes "first time only") are named after
  the component key and are wrong for a migration: `dgAgent[dg-agent]` (live: `dg-hdfs-agent`, two `dg-postgresql-internal-agent*`), `litellm[litellm]`
  (live and migrated name: **`genai-litellm`**, which `docs/config-reference.md` says must be kept "so the release keeps its identity and data"),
  `connectors[connectors]`. Output keys of Create mode on eosdev: `connectors, dgAgent, discovery, dlcEntity, eurekaAgent, genai, hdfs, intelligence,
  kafka, litellm, opendashboards, opensearch, pgbackuprepository, pgbouncer, postgres, rocket, virtualizer`; still missing `bdlDatarest`,
  `datamarketAgent` and the real names.
- **Component key drift:** the ResourceSets read `governanceDatamarketAgent` first and fall back to `datamarketAgent`
  (`keos-use-cases/apps/components/resourceset-apps-apps.yaml:114`, `-use-case.yaml:456`); the eosdev2 reference tenant uses `governanceDatamarketAgent`;
  the catalog derived from the use-cases checkout and the seeded config type say `datamarketAgent`. Hard-coding either is a bug waiting to happen.

## What to build

Principle: **discover the legacy cluster from what is on it (CCT annotations, CRs, env), through the catalog; use Flux objects only as an additional
source for apps that are already migrated.** No new hard-coded component names: every key and entry name comes from the catalog (`config.ComponentType.Component`,
`entry` template) or the use-cases schema.

1. **Replace the Deployment-name pass with the classifier.** Feed `tenantimport.Scan` from `components.Classify` (`internal/components/classify.go`) over the
   same live objects `internal/discovery` already scans, so each classified instance becomes an `Entry{key: type.Component, name: instance.Entry}`. Keep `scanCRDs`
   (it works pre-migration and carries the CR-derived dependency names). Keep `scanHelmReleases` as an additive pass so a half-migrated cluster still works, and
   de-duplicate an instance seen by two passes. Nested types (`postgres-gosec-agent`, `opensearch-gosec-agent`) are not components of their own; they must not become
   entries (they carry a nested `anchor`). Today's `scanDeployments` name/namespace-suffix heuristic can then be deleted.
2. **Entry naming from the catalog's `entry` template**, not from the live name: datamarket agent → `governance-datamarket-agent`, litellm → `genai-litellm`.
   Several instances of one type (three `dgAgent`) are three entries. Reconcile the datamarket component key with what the ResourceSets read first (see
   open question 1) and let `doctor` keep checking component keys against the templates.
3. **Add the missing catalog type for `connectors-management`** (`kinds: [apps/v1/Deployment]`, `application_service: connectors-management`,
   `application_model: connectors-management-service-default`, component `connectors`, chart path `connectors`, rset `resourceset-apps-apps.yaml`
   or wherever `connectors` is declared, entry template `connectors`) in `internal/config/seed.go`, `docs/config-reference.md` seeded-types table, and the seed test.
   An older `catalog.yaml` on a workstation will not have it: follow the existing "inherit the seed's value for the same type id" pattern used by `chart.uses`/`enabledBy`
   where a *field* is missing, and make `doctor` say so when a *type* is missing from the user's catalog (no silent absence).
4. **Dependency `enabled` from the legacy env.** Extend `chart.uses` with the env var(s) that carry the same evidence (`{dependency, values: [...HelmRelease paths...],
   env: [GENAI_ENABLED]}`), evaluated by `useEvidence`/`truthy` (`uses.go`) over the classified instance's primary Deployment container env
   when the entry has no HelmRelease; HelmRelease values keep priority when present (already migrated). Seed the six rules in section 4 and keep inheriting for older catalogs.
   Positive evidence only; no evidence → warn as today. Derive the env var for a values path from the chart's `config/*_env_vars.yaml` if you want to avoid
   maintaining two lists, but a seeded `env:` list is acceptable.
5. **Resolve the cross-component dependencies that were `could not be resolved`** once the entries exist (they resolve by key through `enrichEntry` as for any component):
   `genai`/`intelligence` → `litellm=genai-litellm`; `eurekaAgent`/`rocket` → `dgAgent` (see open question 2 for which agent); `dgAgent` → `connectors` always, `hdfs` when
   its type is `hdfs`, and `governancePgbouncer` / `governancePostgres` (platform Postgres in `keos-core`: `pgbouncer-postgreskeos-governance` / `postgreskeos`; the eosdev2
   reference file and the legacy agents' `DATASTORE_HOST=pgbouncer-postgreskeos-governance.keos-core` agree).
6. **`dgAgent` `config.type`** from the legacy agent (`CUSTOM_SERVICE_EXTRACTOR_TYPE=HDFS` → `hdfs`; `COMM_SERVICE_TYPE` the same on the live Deployments), mapped to the
   overlays `keos-apps/components/dg-agent/app/overlays/{hdfs,s3}`. An agent type with no overlay (the two `POSTGRESQL` agents, see open question 3) must be reported, not silently
   typed `s3` (the ResourceSet default, `resourceset-apps-datastores.yaml:885`).
7. **`pgbackuprepository`** has no legacy object (no `PgBackupRepository` instance exists on eosdev) but `postgres` depends on `apps-<its name>` unconditionally
   (`resourceset-apps-datastores.yaml:496`). Complete mode must not leave that silent: add the entry (name from the schema) with a note saying it is GitOps-only, or fail
   loudly. Decide with the user (open question 4); either way the validator must pass.
8. Keep everything the earlier tasks built: Complete adds only, never overwrites a value; `--dry-run` shows the exact additions; second run adds nothing; scaffolding of
   `llmModels` / litellm `models` warnings stays.

## Open questions (decide with evidence before coding; none is verified)

1. Datamarket component key: `governanceDatamarketAgent` (read first by the ResourceSets, used by eosdev2) or `datamarketAgent` (seed, catalog)? Check `doctor`'s key check and what
   `internal/catalog` extracts when a template reads two keys.
2. Which `dgAgent` do `eurekaAgent` and `rocket` depend on when several exist? eosdev2 uses a single `dg-s3-agent`; eosdev has `dg-hdfs-agent` plus two postgres agents. The rocket ResourceSet
   comment says the dependency is the "dg-s3-agent registration with governance". Probably "the agent whose type matches the tenant's storage", warning when ambiguous.
3. How are the legacy `dg-postgresql-internal-agent` / `-op` meant to be declared in GitOps? `keos-apps` `dg-agent` has only `hdfs` and `s3` overlays, while
   `docs/TASK-dg-hdfs-agent-custom-stratio-credentials.md` mentions `agent-rdbms-postgresql-internal` models in the chart. Read the `charts` dg-agent models and the governance
   charts before deciding whether they are `dgAgent` entries, part of the governance release, or out of scope.
4. `pgbackuprepository` in Complete mode: add with a note, or only warn? (section "What to build" 7.)
5. Should the storage `type` inference of rocket/virtualizer move to the legacy env too? (`EXTERNAL_HDFS_*`, `SPARK_SECURITY_EXTERNALHDFS_ENABLE`.) Out of scope unless it changes the output on eosdev.

## Verification

- Unit tests in `internal/tenantimport` against a fixture with **no HelmRelease CRD installed** (the pre-migration state). `internal/components/testdata/live.yaml` already holds captured
  eosdev objects (the legacy Deployments with their CCT annotations); extend it with `datamarket-agent`, `dg-datarest-pgi`, `genai-litellm`, the `dg-*` agents and `connectors-management` if absent, and
  assert the Create and Complete outputs contain every key and entry of table B (and `connectors`), with the catalog-derived names.
- `uses` tests: a legacy Deployment env with `GENAI_ENABLED=true` writes `dependencies.genai.enabled: true`; `false`/absent writes nothing and still warns; a HelmRelease value wins over env when both exist.
- On eosdev, against a **scratch copy** of `keos-fleet/clusters/eosdev/tenants/config/stratio.yaml` (never the real file; it is the user's test bed): `tenant import --dry-run` proposes the missing
  `dgAgent`, `bdlDatarest`, `datamarketAgent`, `litellm[genai-litellm]`, `connectors` entries and the six `enabled: true`; the post-import validation
  (`tenantcheck`) reports none of the five errors above; a second run adds nothing.
- Regression: the same run on a cluster whose apps are already migrated (HelmReleases present) gives the same keys as before this change.

## Constraints

- Never commit or push `keos-fleet`; do not edit its `stratio.yaml` beyond what the user asks. Do not change the ResourceSets in `keos-use-cases` or the charts.
- Code beats docs: `docs/config-reference.md` and `CONTEXT.md` describe `tenant import` as HelmRelease-assisted; update them with the change.
- Respect the catalog-driven design (`internal/catalog` from the use-cases templates, `internal/config` from `catalog.yaml`); no component names in Go code outside the seed.

## Appendix: regenerate the evidence

```shell
# Legacy workloads and their CCT annotations in the tenant's namespaces
for k in deploy sts ds; do kubectl get $k -A -o json | jq -r --arg k $k '.items[] | select(.metadata.namespace|test("^stratio")) |
  [$k, .metadata.namespace, .metadata.name, (.metadata.ownerReferences[0].kind // "-"),
   (.metadata.annotations["cct.stratio.com/application_service"] // "-"), (.metadata.annotations["cct.stratio.com/application_model"] // "-")] | @tsv'; done
# No app HelmReleases yet
kubectl get helmreleases.helm.toolkit.fluxcd.io -A --no-headers | awk '$1 ~ /^stratio/'
# Legacy env that decides `enabled` (names only; never print secrets)
kubectl -n stratio-rocket get deploy rocket -o json | jq -r '.spec.template.spec.containers[0].env[] | select(.name|test("^(GENAI_ENABLED|SPARTA_CONFIG_VIRTUALIZER_HOST)$")) | "\(.name)=\(.value)"'
kubectl -n stratio-intelligence get deploy intelligence -o json | jq -r '.spec.template.spec.containers[0].env[] | select(.name|test("^(GENAI_ENABLED|CROSSDATA_SERVER_HOST|GENAI_CHAIN_SQL_OPENSEARCH_ENABLED|GENAI_LITELLM_URL)$")) | "\(.name)=\(.value)"'
```

The A-versus-B comparison (what the import finds vs. what the classifier recognises) was a throwaway test in `internal/cli`, deleted afterwards. Recreate it with:

```go
// internal/cli/zz_evidence_test.go (do not commit)
func TestZZEvidence(t *testing.T) {
	ctx := context.Background()
	env, _ := loadEnvironment()
	cat, _ := catalog.Load(env.Repo(config.RepoUseCases))
	cfg, _ := config.Load("")
	c, _ := kubeclient.New(kubeconfigArgs)
	// A: what tenant import discovers today
	comps, _, _ := tenantimport.Scan(ctx, tenantimport.Options{TenantName: env.Tenant, Catalog: cat, Config: cfg, Client: c}, false)
	for key, entries := range comps { for _, e := range entries { fmt.Println("A", key, e.Name, e.Deps) } }
	// B: what the config catalog recognises from the CCT annotations
	idx, _ := discovery.Scan(ctx, c, log.New(os.Stderr, false), cfg.Kinds()...)
	insts, _ := components.Classify(cfg, idx.Objects(), env.Tenant)
	for _, i := range insts { fmt.Println("B", i.Type.Type, i.Type.Component, i.Entry, i.Primary().GetName()) }
}
// run: GOWORK=off go test ./internal/cli -run TestZZEvidence -v -count=1
```
