# Config file reference

flux-stratio reads two plain-YAML files, both written by `flux stratio config init` into
`~/.fluxcd/flux-stratio/` (or `--dir`). See [the README](../README.md#which-files-am-i-using) for
how each is located.

| File | Holds | Changes when |
|---|---|---|
| [`catalog.yaml`](#catalogyaml--the-component-catalog) | Every supported **component type**: its static GitOps coordinates, how to compare it, and the **selectors** that recognize its live legacy instances | a new kind of component is supported, or a selector/exclude needs tuning — never per environment |
| [`environment.yaml`](#environmentyaml) | Where the GitOps repositories are checked out, and which cluster/tenant to operate on | you switch workstation, cluster or tenant |

Neither file holds anything **instance-specific** — which live object is `psql`, whether the
legacy gosec agent is called `psql-agent` or `psql-gosec-agent`, which namespace CCT deployed it
to. Those facts differ per environment, so flux-stratio derives them on every run from the live
cluster and the tenant file (see [Resolution](#resolution-live-object--instance)), and asks you
only when it genuinely can't.

Unknown keys are rejected at load time in both files (a typo fails loudly instead of being
silently ignored). A pre-catalog `config.yaml` (the old single file with a top-level `apps:` list)
is recognized and rejected with a pointer to `config init --force`.

```sh
flux stratio config init --base /stratio/gitops --cluster eosdev --tenant stratio [--charts /stratio/charts]
```

`config init` refuses to overwrite either file without `--force`. `flux stratio doctor` checks both,
plus every type's `component` key and `prepare` step against the real templates.

## `environment.yaml`

```yaml
base: /stratio/gitops        # required: parent of keos-apps, keos-use-cases, keos-fleet, keos-system-services
chartsBase: /stratio/charts  # optional: where chart-mode types' chart.path resolves (defaults to base)
cluster: eosdev              # required: the cluster name (locates the tenant file)
tenant: stratio              # required: the tenant name (locates the tenant file, scopes tenant import)
```

`--base`, `--cluster` and `--tenant` override these for a single invocation; with all three given,
no environment file is needed at all.

## `catalog.yaml` — the component catalog

```yaml
types:
  - type: postgres-gosec-agent     # unique id; also what --as <type>/<entry> takes
    name: Postgres gosec agent     # human-readable label
    component: postgres            # the tenant file's components.<key> its instances live under
    rset: apps/components/resourceset-apps-datastores.yaml  # relative to keos-use-cases
    entry: '{{ .Live.Name | trimSuffix "-gosec-agent" | trimSuffix "-agent" }}'
    object: '{{ .Entry }}-gosec-agent'
    # kustomization: 'apps-{{ .Object }}'   (the default)
    chart:
      path: charts/gosec-agent     # set => chart mode; relative to chartsBase (or base)
    match:
      kinds: [apps/v1/Deployment]
      annotations:
        matchLabels:
          cct.stratio.com/application_service: pg-gosec-agent
    exclude:
      - spec.values.gosecAgent.environment.vault.vaultRole
```

| Field | Required | Meaning |
|---|---|---|
| `type` | yes | Unique identifier, shown in output and taken by `--as` |
| `name` | yes | Human-readable label shown in progress output |
| `component` | yes | The tenant file `components.<key>` this type's instances are declared under (e.g. `postgres`, `dgAgent`). `doctor` checks it against the `keos-use-cases` templates |
| `rset` | yes | Path, relative to `keos-use-cases`, of the ResourceSet template declaring this type's Kustomization |
| `match` | yes | Which live legacy objects are instances of this type — see [Match](#match) |
| `entry` | no | [Template](#name-templates) for the tenant-file entry name an instance maps to. Default `{{ .Live.Name }}` |
| `object` | no | Template for the HelmRelease/custom resource name, inside the rendered Kustomization, to diff against the live object. Default `{{ .Entry }}` |
| `kustomization` | no | Template for the rendered Kustomization name. Default `apps-{{ .Object }}` — the convention every `keos-use-cases` component follows |
| `chart.path` | no | Switches the type into **chart mode**: comparison happens via the chart's rendered env vars against the live workload's resolved environment, instead of a direct manifest/CR diff |
| `chart.valuesRoot` | no | Chart mode only. When a chart mixes more than one flavor's `.Values` root in one directory tree (e.g. `pgmd5`/`pgtls`/`pginternal`), pins which root wins when a key is ambiguous |
| `anchor` | no | Overrides where, relative to the instance's tenant entry, its patches are read from — see [Anchor](#anchor). Almost never needed |
| `prepare` | no | A one-time precondition `apps migrate` satisfies first — see [Prepare](#prepare) |
| `exclude` | no | Dot-paths dropped from the computed diff/patch — see [Exclude](#exclude) |
| `notes` | no | Free-text hint shown to the operator |

### Match

```yaml
match:
  kinds: [apps/v1/Deployment]           # required: group/version/Kind (or version/Kind for core)
  labels:       { matchLabels: {...}, matchExpressions: [...] }   # optional
  annotations:  { matchLabels: {...}, matchExpressions: [...] }   # optional
```

A live object is an instance of a type when its group and kind are in `kinds` (the version isn't
compared) and every selector that's set holds. Both selectors have Kubernetes label-selector
semantics — `matchLabels` pairs must all be equal, and each `matchExpressions` entry is `{key,
operator, values}` with operator `In`, `NotIn`, `Exists` or `DoesNotExist` — applied to the
object's labels or annotations respectively.

Legacy (CCT-deployed) Stratio objects record what they are in two annotations, which is what every
seeded type selects on:

| Annotation | Example values |
|---|---|
| `cct.stratio.com/application_service` | `Postgres`, `PgBouncer`, `pg-gosec-agent`, `os-gosec-agent`, `connectors-dfs`, `bdl`, `genai`, `rocket` |
| `cct.stratio.com/application_model` | `default`, `kraft`, `agent-bdl-default`, `genai-api`, `agent-default` |

`kinds` is what stops a same-named object of another kind from ever being taken for an app — a
real case: a `genai` or `rocket` `PgDatabase` lives right next to the chart app of the same name.

An object with `ownerReferences` is never an instance (the Deployment a `PgBouncer` CR owns, or
rocket's own sub-Deployments), so selectors only need to describe each component's top-level
object. Neither is an object CCT annotated (`cct.stratio.com/application_tenant`) as belonging to a
tenant other than the one this run operates on — eosdev, for instance, runs the platform's own
`opensearch1` (tenant `keos`, in `keos-core`) beside the `stratio` tenant's (`stratio-datastores`).

### Name templates

`entry`, `object` and `kustomization` are Go `text/template` strings. Available data:

| Name | Value |
|---|---|
| `.Live.Name`, `.Live.Namespace` | The matched live object's name/namespace |
| `.Live.Label "<key>"`, `.Live.Annotation "<key>"` | One of its labels/annotations (`""` when absent) |
| `.Entry` | The resolved entry name (in `object` and `kustomization`) |
| `.Object` | The resolved object name (in `kustomization`) |
| `.Tenant` | The tenant name |

Functions: `trimSuffix <suffix>`, `trimPrefix <prefix>`, `replace <old> <new>`, `lower` — all
pipeline-friendly: `{{ .Live.Name | trimSuffix "-api" }}`.

Several live objects of one type rendering the **same entry in the same namespace** become a single
instance (the first, by name, is its primary object).

## Resolution: live object → instance

Every `apps diff`/`backup`/`migrate` run scans the cluster (every kind any type's `match.kinds`
lists, plus Kustomizations, HelmReleases and workloads) and classifies each object against every
type. Then, for the instance you asked for (`apps diff <name>` accepts a live name, e.g.
`psql-agent`, or a derived object name, e.g. `psql-gosec-agent`) — or for all of them, with
`--all`/`--catalog`:

1. **Which type?** If `<name>` matches instances of more than one type, or the same entry is live in
   two namespaces of the same tenant, you're asked which one.
2. **Which tenant entry?** For `apps diff` and `apps migrate` (which render desired state from the
   tenant file), the derived entry must be declared under `components.<component>`. If it isn't —
   say the live `KafkaCluster` is `kafka1` but the tenant declares `kafka` — you're asked to pick
   one of the declared entries. With `--all`, a type whose `components.<component>` the tenant file
   doesn't declare at all is skipped with a warning.
3. The instance's `object` and `kustomization` are rendered from the chosen entry, and the live
   object is used as-is — its own name and namespace, even when the GitOps redesign renamed or
   moved it.

Answers are **never stored**. Answer interactively, or up front with
`--as <type>` / `--as <type>/<entry>`. With `apps migrate --yes`, a question that still needs an
answer isn't asked: with `--all`, every such instance is reported as a failed app (naming `--as`)
and — as with any failed app — nothing is migrated unless `--continue-on-error` is passed, in which
case the resolvable instances go ahead. `apps backup` and `apps diff --drift` read only
the live cluster, so step 2 never applies to them.

## Anchor

A Kustomization's `patches:` field normally comes from its own component entry in the tenant file —
`components.<key>[name=<entry>].patches`. `internal/catalog` derives this default (and the one
real exception currently in use — a postgres/opensearch gosec agent, whose patches live one level
deeper at `config.agent.patches` on its *parent* component's entry) directly from the
`keos-use-cases` templates, so `anchor` is essentially never something you need to set.

If it ever needs setting, it's a dotted field path relative to the owning component entry, e.g.
`anchor: config.agent`. flux-stratio validates it against what the catalog independently derives
from the templates and fails loudly on a mismatch rather than writing a patch into the wrong place.

## Prepare

Some components need a one-time precondition satisfied before they can be safely migrated — named
here, run automatically as the first stage of `apps migrate` for that instance.

| Step | What it does | Automated? |
|---|---|---|
| `prepare-datamarket-agent` | Suspends the legacy `datamarket-agent` HelmRelease and scales its Deployment to 0, waiting for its pods to terminate | yes |
| `prepare-datarest` | Removes the legacy DataRest ingress that would collide with the GitOps-managed one | yes |
| `prepare-dlc` | Removes the legacy DLC ingress and deployment (the chart changed an immutable selector label, so Flux must recreate it) | yes |
| `prepare-genai` | A Postgres data rewrite (renaming a stored component reference) — no Kubernetes API can verify this happened, so it's never auto-detected. `apps migrate` prints the exact SQL and always asks its own separate confirmation before proceeding, never skipped by `--yes` | no |

Every automated step re-checks live cluster state each run rather than trusting a persisted
record — running `apps migrate` again once a precondition holds is a no-op for that step.

## Exclude

Dot-paths dropped from the computed diff and patch before it's written — fields the GitOps side is
authoritative for and must never be back-ported from the live cluster. In practice this is almost
always an identity, vault or governance-integration field.

Paths are rooted at the patch document itself, so a manifest-mode exclude starts with `spec.`
(`spec.bootstrap.pgBackup`) and a chart-mode exclude with `spec.values.<root>.`
(`spec.values.datarestPgInternal.general.identity.approlename`).

## Seeded types

`config init` seeds 18 types, every selector taken from the CCT annotations on real legacy objects
— captured backups and the live eosdev cluster (see `internal/components/testdata/live.yaml`).

| Type | Component | Selects (kind, `application_service` / `application_model`) |
|---|---|---|
| `postgres` | `postgres` | PgCluster, `Postgres` |
| `pgbouncer` | `pgbouncer` | PgBouncer, `PgBouncer` |
| `postgres-gosec-agent` | `postgres` (nested anchor) | Deployment, `pg-gosec-agent` |
| `opensearch` | `opensearch` | OsCluster, `Opensearch` |
| `opendashboards` | `opendashboards` | OsDashboards, `Dashboards-Opensearch` |
| `opensearch-gosec-agent` | `opensearch` (nested anchor) | Deployment, `os-gosec-agent` |
| `hdfs` | `hdfs` | HDFSCluster, `HDFS` |
| `kafka` | `kafka` | KafkaCluster, `Kafka` |
| `dg-agent` | `dgAgent` | Deployment, `connectors-dfs` or `connectors-rdbms` |
| `eureka-agent` | `eurekaAgent` | Deployment, `bdl` / `agent-bdl-default` |
| `bdl-datarest` | `bdlDatarest` | Deployment, `bdl` / `datarest-pginternal` |
| `virtualizer` | `virtualizer` | Deployment, `virtualizer` / `default` |
| `discovery` | `discovery` | Deployment, `discovery` |
| `datamarket-agent` | `datamarketAgent` | Deployment, `data-marketplace` / `agent-default` |
| `genai` | `genai` | Deployment, `genai` / `genai-api` |
| `rocket` | `rocket` | Deployment, `rocket` / `default` |
| `intelligence` | `intelligence` | Deployment, `intelligence` |
| `dlc-entity` | `dlcEntity` | Deployment, `dlc-entity` |
