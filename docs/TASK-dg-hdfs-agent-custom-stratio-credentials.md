# Task: decide what to do about `CUSTOM_STRATIO_CREDENTIALS` on `dg-hdfs-agent`

**Status: open, needs a decision (and probably a chart or overlay change outside this repo).** For a
fresh session. The facts below were verified on 2026-10-02 against eosdev (read-only), the chart
checkout and the keos-apps checkout; what is *not* verified is listed explicitly under "Unknowns".
Do not re-run the whole investigation: re-check only what you need to answer the questions at the end.

## One-paragraph summary

Migrating the legacy (CCT-deployed) `dg-hdfs-agent` onto the `dg-agent` Helm chart changes one
environment variable of its container: live has `CUSTOM_STRATIO_CREDENTIALS` **set to nothing**, the
GitOps render sets it to `"hdfs-external-secret"`, a Secret that does not exist anywhere on the
cluster. `flux stratio apps diff|migrate dg-hdfs-agent` cannot carry the live blank through a
`.Values` patch (the chart's template turns a blank into `hdfs-secret`), so it warns and leaves the
render's value in place. Nobody has decided whether that is harmless. This task is to decide, and to
say where the fix belongs if one is needed.

## Where we come from

- `flux-stratio` migrates Stratio apps from CCT/Ansible deployments to Flux. Read `CONTEXT.md` first
  (§3 command reference, §6 `internal/appdiff`, `internal/diff`; §7 design decisions).
- A user rule governs this decision (auto-memory `migration-preserves-legacy`): **the migration patch
  exists to leave the legacy app as unchanged as possible after migration**, including values a GitOps
  overlay sets. When a legacy value can't reach the workload through a chart `.Values` path, look for
  a patch that does carry it (e.g. the overlay's env list) before reporting and dropping it. The only
  exceptions are deliberate catalog excludes (Vault role names; see `vault-role-naming`).
  Aligning the live product with GitOps conventions is a later, separate intervention.
- Since commit `064b588` the plugin refuses to write a patch value the chart wouldn't reproduce:
  it re-renders the patched chart and, if the variable still differs, takes the patch back out and
  reports it (`internal/diff/settle.go`, reason `UnmappedNotReproduced` =
  "patching its .Values path doesn't reproduce it", `internal/diff/chartdiff.go:41`; CHANGELOG ~line 81).
  Before that commit the same difference showed as an unsettled "found a difference" with a 132-line
  env diff that never converged. So this is not a regression: the same blank was always there, now it is
  named. That is how it surfaced in the parity check of `docs/TASK-legacy-parity-findings.md`
  (removed once done; its findings live in `CONTEXT.md` §9).

## What the plugin prints today

`flux stratio apps diff dg-hdfs-agent` (cluster `eosdev`, tenant `stratio`, exit 0, ends with
"nothing to migrate: the tenant file's existing patch already covers every difference"):

```
⚠️ 12 difference(s) need manual review — not written to the patch:
⚠️   dg-hdfs-agent/CUSTOM_STRATIO_CREDENTIALS: rendered "hdfs-external-secret", live "" — patching its .Values path doesn't reproduce it (agent.config.hdfs.customStratioCredentialsDefinition)
…eleven other manual-review lines (governance URLs `dg-` vs `governance-`, Vault role, COMM_DS_URL, …)
⚠️ 7 variable(s) in the live cluster aren't rendered by the chart: their values will be lost after migration:
…
```

The other eleven manual-review lines are separate, pre-existing, and out of scope here.

## The components involved, and the facts about each

### 1. The live legacy agent (eosdev, namespace `stratio-datastores`)

- Deployment `dg-hdfs-agent`, created by Stratio Command Center. CCT annotations:
  `application_model: agent-dfs-hdfs-internal`, `application_service: connectors-dfs`,
  `application_id: dg-hdfs-agent.stratio-datastores`, `application_version: 15.1.0`.
  "internal" means it talks to the in-cluster HDFS (`hdfs1`), with Kerberos
  (`CUSTOM_STRATIO_CONFIG_MAP=keos-kerberos-config`).
- Container `dg-hdfs-agent` has **no `envFrom`**; every variable is an inline `env` entry. The entry is
  `{"name": "CUSTOM_STRATIO_CREDENTIALS"}`: **no `value` key at all** (the plugin maps a valueless entry
  to `""`, `internal/envvars/envvars.go:137-147`).
- The pre-migration backup (`~/.fluxcd/flux-stratio/backups/dg-hdfs-agent/2026-10-01T11-01-51Z/`)
  has `CUSTOM_STRATIO_CREDENTIALS=` in `env-vars.deployment.dg-hdfs-agent.env`.
- There is **no Secret** whose name contains `hdfs-external`, `hdfs-secret` or `dg-hdfs` in **any**
  namespace (checked with `kubectl get secret -A`, names only, nothing was read), and no
  ExternalSecret for it in `stratio-datastores`.
- There is **no HelmRelease** for the agent on the cluster yet: the tenant file is migrated, the
  cluster has not reconciled it.

### 2. The `dg-agent` chart (`Stratio/charts`, checkout `/stratio/aws/flux/.worktrees/charts-plt-4838/dg-agent`, worktree `charts-plt-4838`)

- `config/connectors/dg-agent_hdfs_env_vars.yaml:9`:
  `CUSTOM_STRATIO_CREDENTIALS: {{ .Values.agent.config.hdfs.customStratioCredentialsDefinition | default "hdfs-secret" | quote }}`
- `values.yaml:636`: `agent.config.hdfs.customStratioCredentialsDefinition: "hdfs-external-secret"`
  (a non-empty chart default; `values.yaml:512`, the generic `agent.config.common` one, defaults to `""`).
- Consequence of `| default`: **no `.Values` value can render an empty string here.** `""`, `null` and
  absent all render `"hdfs-secret"`. That is exactly why the plugin can't patch the blank in.
- The variable reaches the container through the ConfigMap `<release>-hdfs-config` via the container's
  `envFrom` (`values.yaml:208-213`). The container's own `controllers.dg-agent.containers.agent.env`
  (list form: `SERVICE_ACCOUNT_NAME`, `HOST`; `values.yaml:197-207`) is the only inline env, and a
  Kubernetes container `env` entry overrides an `envFrom` variable of the same name.
- `hdfs-external-secret` is the chart's default for an **external** HDFS. The chart's own example
  (`helm-template-examples.md:266-278`) pairs it with `agent.config.hdfs.internal=false`,
  `agent.config.hdfs.configmap=…` and
  `agent.secrets.synchronizedPasswords[0]="hdfs-external-secret"` (the `synchronizedPasswords` entry is
  commented out at `values.yaml:814`, so the default doesn't sync it).
- For `agent.config.hdfs.internal: true` (our case) the chart enables `agent-without-credentials`
  (`values.yaml`, `enabled: … hdfs.internal`), which suggests an internal HDFS needs no credentials
  secret. That is an inference from the chart's structure, not a confirmed statement.
- Git history: the line was last touched by `6f307a9 [PLT-4076] Alinear values de charts con
  propertyName en lugar de internalName (#193)`, a values-alignment commit, not a deliberate decision
  about internal HDFS.

### 3. The GitOps component (`Stratio/keos-apps`, `/stratio/gitops/keos-apps/components/dg-agent/app`)

- `overlays/hdfs/base/helm-patch.yaml` sets, for the HDFS flavor: `releaseName: dg-hdfs-agent`,
  `agent.config.hdfs.customInitPath: "/"`, `agent.config.hdfs.internal: true`, Kerberos config
  (`security.type: KRB`, `configMap: keos-kerberos-config`). **It does not set
  `customStratioCredentialsDefinition` or `synchronizedPasswords`.** (The `s3` overlay does:
  `customStratioCredentialsDefinition: "customer-s3-secret"`, `overlays/s3/base/helm-patch.yaml:18`.)
- So the HDFS render inherits the chart default `hdfs-external-secret` unintentionally, apparently an
  oversight.

### 4. The tenant file entry (`keos-fleet`, `clusters/eosdev/tenants/config/stratio.yaml`, `components.dgAgent`, entry `dg-hdfs-agent`)

- `config.dependencies.hdfs.name: hdfs1`, `type: hdfs`, and a `patches:` list with **one** HelmRelease
  patch: `agent.config.common.{discoveryCronPeriod: 5000, discoveryFullCronPeriod: "0 0 0 * * ?", javaOpts: -Xmx3072m}`
  and `agent.config.dfs.dfsParallelismLevel: 4`. Nothing about credentials.
- That file is modified but uncommitted in the `keos-fleet` checkout. **Do not push or edit it** as
  part of this task: the owner does not want RSIP changes pushed to the cluster yet.

### 5. The catalog type (`internal/config/seed.go:141-160`)

- Type `dg-agent`, component key `dgAgent`, `chart.path: dg-agent`, matched on the CCT annotation
  `application_service` in (`connectors-dfs`, `connectors-rdbms`). No `exclude` entries.

## Why the plugin can't fix it by itself

1. Not through `.Values`: see the `| default` above.
2. Not through the plugin's inline-env patching (`internal/diff/inline.go`): that only patches a
   container `env` entry that **already exists in the rendered chart** with the rendered value. This
   variable is rendered from a ConfigMap, so there is nothing to match. Carrying it would be a new
   capability: *add* an inline env entry that overrides an `envFrom` variable. It must replace the chart's
   whole `controllers.dg-agent.containers.agent.env` list (Helm replaces lists, as in
   `docs/TASK-keos-apps-env-list-overlays.md`), so it would also have to re-state `SERVICE_ACCOUNT_NAME`
   and `HOST`.
3. Excluding it (as the catalog does for Vault roles, `Exclude` paths) just hides the difference
   and keeps the render's value; it doesn't preserve live.

## The decision to make

What does `hdfs-external-secret` do to the migrated agent, versus a blank?

- **A. Harmless / unused for an internal HDFS.** The agent never reads that secret when
  `hdfs.internal: true` + Kerberos. Then accept the difference: document it (e.g. in the catalog
  seed comment next to `dg-agent`) and treat the warning as known. Nothing else to do.
- **B. Harmful or unknowable.** The agent resolves the name (Kubernetes Secret, or a Vault path) at
  startup or on first discovery and fails or logs errors when it's absent. Then the legacy blank must
  be preserved, and the fix has to go in one of:
  1. **`Stratio/charts`** (cleanest): make the template emit `""` when `agent.config.hdfs.internal` is
     true, e.g. in `dg-agent_hdfs_env_vars.yaml:9`. Also consider `values.yaml:636`.
  2. **`Stratio/keos-apps`** (hdfs overlay): can't set a blank (`| default`), so only works together
     with (1) or by overriding the env inline as in point 2 above.
  3. **This plugin**: new "override an envFrom variable with an inline env entry" capability in
     `internal/diff/inline.go`. Most invasive; the user's rule asks for it only if (1) is not
     possible.

## Unknowns (verify before answering; do not state as fact)

- What the agent (`dg-custom-agent_2.12`, image tag `1.1.1`) does with `CUSTOM_STRATIO_CREDENTIALS`:
  whether it's a Kubernetes Secret name, a Vault path component, or ignored under KRB/internal. No source
  or docs were read. Ask the Governance team, or read the image's config docs.
- Whether the chart actually renders `agent.secrets.synchronizedPasswords` in a way that creates or
  needs `hdfs-external-secret` for an internal HDFS (it's commented out, and `_common.tpl:32` fails
  on an empty list, so check what the render really contains: `helm template` with the keos-apps
  overlay values).
- How the CCT descriptor `agent-dfs-hdfs-internal` produced a blank: it doesn't set the variable at
  all (valueless entry), so a descriptor default of "unset" is likely.
- Whether the bjw-s controller env supports `value: ""` for an overriding entry, if option B3 is
  chosen (verify with `helm template`).

## Universe reference

Any look at the CCT descriptor (`agent-dfs-hdfs-internal`, its `parentConfiguration` chain, the image
tag) is done against **release 15.1 of `kubernetes-universe`** (`/stratio/p/kubernetes-universe`,
`origin/branch-15.1`), never `master`/14.x; record the commit SHA you read. Use
`/charts:compare-descriptors` or read `packages/` there. See "Universe reference" in
`docs/TASK-charts-universe-alignment.md`.

## How to investigate (all read-only)

- `flux stratio apps diff dg-hdfs-agent` (installed binary; stdin closed, `</dev/null`).
- `helm template` of the `dg-agent` chart with the keos-apps hdfs overlay values, grepping
  `CUSTOM_STRATIO_CREDENTIALS` and `synchronizedPasswords`. The plugin's own render is in
  `internal/appdiff/chart.go` (`HelmTemplate` in `internal/diff`).
- `kubectl get deploy -n stratio-datastores dg-hdfs-agent -o yaml` (the live env entry),
  `kubectl logs` of the live agent for any mention of credentials, and `kubectl get secret -A` **names only**
  (never print Secret data).
- Do not apply, patch, or migrate anything: no `apps migrate` without `--dry-run`, no tenant-file edit,
  no push of `keos-fleet`.

## Deliverable

A short written answer (in this file under a new "Decision" heading, or to the user):

1. A or B, with the evidence for it.
2. If B: which repo gets the change, the exact change, and whether `flux-stratio` needs any change at all
   (probably only a seed comment and, once the chart emits a blank, nothing: the diff then settles).
3. If A: the one-line note to add next to the `dg-agent` seed entry, so the warning stops reading as a
   defect.

## Related

- `docs/TASK-keos-apps-env-list-overlays.md`: the same "overlay replaces a whole env list" class of
  problem (rocket), and how Helm replaces lists.
- `docs/TASK-chart-spec-comparison.md`: chart mode compares env vars only; resources, replicas, image are
  not compared.
- `docs/TASK-charts-universe-alignment.md`: re-verification of every chart against the CCT descriptors; this
  is exactly the kind of drift it looks for.
- `CONTEXT.md` §9 row "Drops a live env var that has no `value`": why a blank live variable is `""`
  here and not missing.
