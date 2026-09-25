# Config file reference

flux-stratio's config file is the single source of truth for which applications can be migrated,
where their ResourceSet template and Kustomization live, and any per-app quirks the generic
pipeline needs to know about. See [the README](../README.md#which-config-am-i-using) for how the
file itself is located.

It's plain YAML; unknown top-level or per-app keys are rejected at load time (a typo fails loudly
instead of being silently ignored).

`flux stratio config init --base <path> --cluster <name> --tenant <name>` writes a starting file
seeded with the known 16-application Stratio catalog (see [`internal/config/seed.go`](../internal/config/seed.go))
instead of an empty one — review and prune it for your environment rather than authoring the whole
`apps:` list by hand. It refuses to overwrite an existing file without `--force`. Pass `--charts
<path>` too if the chart-mode apps' Helm chart sources aren't checked out as a sibling of `--base`
(see `chartsBase` below).

## Top level

```yaml
base: /path/to/gitops   # required: parent of keos-apps, keos-use-cases, keos-fleet, keos-system-services
cluster: eosdev          # required: the cluster name (used to locate the tenant file)
tenant: stratio           # required: the tenant name (used to locate the tenant file and to scope tenant import)
chartsBase: /path/to/charts  # optional: overrides base for chartPath resolution (see below)
apps: [ ... ]              # the app catalog, see below
```

`--base`, `--cluster` and `--tenant` on the command line override these for a single invocation.
`chartsBase` has no such per-invocation flag — it's set once, either by `config init --charts` or
by hand-editing the config file.

## `apps[]`

Each entry describes one application.

| Field | Required | Meaning |
|---|---|---|
| `id` | yes | Unique identifier, used on the command line (`apps diff <id>`, `apps migrate <id>`) and as its backup directory name |
| `name` | yes | Human-readable label shown in progress output |
| `rset` | yes | Path, relative to `keos-use-cases`, of the ResourceSet template declaring this app's Kustomization |
| `kustomization` | yes | The exact rendered Kustomization name to select |
| `object` | yes | The exact HelmRelease or custom resource name, inside that Kustomization, to diff against the live cluster |
| `chartPath` | no | Path, relative to `base` (or `chartsBase`, if set), to a Helm chart — set this to switch the app into **chart mode**: comparison happens via the chart's rendered env vars against the live workload's resolved environment, instead of a direct manifest/CR diff. Leave unset for a CRD/manifest-backed app |
| `valuesRoot` | no | Chart mode only. When a chart mixes more than one flavor's `.Values` root in the same directory tree (e.g. a chart with `pgmd5`/`pgtls`/`pginternal` variants sharing one `config/` directory), pins which root wins when a key is ambiguous |
| `renamed` | no | The live cluster object's name, when the GitOps redesign renamed it (the object was called something else before migration) |
| `previousNamespace` | no | Fallback namespace to look for the live object in, if it isn't found in the namespace the current convention implies |
| `anchor` | no | Overrides where, relative to this app's own component entry in the tenant file, its patches are read from — see [Anchor](#anchor) below. Almost never needed: the catalog derives this automatically for every case currently in use |
| `prepare` | no | Names a one-time precondition this app requires before migration — see [Prepare](#prepare) below |
| `exclude` | no | A list of dot-paths to drop from the computed diff/patch — see [Exclude](#exclude) below |
| `notes` | no | Free-text hint shown to the operator, e.g. alongside a prepare step that blocks migration |

`chartsBase` (top-level, optional) exists for the case where the Helm chart sources
`chartPath` points into aren't checked out as a sibling of `keos-apps`/`keos-use-cases`/
`keos-fleet`/`keos-system-services` under `base` — e.g. a separate `charts` repo checked out
somewhere else entirely. When set, every `chartPath` resolves relative to `chartsBase`
instead of `base`; leave it unset (the common case) and `chartPath` keeps resolving relative
to `base` as before.

### A manifest-mode example

```yaml
apps:
  - id: psql
    name: Postgres psql
    rset: apps/components/resourceset-apps-datastores.yaml
    kustomization: apps-psql
    object: psql
    exclude:
      - spec.bootstrap.pgBackup
```

### A chart-mode example, with a rename

```yaml
apps:
  - id: psql-gosec-agent
    name: Postgres gosec agent
    rset: apps/components/resourceset-apps-datastores.yaml
    kustomization: apps-psql-gosec-agent
    object: psql-gosec-agent
    chartPath: charts/gosec-agent
    renamed: psql-agent
    exclude:
      - spec.values.gosecAgent.environment.domainsConfig.mappingUrl
```

### An app gated on a prepare step

```yaml
apps:
  - id: datamarket-agent
    name: Datamarket Agent
    rset: apps/components/resourceset-apps-apps.yaml
    kustomization: apps-governance-datamarket-agent
    object: governance-datamarket-agent
    chartPath: charts/governance-datamarket-agent
    previousNamespace: stratio-datastores
    prepare: prepare-datamarket-agent
    notes: >-
      Suspends and scales down the legacy HelmRelease before cutover;
      apps migrate runs this automatically.
```

## Anchor

A Kustomization's `patches:` field normally comes from its own component entry in the tenant
file — `components.<key>[name=<object>].patches` — found by scanning every component key for one
named `object`, so the config never needs to say which key that is. `internal/catalog` derives this
default (and the one real exception currently in use — a postgres/opensearch gosec agent, whose
patches live one level deeper at `config.agent.patches` on its *parent* component's entry, since a
gosec agent has no top-level entry of its own) directly from the `keos-use-cases` templates, so
`anchor` is essentially never something you need to set by hand.

If it ever needs setting, it's a dotted field path relative to the owning component entry, e.g.:

```yaml
anchor: config.agent
```

flux-stratio validates a configured `anchor` against what the catalog independently derives from
the templates and fails loudly on a mismatch — better than writing a patch into the wrong place in
the tenant file.

## Prepare

Some applications need a one-time precondition satisfied before they can be safely migrated —
named here, run automatically as the first stage of `apps migrate` for that app (see the
[README](../README.md#commands)).

| Step | What it does | Automated? |
|---|---|---|
| `prepare-datamarket-agent` | Suspends the legacy `datamarket-agent` HelmRelease and scales its Deployment to 0, waiting for its pods to terminate | yes |
| `prepare-datarest` | Removes the legacy DataRest ingress that would collide with the GitOps-managed one | yes |
| `prepare-dlc` | Removes the legacy DLC ingress and deployment (the chart changed an immutable selector label, so Flux must recreate it) | yes |
| `prepare-genai` | A Postgres data rewrite (renaming a stored component reference) — no Kubernetes API can verify this happened, so it's never auto-detected. `apps migrate` prints the exact SQL and always asks its own separate confirmation before proceeding, never skipped by `--yes` | no |

Every automated step re-checks live cluster state each run rather than trusting a persisted
record — running `apps migrate` again against an app whose precondition already holds is a no-op
for that step, not a repeat of the mutation.

## Exclude

Dot-paths dropped from the computed diff and patch before it's written — fields the GitOps side is
authoritative for and must never be back-ported from the live cluster. In practice this is almost
always an identity, vault or governance-integration field: a value the platform assigns once at
provisioning time, not something that should ever flow from the legacy cluster into the new one.

Paths are rooted at the patch document itself, so a manifest-mode exclude starts with `spec.`:

```yaml
exclude:
  - spec.bootstrap.pgBackup
```

and a chart-mode exclude starts with `spec.values.<root>.`:

```yaml
exclude:
  - spec.values.datarestPgInternal.general.identity.approlename
```
