---
name: add-catalog-component
description: Add support for a new legacy Stratio component to flux-stratio's component catalog (catalog.yaml, seeded by `flux stratio config init`), so `apps diff/backup/migrate` can classify and migrate it. Use when `flux stratio apps diff <name>` fails with "none is selected by any catalog type" for a not-yet-migrated (CCT-deployed) component, or when asked to support a component the catalog doesn't cover yet.
---

# Add a component type to the flux-stratio catalog

This skill turns a live legacy (CCT-deployed) component that no catalog type selects into a
**validated, tested, approved** catalog type. After that, it upstreams the type into the seed that
`flux stratio config init` writes.

It generalizes the procedure first run for `opendashboards` (see [Worked example](#worked-example-opendashboards-on-eosdev)).
The core of it is one rule: **every dimension the catalog format supports gets an explicit,
evidenced answer.** A type is never written from defaults alone.

## Ground rules

- **Cluster access is read-only**: `kubectl get`, `flux stratio apps diff`, `apps migrate --dry-run`
  and `doctor`. Never apply, delete, patch, suspend or scale anything.
- **Never write the tenant file yourself, and never commit or push GitOps repos.** The actual
  `apps migrate` (and the push that follows) is the operator's call, after they approve the diff.
- **The catalog is static.** No instance or environment values in a type: no fixed object names, no
  namespaces, no cluster or tenant names, no absolute paths. Those are derived at run time (see
  [Schema](#catalogyaml-schema--enforced)).
- **If the format can't express something, stop.** Don't work around it with instance-specific
  values; extend the plugin instead (see [Format gaps](#format-gaps--extend-the-plugin-dont-work-around)).

## Step 0 — Locate everything (never assume paths)

Don't assume `/stratio/...`, `~/.fluxcd/...` or any other absolute path. Work out every location
the same way the plugin does, and refer to it by placeholder from then on.

| Placeholder | How to resolve it (first match wins) |
|---|---|
| **environment file** | `--env-config` → `$FLUX_STRATIO_ENV` → `~/.fluxcd/flux-stratio/environment.yaml` → `./flux-stratio-env.yaml` |
| `<cluster>`, `<tenant>` | That file's `cluster` and `tenant`. Any `--cluster/--tenant` the operator passes win |
| `<keos-apps>`, `<keos-use-cases>`, `<keos-fleet>`, `<charts>` | Each repository's checkout: that file's `repos.<name>` (or a `--repo <name>=<path>` the operator passes), else `<base>/<name>` — `base` from that file or `--base` |
| **catalog file** `<catalog>` | `--config` → `$FLUX_STRATIO_CONFIG` → `~/.fluxcd/flux-stratio/catalog.yaml` → `./flux-stratio.yaml` |
| **backups** `<backups>` | `--dir`, else the `backups/` directory next to `<catalog>` |
| **tenant file** `<tenantFile>` | `<keos-fleet>/clusters/<cluster>/tenants/config/<tenant>.yaml` |
| **flux-stratio checkout** `<repo>` | `git rev-parse --show-toplevel` from where this skill runs |

Confirm them with `flux stratio doctor`: the `environment`, `repo layout`, `chart paths` and
`tenant file` checks must pass, and doctor prints the paths it resolved.

## Step 1 — Reproduce

```shell
flux stratio apps diff <name>
```
You'll get `found <Kind> <ns>/<name>, ... live, but none is selected by any catalog type`. That
error lists every live object with that name, which is your first evidence for Step 2. Any other
error (e.g. "commented out in the tenant file") isn't a catalog gap; fix that instead.

If the error says the object "is rendered by HelmRelease …, already migrated", the component was
migrated before a catalog type existed for it (or a type's `chart.path` doesn't name the chart the
HelmRelease deploys). The CCT annotations the selector needs are gone from the live object: Helm
rewrote its metadata when the HelmRelease adopted it. Take the selector evidence from the
pre-migration backup (`<backups>/<name>/*/deployment.yaml` or `cr.yaml`) or from sibling objects
CCT still manages, and make sure `chart.path`'s base name equals the HelmRelease's
`spec.chart.spec.chart`, since that's what resolves the migrated workload by name
(`ManagedMatches`). Also check the workload's `keos.stratio.com/tenant` label is the run's tenant.

## Step 2 — Gather evidence for every dimension (read-only)

Answer each row and write down the answer, even when it's "none / default". Commands use the
placeholders from Step 0.

| Dimension | Question | Evidence | Drives |
|---|---|---|---|
| **Selector / kind** | Which live object is the component's top-level (anchor) object, and what identifies its *type* across environments? | Its `apiVersion/kind` and CCT annotations (below). Compare it with other instances (other namespaces/tenants) and with same-named objects of other kinds, e.g. a `PgDatabase` often shares an app's name | `match.kinds`, `match.annotations` / `match.labels` |
| **Ownership / grouping** | Is it owned by something? Does the component deploy several anchor-like objects? | `ownerReferences`, and which objects share `application_service` / `application_model` | Owned objects are skipped automatically, so select the owner. For several workloads, pin the anchor by model, or group them with an `entry` template |
| **Tenant scope** | Does another tenant (e.g. the platform's `keos`) run a copy? | `cct.stratio.com/application_tenant` on each candidate | Handled automatically; mention it in the type's comment |
| **Coordinates** | Which component key, rset file and Kustomization name? Is the patch anchor the default, nested (`config.agent`-style), or not patchable (`patches: []`)? | The template block (below) | `component`, `rset`, `kustomization`, `anchor` |
| **Renames** | Does the GitOps entry/object name differ from the live name? | The template's `name:` and `postBuild.substitute` (e.g. `*_NAME: << get $component "name" >>`, `-gosec-agent` suffixes) vs. the live name. Also `flux stratio tenant import` output | `entry`, `object` templates |
| **Namespaces** | Does the render's `targetNamespace` differ from the live namespace? | The template's `targetNamespace` vs. the live object's namespace | Nothing to set: the resolver falls back to the live namespace. Record the check |
| **Mode** | Is the rendered object a CR (manifest mode) or a HelmRelease (chart mode)? Which values root? | `<keos-apps>/components/<dir>/app/base/*.yaml`. For chart mode, the chart under `<charts>` | `chart.path`, `chart.valuesRoot` |
| **Dependencies** | Which spec fields reference another component the template substitutes? | The template's `$dependencies` and `postBuild.substitute`, then the matching fields in the diff | Candidates for `exclude` |
| **Exclusions** | Which differing fields must GitOps own (identity, vault, governance, cluster references, maybe images and URLs)? | The first `--view patch` diff, field by field (Step 5) | `exclude` |
| **Data identity** | Does the chart derive the identity its data is bound to (cert CN = DB user, Vault paths, gosec user) from the release name? Then renaming loses the data | The chart's `{{ .Release.Name }}.{{ .Release.Namespace }}` uses (secretsBundle, gosecUser/Policy, SERVICE_NAME) vs. the live identity | Keep the legacy name as the entry (default templates). The GitOps name must be configurable (keos-apps `${<NAME>:=<default>}`). **Never point the release at the legacy Vault keys**: a chart SecretsBundle owns `userland/passwords/<release>.<namespace>/` and deletes every key there it doesn't declare. Data encrypted with legacy secrets must be cleaned and re-created before cutover |
| **Preconditions** | Would cutover collide with anything: an unowned legacy Ingress, an immutable selector, a legacy HelmRelease to suspend, a data rewrite? | Live Ingress, Service and HelmRelease objects for the component and who owns them; immutable fields | `prepare` (an existing step, or a new one in `<repo>/internal/prepare`), `notes` |
| **Flux state / backup** | Is the live object already Flux-managed? Does a backup exist? | The `kustomize.toolkit.fluxcd.io/name` label, and `<backups>/<id>/` | Whether Step 4 diffs live or `--baseline latest` |

Useful read-only commands:

```shell
# Anchor object: kind, CCT identity, tenant, owners, Flux state
kubectl get <kind> -A -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,SVC:.metadata.annotations.cct\.stratio\.com/application_service,MODEL:.metadata.annotations.cct\.stratio\.com/application_model,TENANT:.metadata.annotations.cct\.stratio\.com/application_tenant,OWNER:.metadata.ownerReferences[0].kind,FLUX:.metadata.labels.kustomize\.toolkit\.fluxcd\.io/name'

# Everything else that shares the CCT service (siblings, other flavors)
kubectl get deploy,sts,<kind> -A -o custom-columns='KIND:.kind,NS:.metadata.namespace,NAME:.metadata.name,SVC:.metadata.annotations.cct\.stratio\.com/application_service,MODEL:.metadata.annotations.cct\.stratio\.com/application_model,OWNER:.metadata.ownerReferences[0].kind' | grep -i <service>

# Which template block renders it, and how (list every component key, then find yours)
grep -noE 'range \$component := \$[A-Za-z0-9]+' <keos-use-cases>/apps/components/resourceset-apps-*.yaml
grep -nF 'range $component := $<componentKey>' <keos-use-cases>/apps/components/resourceset-apps-*.yaml
#   → inside the block: name: apps-..., targetNamespace, path: components/<dir>/app/overlays/..., patches:, postBuild.substitute

# Mode: CR or HelmRelease?
grep -n '^kind:' <keos-apps>/components/<dir>/app/base/*.yaml

# Tenant entry declared (not commented out)?  Collisions?  Backup?
grep -n '<componentKey>:' -A6 <tenantFile>
kubectl get ingress,svc,helmrelease -n <ns> -o custom-columns='KIND:.kind,NAME:.metadata.name,OWNER:.metadata.ownerReferences[0].kind' | grep <name>
ls <backups>/<name>/
```

## Step 3 — Write the type

Derive each field from its dimension's evidence, and **omit every field that equals its default**.

### catalog.yaml schema — enforced

This mirrors the loader in `<repo>/internal/config/catalog.go`. `flux stratio doctor` rejects
anything that doesn't conform.

- **Top level:** `types:` only. No `apiVersion`/`kind` header, no environment keys (those belong in
  the environment file). Unknown keys at any level are rejected.
- **Field order** in each entry: `type`, `name`, `component`, `rset`, then (only if needed)
  `entry`, `object`, `kustomization`, `anchor`, `chart`, then `match`, `prepare`, `exclude`,
  `notes`.
- **Placement:** put the new type next to related types, e.g. `opendashboards` right after
  `opensearch`.

| Field | Required | Rules |
|---|---|---|
| `type` | yes | unique id; kebab-case; what `--as <type>/<entry>` takes |
| `name` | yes | human-readable label |
| `component` | yes | the tenant file's `components.<key>`, exactly as the template's `range $component := $<key>`. doctor checks it against `<keos-use-cases>` |
| `rset` | yes | path relative to `<keos-use-cases>`, e.g. `apps/components/resourceset-apps-datastores.yaml`. **Use the file whose block you read.** Keys like `postgres` appear in several rset files |
| `entry` | no | template, default `{{ .Live.Name }}`: the tenant entry the live object migrates into |
| `object` | no | template, default `{{ .Entry }}`: the rendered HelmRelease/CR name |
| `kustomization` | no | template, default `apps-{{ .Object }}` |
| `anchor` | no | dotted path (e.g. `config.agent`). Almost never needed, since it's derived from the templates, and a wrong value fails loudly |
| `chart.path` | chart mode | relative to the charts repository root `<charts>`, e.g. `litellm` (never `charts/litellm`); enables chart/env-var comparison |
| `chart.valuesRoot` | no | chart mode only: pins the `.Values` root when a chart has several flavors |
| `match.kinds` | yes | list of `group/version/Kind` (`version/Kind` for the core group). The version isn't compared at match time |
| `match.annotations` / `match.labels` | at least one | `matchLabels: {k: v}` and/or `matchExpressions: [{key, operator, values}]`. Operators: `In` or `NotIn` (with values), `Exists` or `DoesNotExist` (without) |
| `prepare` | no | must name an existing step in `<repo>/internal/prepare` |
| `exclude` | no | dot-paths rooted at the patch document: `spec.…` for manifest mode, `spec.values.<root>.…` for chart mode |
| `notes` | no | free text shown to the operator |

**Templates** are Go `text/template` strings with this data and these functions:
- **Data:** `.Live.Name`, `.Live.Namespace`, `.Live.Label "<k>"`, `.Live.Annotation "<k>"`, `.Entry`
  (in `object`/`kustomization`), `.Object` (in `kustomization`), `.Tenant`.
- **Functions:** `trimSuffix`, `trimPrefix`, `replace`, `lower`, pipeline-style, e.g.
  `{{ .Live.Name | trimSuffix "-agent" }}`.

**Selectors:**
- Prefer the CCT annotations `cct.stratio.com/application_service` (plus `application_model` when a
  service has several flavors or sibling apps).
- Always pin `match.kinds`.
- Never select on names.

Copy-paste block:

```yaml
  - type: <kebab-id>
    name: <Human name>
    component: <componentKey>
    rset: apps/components/resourceset-apps-<file>.yaml
    # entry/object/kustomization/anchor: only if they differ from the defaults
    # chart:
    #   path: <chart>
    #   valuesRoot: <root>
    match:
      kinds: [<group>/<version>/<Kind>]
      annotations:
        matchLabels:
          cct.stratio.com/application_service: <service>
          # cct.stratio.com/application_model: <model>
    # prepare: <step>
    # exclude:
    #   - spec.<path>
    # notes: <why any of the above>
```

Invalid block, corrected:

```yaml
# ✗ carries instance values and a name selector; missing kinds
  - type: dashboards
    name: Dashboards
    component: opendashboards
    rset: apps/components/resourceset-apps-datastores.yaml
    object: opensearch1-dashboards        # instance name — breaks every other environment
    match:
      labels:
        matchLabels:
          app: opensearch1-dashboards     # name-based, not type-based

# ✓ static and type-based: defaults cover the names
  - type: opendashboards
    name: Opensearch dashboards
    component: opendashboards
    rset: apps/components/resourceset-apps-datastores.yaml
    match:
      kinds: [opensearch.stratio.com/v1/OsDashboards]
      annotations:
        matchLabels:
          cct.stratio.com/application_service: Dashboards-Opensearch
```

### Seeded types as worked patterns

| Variant | Seed type to copy from |
|---|---|
| Manifest mode, all defaults | `opendashboards`, `postgres`, `kafka` |
| Rename + nested anchor + chart | `postgres-gosec-agent` (`entry` trims `-agent`/`-gosec-agent`, `object: '{{ .Entry }}-gosec-agent'`) |
| Several workloads, pinned anchor | `genai` (`application_model: genai-api`, `entry` trims `-api`) |
| Live name ≠ tenant entry + prepare step | `datamarket-agent` (`entry: 'governance-{{ .Live.Name }}'`, `prepare-datamarket-agent`) |
| Chart flavor | `bdl-datarest` (`application_model: datarest-pginternal` ↔ `valuesRoot: datarestPgInternal`) |
| One type, several flavors | `dg-agent` (`In` over `connectors-dfs`, `connectors-rdbms`) |
| Model excludes siblings | `virtualizer`, `rocket` (`application_model: default`) |

## Step 4 — Validate, then test against the live cluster

1. Add the block to `<catalog>`. When testing a scratch copy instead (`flux stratio config init --dir
   <scratch> ...` plus the new block, then `--config <scratch>/catalog.yaml`), pass `--dir <backups>`
   to anything that uses `--baseline latest`. Otherwise `latest` looks for backups next to the
   scratch catalog.
2. **Validation gate:** `flux stratio doctor` must pass both of these before any diff:
   - `catalog`: schema, kinds, operators and templates are valid, and types are unique;
   - `catalog types`: the component key exists and the prepare step is known.
3. Check it's classified and rendered, and look at the patch:
   ```shell
   flux stratio apps diff <name>                                   # desired state vs. live cluster
   flux stratio apps diff <name> --view patch                      # the patch migrate would write
   flux stratio apps diff <name> --view patch --baseline latest    # vs. your last backup: should match live if not yet Flux-managed
   ```
   If the live object is already Flux-managed (you'll see a warning), live no longer holds the
   legacy values. Use `--baseline latest` for everything from here on.

## Step 5 — Review the patch with the operator (approval gate)

For **every** field in the patch, show:
- the rendered GitOps value (unified view, or the keos-apps base plus size overlay);
- the legacy value;
- a classification of the field.

| Classification | Default decision |
|---|---|
| Legacy sizing (instances, resources, storage) | keep (this is what the patch is for) |
| Identity, vault, governance, SSO, cluster references substituted from dependencies | propose `exclude` |
| Versions (`image`) and URLs (`exposition.host`) | **ask**: it's a policy choice (both were kept for `opendashboards`) |

Re-run Step 4 after each `exclude` change. Stop only when the operator explicitly approves the
patch. Then show `flux stratio apps migrate <name> --dry-run` (it writes nothing) as the final
preview.

## Step 6 — Migrating (operator's call)

- **Back up before the component is pushed to the tenant file:** `flux stratio apps backup <name>`.
- **Migrate before Flux reconciles it unpatched:** `flux stratio apps migrate <name>`, then commit and
  push the tenant file.
- **If Flux already reconciled it:** `flux stratio apps migrate <name> --baseline latest`.

## Step 7 — Upstream the type into the seed

Everything here is in `<repo>`:

1. `internal/config/seed.go`: add the type (a new `kind…` const if needed) next to its relatives, with
   a comment recording every non-obvious decision: excludes kept or dropped, why no prepare step, and
   tenant duplicates.
2. `internal/components/testdata/live.yaml`: add the live anchor object's **metadata only**
   (name, namespace, `cct.*` labels, the CCT annotations above, and `ownerReferences` with a
   placeholder uid). No spec, no secrets. Add negative cases too: same-named objects of other kinds,
   and owned siblings.
3. Tests:
   - `internal/components/classify_test.go`: the expected instance list;
   - `internal/config/seed_test.go`: the type count;
   - `internal/components/resolve_test.go`: the instance counts.
4. Docs:
   - `docs/config-reference.md`: the "Seeded types" table and count;
   - README and CONTEXT.md: the type count;
   - CHANGELOG: one line with the decisions.
5. Verify:
   ```shell
   make fmt-check vet lint test build
   FLUX_STRATIO_KEOS_USE_CASES=<keos-use-cases> GOWORK=off go test ./internal/components/ -run RealTemplates -count=1
   ```
   The integration test checks, against the real templates, that the component key exists and that
   the rendered Kustomization resolves to the expected anchor.

## Format gaps — extend the plugin, don't work around

Stop and propose a plugin change (code, tests and docs, with the operator's go-ahead) when:
- a rename can't be expressed as an `entry`/`object` template over the live object;
- the GitOps namespace can't be reached by the live-namespace fallback;
- cutover needs a precondition no existing `prepare` step covers (add one in `internal/prepare`);
- the anchor object can't be told apart by kind + labels/annotations (e.g. no CCT annotations);
- the patch shape isn't one `internal/catalog` recognizes.

## Worked example: opendashboards on eosdev

*Example values from eosdev: `<tenant>` = `stratio`.*

| Dimension | Answer (evidence) |
|---|---|
| Selector / kind | `OsDashboards` `opensearch.stratio.com/v1`, `application_service: Dashboards-Opensearch`, model `default` |
| Ownership / grouping | The CR is unowned; its Deployment, Service and Ingress are owned by it and skipped |
| Tenant scope | `application_tenant: stratio`, no other copy |
| Coordinates | `$opendashboards` block in `resourceset-apps-datastores.yaml`; `name: apps-<< name >>`; `patches: << get $component "patches" >>` → default anchor |
| Renames | none (`OS_DASHBOARDS_NAME` = entry = live name) |
| Namespaces | `targetNamespace: <tenant>-datastores` = live `stratio-datastores` |
| Mode | manifest (`keos-apps/components/opendashboards/app/base/osdashboards.yaml` is the CR) |
| Dependencies | `spec.opensearch.{name,namespace}` ← `OS_CLUSTER_*`; identical in the diff, so no exclude needed |
| Exclusions | none. The patch is `instances` and `resources` (legacy sizing), plus `image` and `exposition.host`, both kept by operator decision |
| Preconditions | none: the legacy Ingress/Service are CR-owned and Flux adopts the CR in place |
| Flux state / backup | not Flux-managed; backup `<backups>/opensearch1-dashboards/…/cr.yaml`; live and baseline patches identical |

Resulting seed type:

```yaml
  - type: opendashboards
    name: Opensearch dashboards
    component: opendashboards
    rset: apps/components/resourceset-apps-datastores.yaml
    match:
      kinds: [opensearch.stratio.com/v1/OsDashboards]
      annotations:
        matchLabels:
          cct.stratio.com/application_service: Dashboards-Opensearch
```
