# Task: LiteLLM migration changes the encryption salt, so stored model/credential secrets stop decrypting; keep the legacy data usable

**Status (2026-10-08): solution implemented in flux-stratio (`prepare-litellm` moves the legacy Vault passwords; `doctor` checks Vault access), see "Solution" at the end. Unit-tested and dry-run on eosdev; the sandbox end-to-end run against the real secrets operator is pending the user's go-ahead. eosdev itself was fixed by hand on 2026-10-06 and cannot get its legacy values back.**
The goal of migrating LiteLLM was that, reusing the legacy Postgres database, "the data stays the same". It does not
fully: the data is still there, but whatever LiteLLM encrypted with the legacy salt cannot be read any more.
A solution may live in flux-stratio (catalog/prepare step), keos-apps, charts or the secrets operator; finding which is
the task.

Related: `docs/TASK-litellm-gosec-groups.md` (the groups 404, already fixed and pushed; this is what remains after it),
`internal/config/seed.go` (litellm entry and its `Notes`), `docs/config-reference.md`.

## Where the code lives (work in these checkouts)

| Repo | Checkout | Branch / PR |
|---|---|---|
| keos-apps | `/stratio/aws/flux/.worktrees/keos-apps-env-list-overlays` | `nojira/env-list-overlays` (Stratio/keos-apps PR #152) |
| charts | `/stratio/aws/flux/.worktrees/charts-align-15.1.0` | `dgutierrez-stratio/align-15.1.0` (Stratio/charts PR #258) |
| keos-use-cases | `/stratio/aws/flux/.worktrees/keos-use-cases-plt-4838` | `dgutierrez-stratio/PLT-4838-litellm-name` (PR #202) |
| flux-stratio | `/stratio/gitops/flux-stratio` | `feat/application-migration` (PR #1) |
| keos-fleet (eosdev) | `/stratio/gitops/keos-fleet` | **never commit/push; do not edit `clusters/eosdev/tenants/config/stratio.yaml`** (the user tests by hand) |

Cluster: `eosdev`, kubectl context `kubernetes-admin@eosdev`. Read-only `get/describe/logs` is fine. Anything that
changes the cluster, the database or Vault is the user's call: ask first. Pushing to the PR branches above is how Flux on
eosdev picks changes up (it follows them).

## The symptom (current, after the groups fix)

LiteLLM (release `genai-litellm`, namespace `stratio-genai`) logs every 30 s:

```
ERROR LiteLLM Proxy:encrypt_decrypt_utils.py:74 Error decrypting value for key: api_key,
Did your master_key/salt key change recently? Error: Decryption failed. Ciphertext failed verification
```

188 occurrences in 2 h; the first at 10:12:52Z, on the pod's first DB sync after the migration. It fires in the periodic
DB sync, right after `Found 2 MCP servers in database` / `ToolPolicyRegistry: synced ...`.

The post-requisites job (`talk-to-your-data-postrequisites-litellm`) now succeeds, and registers the models declared in
the tenant entry's `config.models`. But for a credential that already exists it logs
`Credential 'gpt-5.4-mini' already exists, leaving it untouched (set auth.token.forceRecreate: true to rotate it)`,
so the credential row that predates the migration is kept as it is, still encrypted with the legacy salt.

## Why (what is established)

- The migration keeps the **legacy Postgres database** `genai-litellm.stratio-genai` (`liteLlm.settings.postgresql.postgresDatabase`
  in the tenant patch; PgDatabase `stratio-datastores/genai-litellm.stratio-genai`, created 2026-10-01). Rows survive.
- LiteLLM encrypts provider secrets stored in its DB (model/credential `api_key`) with its **salt** (`db-salt-key`).
- The chart's `SecretsBundle` `genai-litellm` (created 2026-10-06 10:11:41Z) owns the Vault path
  `userland/passwords/genai-litellm.stratio-genai/` and declares `keystore`, `master-api-key`, `db-salt-key`,
  `keos-key-secret`: **fresh random values**. The legacy ones are underscored (`master_api_key`, `db_salt_key`,
  `keos_key_secret`); the bundle deletes keys it does not declare and the secrets operator only accepts hyphenated
  names, so they were not carried over (see the long comment above the litellm entry in `seed.go`).
- The current documented workaround (`seed.go` Notes): before cutover delete the rows encrypted with the legacy salt
  (`LiteLLM_ProxyModelTable`, `LiteLLM_CredentialsTable`) and declare the models in `config.models` so the job re-registers
  them. That only works if the tenant file still holds the provider tokens (`auth.token.secretRef`).

## What is NOT verified (do not take these as facts)

- ~~Which rows fail~~ — resolved, see "Findings" (the single credential row).
- **That the legacy salt is the cause.** It is the obvious reading of the error text, not confirmed by decrypting with the
  old salt.
- **Whether the legacy salt is still in Vault** (`userland/passwords/genai-litellm.ns/db_salt_key`, or similar) and whether
  it could be loaded into the new `db-salt-key`. If it can, no row needs deleting and no data is lost. Whether the secrets
  operator/bundle allows a pre-seeded or imported value is unknown.
- Access: reading the DB needs the postgres superuser password (`psql -U postgres` in `psql-0`, container `postgresql`
  asks for one); LiteLLM's admin API needs the new master key. Neither was read. Ask the user how they want to
  authenticate before touching either.

## Findings from inspecting the database (2026-10-06 ~11:55Z, read-only, then one manual fix)

Access: `kubectl -n stratio-datastores exec psql-0 -c postgresql -- env PGPASSFILE=/etc/stratio/config/pgpass psql -d 'genai-litellm.stratio-genai'`
(leader found with `kubectl -n stratio-datastores get pgcluster psql -o json | jq .status.pgNodes`).

- `LiteLLM_CredentialsTable`: **one** row, `gpt-5.4-mini`, created 2026-10-01 by `default_user_id` (pre-migration, hand-registered),
  with an `api_key` value. It is the only row with an `api_key` field, so it is the source of the 188 decrypt errors.
- `LiteLLM_ProxyModelTable`: three `gpt-5.4-mini` rows: two from 2026-10-01 (hand-registered, no `managed_by`) and
  `keos-stratio-gpt-5-4-mini` (created by the job today, `managed_by=keos-gitops`). All reference credential `gpt-5.4-mini`.
  Their `litellm_params` values are also stored encrypted, but they never produced a decrypt error.
- `LiteLLM_MCPServerTable`: 2 servers (`Stratio_Data`, `Stratio_Gov`, `auth_type=none`, created by `genai-api.stratio-genai`); MCP user
  credential/env-var tables are empty. Not implicated.
- So the earlier assumption that stale rows were in `LiteLLM_ProxyModelTable` was wrong for the errors; only the credential row was.

Manual fix applied (user approved the exact row): deleted that one credential row by id, **restarted the LiteLLM pod**
(its `/credentials` list is cached in memory, so the job still saw the credential as existing and skipped it), then
re-ran the job (deleted the Job, reconciled `apps-litellm-postrequisites`). The job re-created the credential with the new
salt from the tenant's token secret (new row 11:58:58Z) and the decrypt errors stopped (0 on the new pod).
Consequences for the real solution: the credential was re-created from the **Kubernetes secret** in the tenant entry, so
this only works because that token is still available; the legacy ciphertext itself was never recovered. A generic fix
must also handle LiteLLM's in-memory credential cache (restart, or delete through the API instead of SQL).

## Open questions / candidate solutions (to investigate, in this order)

1. **Preserve the salt**: can the migration (or a prepare step) put the legacy `db_salt_key` value into the new release's
   `db-salt-key` before LiteLLM starts, so existing rows keep decrypting? Look at the secrets operator
   (`/stratio/operators/secrets-operator`) for import/seed semantics and at `SecretsBundle` password types. Likewise the
   legacy `master_api_key` and `keos_key_secret` (what uses them?).
2. **Detect and warn**: `apps diff`/`migrate` could report that the legacy salt will be lost and which rows would become
   unreadable, instead of a note buried in `Notes`.
3. **Automate the cleanup**: a per-component prepare step (like `prepare-genai`, a `prepare.DBQuery`, always confirmed,
   never skipped by `--yes`) that deletes only the rows that fail to decrypt, after showing them. Only if (1) is impossible.
4. The job's `forceRecreate` knob already exists for credentials; check whether the models table needs its own path.

## eosdev state right now (2026-10-06 ~11:50Z)

- `apps-litellm` and `apps-litellm-postrequisites` Ready on keos-apps `fad47b6`; HelmRelease litellm `15.1.0-PR258-SNAPSHOT`.
- LiteLLM pod healthy except the decrypt loop; gosec sync `0 errors` against the legacy groups.
- The other genai workloads (`genai-api`, `genai-ui`) are still the **legacy CCT** deployments (label
  `cct.stratio.com/application_id`; no `apps-genai` Kustomization/HelmRelease); they show no decrypt/auth errors; their
  only errors are the governance business-glossary URL refusing connections (`dg-businessglossary-api.keos-core:60000`,
  unrelated to LiteLLM, root cause not investigated).

## Is it safe to migrate genai next? (read-only assessment, 2026-10-06)

The salt itself does not repeat there (the chart holds no salt) — but see the updates at the end: genai-api's LiteLLM *service key* does break:
- The genai chart's SecretsBundles (`secretsBundle.api/ui/developer-proxy`, `charts/genai/values.yaml:745-790`) declare
  only certificates and a `keystore` password: no salt, no encryption key. Its env var files reference no stored/encrypted
  secret other than `VAULT_ROLE` (which the catalog excludes from the patch).
- The live `genai-api` bundle already holds the same `keystore` shape; `genai-ui` has no bundle yet (the chart adds one).
Residual risks (not new): `prepare-genai` rewrites Postgres data and always asks its own confirmation; the keystore/cert
of the three workloads is reissued by the chart's bundles; the earlier genai-ui crash-loop came from copying genai-api's
Vault role (excluded now). Whether the genai database holds anything encrypted with an outside key is not known: check
the genai-api docs/code before migrating if that matters.

## Constraints

- Never commit or push keos-fleet; never edit its `stratio.yaml`.
- Do not change `postgresDatabase`, the release name (`releaseName` = `LITELLM_NAME`) or the vault approlename exclusion:
  they keep database access working.
- No generic "delete legacy workload" step (memory `legacy-cutover-stays-manual`).
- Do not delete database rows or rotate Vault secrets without the user's explicit go-ahead on the exact rows/keys.

## Update 2026-10-06 ~12:10Z: the same secret rotation also breaks genai-api's LiteLLM key (found after `prepare-genai`)

My earlier conclusion that no other service was affected ("genai-api shows no auth errors") was wrong: genai-api's chains could not
start for an unrelated reason (the dead `dg-businessglossary-api` governance name), which hid this. After the user ran
`apps migrate genai` (`prepare-genai`: `UPDATE 7`, verified 0 rows with the old name, 7 with `governance-businessglossary-api`),
the chains get past governance and then fail with:

`Failed to get model group info for endpoint 'gpt-5.4-mini'. HTTP Code 401 ... token_not_found_in_db ... Key Hash 83f57a20...`

- LiteLLM's `LiteLLM_VerificationToken` has the key registered for genai-api (`key_alias=genai-api.stratio-genai`, hash prefix
  `13a8aa04`, created 2026-10-01), but genai-api presents a key whose hash `83f57a20...` is **not in the table**.
- Every key LiteLLM shows is masked `sk-...keos` (admin, genai-api, the auto-provisioned user), i.e. they appear to be keos-issued
  keys. **Hypothesis, not verified:** they are derived from `keos-key-secret`, which the migration regenerated, so the key genai-api
  computes/reads no longer matches the stored hash. How genai-api obtains its key was not found (the genai chart only registers
  `service_id` with LiteLLM in its prerequisites job, `charts/genai/values.yaml:~331`).
- The genai chart's prerequisites job re-registers genai-api with LiteLLM when Flux applies the migrated genai; whether that heals
  the key is unknown. Check after the genai patch is applied before deciding on a manual fix.

## Update 2026-10-06 ~12:45Z: root cause of the genai-api 401 (mTLS user key derived with the rotated secret)

genai migration is deployed and Flux-green (`apps-genai`, `apps-genai-postrequisites`, HelmRelease `genai` Ready; governance rewrite fixed;
gosec-operator OOM fixed by keos-system-services PR #254 on eosdev), but genai-api still gets 401 from LiteLLM.

- genai-api authenticates to LiteLLM by **mTLS** (litellm audit: `authType=mTLS, userId=genai-api.stratio-genai, resource=/keos/models`);
  LiteLLM's keos layer then maps the cert identity to a LiteLLM key and fails: `Invalid proxy server token passed. key=83f57a20...`.
- That derived key (hash `83f57a20...`) is not in `LiteLLM_VerificationToken`; the row registered for the same user
  (`key_alias=genai-api.stratio-genai`, hash `13a8aa04`, 2026-10-01) was issued under the legacy secret. The `admin` key
  (`04a05dd3`, 2026-10-01) predates the rotation too; the key of `dgutierrez` (auto-provisioned 2026-10-06 11:47, after it) is the only post-rotation one.
  Consistent with keys being derived from `keos-key-secret` (still unverified in the LiteLLM source).
- The genai chart's prerequisites job registers genai-api with `POST /keos/service/register` and treats **409 as success**
  (`User with id genai-api.stratio-genai already exists`), so it does not re-issue the key. Re-registering would need the stale user/key removed first.
- So any LiteLLM service/user registered before the migration needs its key re-issued: genai-api (`/keos/models`, `/keos/mcp/servers`,
  model group info) and `admin`. Preserving the legacy `keos-key-secret` (and salt) in the new release would avoid this class of problem entirely.


## Manual fix log and runbook for eosdev (2026-10-06, 13:00-13:20Z) — what was done, in order

All with the user's go-ahead; each row identified by id first. PGPASSFILE route: `kubectl -n stratio-datastores exec psql-0 -c postgresql -- env PGPASSFILE=/etc/stratio/config/pgpass psql -d '<db>'`.

1. `genai-litellm.stratio-genai` DB: `DELETE FROM "LiteLLM_CredentialsTable"` (the one pre-migration credential `gpt-5.4-mini`); restart the LiteLLM pod
   (**it caches credentials/users/teams in memory — every DB-side delete needs a restart before the register/job sees it**); re-run the
   post-requisites job (delete the Job, reconcile `apps-litellm-postrequisites`) -> credential re-created from the tenant secret.
2. After `apps migrate genai`: delete from `LiteLLM_VerificationToken` the key `genai-api.stratio-genai`, from `LiteLLM_UserTable` the user, **and from
   `LiteLLM_TeamTable` the team of the same name** (the register endpoint creates user + team + key and 400s with `Team id ... already exists`
   if the team is left; with the user left it 409s and the chart's job counts that as success). Restart LiteLLM, then create a one-off copy of the
   chart's Job `genai-genai-prerequisites-1` under another name (export, strip status/uid/selector/controller labels, rename) so the whole script re-runs
   (`POST /keos/service/register` -> 200 and a new key). Deleted the copy afterwards. Result: all 7 genai-api chains `started`, 0 LiteLLM decrypt errors,
   no more 401s. A burst of `Access to AI model is not authorized` right after the register (13:18-13:19Z) cleared by itself within ~1 min.
3. NOT done: the `admin` user/key (`04a05dd3`, 2026-10-01) is also from before the rotation and probably stale; nothing depended on it during the test.
   The two hand-registered duplicate `gpt-5.4-mini` model rows (2026-10-01) were left.

Cost of the manual user/team delete: the genai-api user's LiteLLM spend counters reset (0.0105 USD; `LiteLLM_SpendLogs` rows, 34, are kept).

## Other findings worth keeping (same day)

- **gosec-operator OOM (not a migration bug, but it blocked `apps migrate genai`)**: the `manager` is OOMKilled at its 75Mi limit when genai's Gosec
  resources arrive; the webhook has no endpoints, so the HelmRelease install fails (`failed calling webhook "mgosecuser.stratio.com"`) and Helm's
  remediation uninstall **removed the running legacy genai-api/genai-ui Deployments** (downtime). The chart range `0.2.*-0` had auto-moved the operator
  from 0.2.0 to 0.2.1-rc.2 on 2026-10-02. VPA's own recommendation was ~215Mi (capped at 75Mi). Fix: Stratio/keos-system-services PR #254 (256Mi),
  used on eosdev by pointing `keosSystemServices` of the `keos` RSIP (`keos-fleet/clusters/eosdev/default/resourcesetinputproviders.yaml`) at that branch.
- **`dg-businessglossary-api` Service has no endpoints** once governance is migrated (legacy CCT Service left behind); genai's stored chain config still
  names it until `prepare-genai` rewrites it. Verified before/after: 7 chains with the old name -> 0 (`"genai-api.<tenant>-genai".chain` in DB `genai`).
- The chart's genai prerequisites job **prints the LiteLLM service key in clear in its log** (`{"key":"sk-...-keos"}`); worth fixing in the charts repo.
- genai-api uses mTLS to LiteLLM (`authType=mTLS`); LiteLLM maps the cert identity to a key derived under the (rotated) secret. Any identity registered
  before the rotation needs re-registration (see runbook). Open question still: can the legacy `keos-key-secret`/salt be carried over so none of this is needed?

## Update 2026-10-07: genai chat down = genai-api out of memory (not a config error); assessment of the migration

Backups compared (`~/.fluxcd/flux-stratio/backups/{genai,litellm}/2026-10-02T12-30-49Z`) with the live ConfigMaps/HelmRelease values: the only differences are intended
(governance URL via `prepare-genai`, GitOps Vault role names, litellm public URLs moved to the admin host) plus `genai-ui` `DISCOVERY_DATABASE` 3 -> 1 (the catalog excludes
`discoveryDatabase` with no recorded reason; one `GET /discovery/api/database` 401 seen, root cause not investigated).

Chat failure (`Chain 'genai-ui.stratio-genai-chat' is not running`): each genai-api chain is its own process (~200-295 MB). With all 7 chains running (rocket x3 and intelligence x2
only started once governance and the LiteLLM key were fixed) the 2048M limit carried over from the legacy deployment was exceeded: 328 "killed by the system" lines from 2026-10-06 13:22Z,
the chat chain's restart failed at 15:01Z (503) and it stayed stopped. Fix tested live (not yet in git): `controllers.api.containers.api.resources` limits 2 cpu / 4096Mi, requests 500m / 3072Mi
-> all 7 chains started, 0 kills, 2259Mi used. Persist it in the tenant patch (keos-fleet `stratio.yaml`, `genaiApi` `controllers.api.containers.api.resources`).
Cluster has room (workers 62-88% memory requested, 50-64% used); no other app needs shrinking (largest waste: rocket-catalog/rocket-debug, hdfs1-zk).

Plugin follow-ups: warn when legacy `resources` are carried into a chart whose workload count grew (genai-api chains: 2 GiB fits <= 5 chains) or add a post-migration "all chains running"
check; document/verify the `discoveryDatabase` exclude; genai-ui/genai-api networking still carries legacy host/path (only works via the legacy ingress; decide before cutover).
LiteLLM noise: ~660 `CacheCodec.deserialize ... LiteLLM_UserTable` warnings because user and team `genai-api.stratio-genai` share an id (cache returns the team for the user key); no functional impact seen.


## Solution (2026-10-08): `prepare-litellm` moves the legacy Vault passwords before the chart is applied

### What eosdev's Vault showed (read-only, root token via `keos-core/vault-unseal-keys`, `vault-root`)
- `userland/` is **kv v1**: no versions, a deleted entry is gone for good.
- `userland/passwords/genai-litellm.stratio-genai/` holds only `db-salt-key`(32), `keos-key-secret`(32), `master-api-key`(13),
  `keystore`(14), none with a `user` field. The underscored legacy entries are **gone**: the chart's SecretsBundle deletes every
  entry it doesn't declare in its folder (`secrets-operator/pkg/bundle/passwords.go:36-51`, subfolders excepted) on its first
  reconcile. The legacy values on eosdev cannot be recovered; the manual runbook above stays the fix there.
- Legacy lengths (descriptor `genai-litellm-default.json:1580-1617`: `sk-`+10, 32, 32, 14) equal the chart's, and the operator
  keeps an existing value whose length matches and that has no `user` (`pkg/password/generic.go:96-121`).
- Only other underscored entry on eosdev: `datamarket-agent.stratio-datastores/jwt_secret` (candidate for the same move later).

### What the step does now
Before deleting the legacy Deployment, `prepare-litellm` (`internal/prepare/step_litellm.go`, `vaultmove.go`), for
`db_salt_key`/`keos_key_secret`/`master_api_key` in `userland/passwords/<live name>.<ns>/`:
1. backs the original up verbatim to `legacy/<name>` (survives the bundle);
2. copies its `pass` to the chart's hyphenated name, `{pass}` only;
3. never writes or deletes the original (the bundle removes it once the chart is applied).
All decided read-only in Plan: chart entry already holding another value (release has its own: moving would rotate a live
secret), a legacy value of another length, or `userland/` not kv v1 = error before any write; originals gone = warning only.

Idempotency (apps migrate re-runs every step): Plan starts with `InspectPasswords` (mount, then list the folder and `legacy/`),
reads only entries that exist, and plans only what doesn't hold yet; a run interrupted after its backups is completed from
them. Each write re-reads its target when it runs and refuses one that changed since the plan (chart applied while the prompt
waited; kv v1 has no CAS, so this narrows the window). After the cutover (no legacy Deployment, the release's SecretsBundle
exists) the step doesn't reach Vault at all. Vault is reached the operator's way through new package `internal/vault` (exec into
`keos-core/vault-0` container `vault`, token on stdin, `Data` redacted, no delete).

`flux stratio doctor` `vault access`: for each legacy app whose prepare step moves Vault passwords, it makes the same reads
(`prepare.InspectPasswords`; names only, no value) and reports what the step would do; required then, skipped (no Vault touched)
when no such app is left.

### Verified
- `make fmt-check vet test`; lint clean on the changed packages (4 pre-existing findings elsewhere).
- eosdev, read-only: `go test -tags e2e -run TestE2E_InspectPasswords` with `FLUX_STRATIO_E2E_APP=stratio-genai/genai-litellm` reads
  the real folder: kv v1, entries `db-salt-key keos-key-secret keystore master-api-key`, no `legacy/`, the 3 legacy passwords not in
  Vault. `doctor`: vault access skipped (LiteLLM already migrated, nothing to read). `apps migrate litellm --dry-run`: nothing planned,
  Vault not reached; keos-fleet untouched.
- Pending (writes to Vault, needs the user's go-ahead): `FLUX_STRATIO_E2E_CONTEXT=kubernetes-admin@eosdev go test -tags e2e -run TestE2E_PasswordMoves -v ./internal/prepare/`
  seeds a sandbox folder `userland/passwords/flux-stratio-e2e.stratio-genai/`, runs the moves, applies a throwaway SecretsBundle and
  checks the operator keeps the moved values, removes the originals and leaves `legacy/`. Its `legacy/` entries are then deleted by hand.
