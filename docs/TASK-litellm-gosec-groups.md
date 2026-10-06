# Task: LiteLLM post-requisites job targets a Gosec group nobody creates (HTTP 404); make it use the groups the app uses

**Status: not started. Investigated read-only on 2026-10-06; nothing was changed.** The fix lives in the **keos-apps**
repository (and, optionally, the **charts** repository), not in flux-stratio's Go code. flux-stratio only needs a note
(see "flux-stratio side").

Related: `docs/TASK-keos-apps-env-list-overlays.md` (keos-apps branch/PR), `docs/TASK-charts-universe-alignment.md`
(charts branch/PR), `docs/config-reference.md` and `internal/config/seed.go` (the litellm catalog entry and its notes).

## Where the code lives (work in these checkouts, never in a scratch copy of keos-fleet)

| Repo | Checkout | Branch / PR |
|---|---|---|
| keos-apps | `/stratio/aws/flux/.worktrees/keos-apps-env-list-overlays` | `nojira/env-list-overlays` (Stratio/keos-apps PR #152) |
| charts | `/stratio/aws/flux/.worktrees/charts-align-15.1.0` | `dgutierrez-stratio/align-15.1.0` (Stratio/charts PR #258) |
| keos-use-cases | `/stratio/aws/flux/.worktrees/keos-use-cases-plt-4838` | `dgutierrez-stratio/PLT-4838-litellm-name` (PR #202); read only here |
| keos-fleet (eosdev) | `/stratio/gitops/keos-fleet` | **never commit or push; do not edit `clusters/eosdev/tenants/config/stratio.yaml`** — the user tests the migration there by hand |

The cluster is `eosdev` (kubectl context `kubernetes-admin@eosdev`). Read-only `kubectl get/describe/logs` is fine;
anything that changes the cluster is the user's call.

## The symptom

After migrating LiteLLM with `flux stratio apps migrate genai-litellm`, the Kustomization
`stratio-genai/apps-litellm-postrequisites` never gets ready (health check timeout after 5 minutes), because its Job
`talk-to-your-data-postrequisites-litellm` crash-loops (exit 1, `backoffLimit` 100):

```
+ echo 'Add stratio-stratio-owners to litellm admin users group'
+ POST_ADMIN_GROUP='{"gid":"stratio-litellm_admin_users", ... "gids":["stratio-stratio-owners"] ...}'
+ libs.gosec.addUserToGroupFromJson '...' keos true /secrets/ca.crt /vault/secrets/keos-universe-utils-cert.crt ...key
++ curl ... -XPATCH ... https://gosec-identities-daas.keos-core:8443/identities/groups
+ CURL_CODE_RETURN=404
ERROR: POST request to add users to group in GoSec https://gosec-identities-daas.keos-core:8443/identities/groups failed with HTTP code 404
```

It is **not** a Vault problem (the job got its certificates and the master API key) and **not** a resources problem. It
is a Gosec identities API 404: the group `stratio-litellm_admin_users` does not exist, and nothing ever creates it
(the gosec-identities-daas audit log has only PATCHes that returned 404 for `stratio-litellm_*`).

Because the job exits on its first step, **the steps after it never run**: the LiteLLM models are never re-registered
from `config.models`. That is why the running pod logs, every 30 s,
`Error decrypting value for key: api_key, Did your master_key/salt key change recently?` (expected after the migration
deliberately gave the release fresh Vault secrets, see the litellm `Notes` in `internal/config/seed.go`; the models
rows encrypted with the legacy salt must be cleaned and re-registered by this job).

## Root cause: three different group names

The release is called `genai-litellm` (the legacy name, deliberately kept: `LITELLM_NAME` = the tenant entry's name, see
below), tenant `stratio`, namespace `stratio-genai`.

| Who | Admin group it uses | Source |
|---|---|---|
| **The post-requisites job** (the thing failing) | `stratio-litellm_admin_users`, `stratio-litellm_users` | keos-apps `components/litellm/config/talk-to-your-data/postrequisites/job.yaml:90` and `:94`, hardcoded as `${TENANT_NAME}-litellm_admin_users` / `_users`: it assumes the release is called `litellm` |
| **The chart's prerequisites job** | `stratio-genai-litellm_admin_users`, `_users` (they exist, created `201` then `409`) | charts `litellm/config/litellm_prerequisites.yaml:1-16`, `{{ tenant }}-{{ .Release.Name }}_admin_users`, run by `libs.gosec.createGroup` (`litellm/values.yaml:172-173`) |
| **The app itself** (LiteLLM's gosec sync) | `genai-litellm.stratio-genai_admin_users`, `_users` (legacy names) | the tenant patch `migrate` wrote into `stratio.yaml` (`liteLlm.general.gosecIntegration.adminGroup/usersGroup`); the chart default would be `stratio-genai-litellm_*`. Delivered to the pod as `GENAI_LITELLM_GOSEC_ADMIN_GROUP` / `_USERS_GROUP` through the ConfigMap `genai-litellm-config` (charts `litellm/config/litellm_env_vars.yaml:11,19`) |

LiteLLM is the only component whose release name comes from a variable (`LITELLM_NAME`, set by keos-use-cases
`apps/components/resourceset-apps-genai.yaml:156` and `:200` from `components.litellm[].name`) and whose
post-requisites also name something after the component. The same job already uses `${LITELLM_NAME:=litellm}` for the
Vault path (`job.yaml:33,35`) and `LITELLM_SERVICE_NAME` (`job.yaml:52`); only the two group names were left hardcoded.

## What the groups do — and what they do not (this was the user's worry)

The user fears that using the chart's GitOps names would cut access to critical resources such as databases, as happened
in an earlier run of the plugin. Investigated with three read-only sub-investigations (chart, other repos, live cluster):

- **Database access does not depend on any group.** The Postgres grant is a `GosecPolicy`/`GosecBinding`
  (`genai-litellm.stratio-genai-pgacl`, charts `litellm/values.yaml:31-94`) whose **only subject is the `GosecUser`
  `genai-litellm.stratio-genai`** (the certificate CN, derived from the release name). It grants on the database
  `genai-litellm.stratio-genai` (the legacy one, kept through `liteLlm.settings.postgresql.postgresDatabase`). Verified on
  the cluster: policy and binding OK, no group is a subject. The release name and `postgresDatabase` are what protect
  database access, and both stay as they are in every option below.
- **The groups only drive LiteLLM's own authorization**: a periodic gosec sync (every 300 s, `syncOnStartup` true,
  **`deleteStaleSyncedUsers: true`**) maps membership of the admin group to LiteLLM admins and of the users group to LiteLLM
  users. Nothing else in the chart or in any repo consumes these three group names; no policy grants them anything.
- **The earlier incident was not about group names.** Nothing in the repos records one. The nearest ones: virtualizer's
  `permission denied for database "virtualizer-monitor"` (a missing Gosec binding because `uiEnabled` was false), and
  genai-ui crash-looping after `apps migrate genai` copied genai-api's Vault role into it (hence "Vault roles follow the
  GitOps naming").
- **The real risk of switching the app to the chart's default groups is membership, not databases.** The new
  `stratio-genai-litellm_*` groups are empty; with `deleteStaleSyncedUsers: true` the next sync would remove LiteLLM's
  currently synced admins and users until they are added to the new groups. It would also contradict the catalog's own
  decision: seed.go says "**Kept**: the legacy Postgres database/schema, gosec groups and autoUvicornWorkers".

**Decision (user to confirm, recommended): keep the legacy groups in the patch** and fix the job so it targets whichever
groups the app is configured with.

## Current state on eosdev (2026-10-06 ~10:25Z)

- LiteLLM pod `genai-litellm-…` Running; DB permission error only during the first seconds (cleared once the Gosec
  Postgres agent applied the ACLs at 10:11:59/10:12:22); gosec sync every 5 min with 0 errors against the legacy groups
  (`Dual-group sync: admin_group=genai-litellm.stratio-genai_admin_users, users_group=genai-litellm.stratio-genai_users`).
- Whether the **legacy users group exists** is ambiguous (the identities audit log shows GETs with 200 and no body). The
  fix must not assume it does.
- `GosecGroup` CRs: only `stratio-genai-agents` (groups of this component are created by scripts through the API, not CRs).

## The fix (keos-apps, `nojira/env-list-overlays`)

File: `components/litellm/config/talk-to-your-data/postrequisites/job.yaml` (the script is in the ConfigMap data of the
same file, lines ~85-100; the container env is at lines ~48-56).

1. **Take the group names from the app's own ConfigMap.** Add to the job container
   `envFrom: - configMapRef: { name: ${LITELLM_NAME:=litellm}-config }` (the chart's ConfigMap is `{{ .Release.Name }}-config`,
   i.e. `genai-litellm-config` here; it defines `GENAI_LITELLM_GOSEC_ADMIN_GROUP` and `GENAI_LITELLM_GOSEC_USERS_GROUP`).
   `envFrom` is resolved by the kubelet, so no extra RBAC is needed (the job's Role only grants `get secrets`).
   The Kustomization already has `dependsOn: apps-litellm` with `wait: true`
   (keos-use-cases `resourceset-apps-genai.yaml:~175-185`), so the ConfigMap exists when the job starts. Use these two
   variables in place of the hardcoded `${TENANT_NAME}-litellm_admin_users` / `_users`, in the JSON `gid`/`name`/`groupname`.
   This makes the job correct for the legacy groups (here) and for the chart's default groups (a fresh install).
2. **Create the group if missing before patching it.** `libs.gosec.createGroup <json> <ca> <cert> <key>` (from
   `/etc/stratio/gosec.sh` in the `keos-universe-utils` image) POSTs the group and treats HTTP 409 as "already exists"
   (see the prerequisites job log: `Group already exists in GoSec with HTTP code 409`). Write the group JSON to a temp
   file (same shape as `litellm_prerequisites.yaml`: `gid`, `groupname`, `name`, `tids: ["${TENANT_NAME}"]`,
   `input_source_type: "Custom"`) and call it before `libs.gosec.addUserToGroupFromJson`. Then the legacy users group
   is created if it does not exist, and an existing one is left alone.
3. Keep the rest of the script unchanged (the PATCH adds `${TENANT_NAME}-stratio-owners` to the admin group and
   `${TENANT_NAME}-users` to the users group; then the model reconcile steps run).

Optional, charts (`dgutierrez-stratio/align-15.1.0`): make `litellm/config/litellm_prerequisites.yaml` use
`.Values.liteLlm.general.gosecIntegration.adminGroup` / `usersGroup` instead of re-deriving `{{ tenant }}-{{ .Release.Name }}_…`,
so the chart creates exactly the groups the app reads and there is one source of truth (today it also creates two unused
default-named groups next to the legacy ones). Only do this if the user agrees; it changes the charts PR.

## Separate finding (do NOT fix in this task; report to the user)

The chart's `pgDatabase.litellm` (charts `litellm/values.yaml:107-122`) creates a database named after the **release**
(`genai-litellm`, PgDatabase `stratio-genai/genai-litellm`, Helm-owned) and the Gosec Postgres agent applies ACLs to it
(7034 queries at 09:51), while the app uses the legacy database `genai-litellm.stratio-genai` (PgDatabase
`stratio-datastores/genai-litellm.stratio-genai`, created 2026-10-01, **not** Helm-owned). The policy already follows
`postgresDatabase`; the PgDatabase should too (it should adopt/target `postgresDatabase`). Harmless today (an empty
extra database), but the real database's CR is outside Helm. A charts follow-up.

## flux-stratio side (small)

- `internal/config/seed.go` litellm `Notes` and `docs/config-reference.md`: add one sentence that the keos-apps
  post-requisites job must read the group names from the release's ConfigMap (so a patch that keeps the legacy groups
  keeps working), once the keos-apps change is merged. No Go behaviour changes.
- Do **not** add `liteLlm.general.gosecIntegration.*` to the litellm catalog `exclude`: the decision is to keep them.

## How to verify (read-only unless the user says otherwise)

1. Render the keos-apps component with a substituted `LITELLM_NAME=genai-litellm` and check the job spec has the
   `envFrom` and the script uses the two variables (e.g. `kubectl kustomize` / `flux build kustomization --dry-run`
   with the substitutions; look at how `flux stratio` renders in `internal/render`).
2. `bash -n` the extracted script; a shell test with stubbed `libs.gosec.*` functions that records calls: with
   `GENAI_LITELLM_GOSEC_ADMIN_GROUP=genai-litellm.stratio-genai_admin_users` it must call `createGroup` then
   `addUserToGroupFromJson` with that gid, and never with `stratio-litellm_*`.
3. On eosdev (the user applies/pushes; do not do it for them): after Flux reconciles `apps-litellm-postrequisites`, the
   Job completes, `kubectl -n stratio-genai logs job/talk-to-your-data-postrequisites-litellm` shows the model
   reconcile steps, and the `Error decrypting value for key: api_key` lines in `genai-litellm-…` stop.
4. Confirm the LiteLLM gosec sync still reports `0 errors` and that the synced admin did not disappear.

## Constraints

- Never commit or push keos-fleet; never edit its `stratio.yaml` for the user.
- Do not change `postgresDatabase`, the release name (`releaseName` = `LITELLM_NAME`), or the vault role exclusions: they
  are what keeps database access working.
- No generic "delete legacy workload" step (see memory `legacy-cutover-stays-manual`); this task needs none.
