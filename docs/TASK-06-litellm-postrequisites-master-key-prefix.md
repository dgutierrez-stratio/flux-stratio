# TASK-06 — the litellm post-requisites Job prefixes `sk-` unconditionally, so on a migrated cluster it authenticates with `sk-sk-…`, its API calls fail silently and the model setup never runs

**Status: open. Analysed 2026-10-14 (read-only, verdict and recommendation below, awaiting the user's decision on the open points). No cluster, Vault or repo change made for this finding.**
Found in the eosdev legacy->GitOps migration run of 2026-10-13 (see `TASK-00-eosdev-audit-index.md`; it is finding 09 there, the
file keeps the number the user asked for).
Tools to change: `keos-apps` (the Job script; checkout used by the cluster:
`/stratio/aws/flux/.worktrees/keos-apps-env-list-overlays`, branch `nojira/env-list-overlays`, PR #152). `flux-stratio` needs **no** code
change (see option c). Related: `docs/TASK-litellm-salt-continuity.md` (the `prepare-litellm` Vault copy that causes it),
`docs/TASK-litellm-gosec-groups.md`.

## Requirement for this task

Read "Migration principle" in `TASK-00`. Keep the legacy master key **byte-identical** (it is a credential other clients use); make a fresh
GitOps install and a migrated cluster behave the same; classify each option **preserve legacy**, **accept new** or **needs a product
decision** with evidence.

## What happened

The keos-apps post-requisites Job `talk-to-your-data-postrequisites-litellm` reconciles the tenant's LLM models and credentials against the
LiteLLM admin API with the master key. It builds the header as

```
MASTER_API_KEY="sk-$(cat /vault/secrets/litellm-master-api-key)"
```

On eosdev the key in Vault (`userland/passwords/genai-litellm.stratio-genai/master-api-key`) is the **legacy** one that `prepare-litellm` copied
from `master_api_key`. The legacy value already starts with `sk-` (legacy descriptor: `sk-` + 10 characters, 13 in all). The Job therefore
sends `sk-sk-…`, LiteLLM answers an auth error, `GET /credentials` and `GET /model/info` return no `credentials` / `data` array and `jq` prints
`Cannot iterate over null`. Nothing checks those calls, so the Job carries on and ends `Complete 1/1`. The server itself is fine, because its
entrypoint prepends `sk-` only when it is missing.

A **fresh** install works only by accident: the chart's `SecretsBundle` generates a random 13-character value with no prefix, the entrypoint
turns it into `sk-<value>` and the Job builds the same `sk-<value>`. The Job's rule and the server's rule agree only when the stored value
has no prefix.

Correction to the first reading ("the model setup has never run on a migrated cluster"): it ran on eosdev while Vault still held the bundle's
fresh, unprefixed key (see `TASK-litellm-salt-continuity.md`, "The symptom": the Job registered models and logged `Credential
'gpt-5.4-mini' already exists`). It stopped working once the legacy key was in place. And on eosdev it is invisible today because
`LLM_MODELS_JSON=[]`: with no model declared the per-model loop never runs, so the only calls that matter are the two unchecked GETs.
With models declared the first `POST /credentials` would get a non-2xx and the Job would `exit 1` (and retry, `backoffLimit: 100`), so that
case is loud, not silent. What is silent is: the two GETs, the stale-model cleanup, and the `DELETE`/`model/delete` calls at the end.

## Evidence

### The image commit (what the user pointed at)

`Stratio/genai-litellm` commit `7b695f5d6c05321c9eafdb5547e03b382ac52459` "[AI-453] Allow deployment using a helm chart (#73)", 2026-06-08,
files `docker/entrypoint.sh` and `CHANGELOG.md` (0.2.2 upcoming: "Configurable Vault secret names via `GENAI_LITELLM_VAULT_*_SECRET_NAME`
env vars and automatic 'sk-' prefix normalization for the master API key").

- New env vars, all read by the entrypoint (`entrypoint.sh:38-40`), defaults are the legacy underscored names:
  `GENAI_LITELLM_VAULT_MASTER_API_KEY_SECRET_NAME` (`master_api_key`), `GENAI_LITELLM_VAULT_DB_SALT_KEY_SECRET_NAME` (`db_salt_key`),
  `GENAI_LITELLM_VAULT_KEOS_KEY_SECRET_NAME` (`keos_key_secret`). They only choose which entry under
  `userland/passwords/$SERVICE_NAME/` is read (the secrets operator only accepts hyphenated names, hence the variable).
- `download_vault_passwords()` (`entrypoint.sh:117-127`) reads the three entries into `LITELLM_MASTER_KEY`, `LITELLM_SALT_KEY`,
  `GENAI_LITELLM_KEOS_KEY_SECRET`, then:
  ```
  if [[ "${LITELLM_MASTER_KEY}" != sk-* ]]; then
    WARN "Master API key from Vault does not start with 'sk-'; prepending it. ..."
    export LITELLM_MASTER_KEY="sk-${LITELLM_MASTER_KEY}"
  fi
  ```
  Prefix rule: **prepend `sk-` only when the value does not already start with `sk-`**; the Vault value is never rewritten, only the
  server's in-memory `LITELLM_MASTER_KEY`. There is **no** variable to turn the prefix off or to hand the effective key to another pod.
- Tags that contain it: `0.2.2-M1` (the commit itself), `0.2.2`, `0.2.3`; `0.3.0` / `0.3.0-65e82dc` / `0.3.0-b7465f3` are on `branch-0.3`
  (diverged from `0.2.2-M1` in the compare API) and their `docker/entrypoint.sh` carries the same lines (checked at `65e82dc` and
  `b7465f3`). `0.2.1` and earlier do not.
- **eosdev runs it**: `kubectl -n stratio-genai get deploy genai-litellm` -> `genai-litellm:0.3.0-b7465f3`. The chart sets the names
  (`Stratio/charts litellm/config/litellm_env_vars.yaml:67-70`, worktree `charts-align-15.1.0`; live ConfigMap `genai-litellm-config`:
  `..._MASTER_API_KEY_SECRET_NAME=master-api-key`, `..._DB_SALT_KEY_...=db-salt-key`, `..._KEOS_KEY_...=keos-key-secret`). Entrypoint log:
  `Retrieving pass for 'master-api-key' ... userland/passwords/genai-litellm.stratio-genai/...`, **no** "prepending it" WARN.
- Inside the running server (PID 1 environment, only booleans and length read): `LITELLM_MASTER_KEY` starts with `sk-` = true, starts with
  `sk-sk-` = false, length 13. So the Vault value starts with `sk-` and the server uses it as is.

### The Job (root cause)

`keos-apps` branch `nojira/env-list-overlays` @ `fad47b6` (what Flux applies: `kubectl -n stratio-genai get kustomization apps-litellm-postrequisites`
-> `refs/heads/nojira/env-list-overlays@sha1:fad47b6...`), file
`components/litellm/config/talk-to-your-data/postrequisites/job.yaml`:

| Line | What |
|---|---|
| 33-37 | Vault agent annotations render `.Data.pass` of `userland/passwords/${LITELLM_NAME}.ns/master-api-key` (-> `genai-litellm.stratio-genai`) to `/vault/secrets/litellm-master-api-key` |
| **122** | `MASTER_API_KEY="sk-$(cat /vault/secrets/litellm-master-api-key)"` : unconditional prefix, the bug |
| 123 | `AUTH_HEADER="X-LiteLLM-API-Key: ${MASTER_API_KEY}"` |
| 134 | `EXISTING_CREDENTIAL_NAMES=$(curl -sk ... /credentials \| jq -r '.credentials[].credential_name')`: no status check, `jq` error ignored |
| 139-142 | same for `GET /model/info` -> `MANAGED_MODELS_JSON` |
| 180-183, 197-200 | the only checked calls (`POST /credentials`, `POST model/new|update`: non-2xx -> `exit 1`) |
| 170, 211-213 | `DELETE /credentials/...` and `POST /model/delete` print the HTTP code and ignore it |
| (end, 215) | no explicit exit: the script's status is that of the last command, so any earlier failure is lost |

(The same script is at `/stratio/gitops/keos-apps/components/litellm/...job.yaml:97` on `main`; that checkout predates the PLT-4838 edits and is
not what runs.)

### Live symptom (read-only)

`kubectl -n stratio-genai logs talk-to-your-data-postrequisites-litellm-8mdpr` (Job `Complete`, 16h old, `LLM_MODELS_JSON=[]`):

```
+ MASTER_API_KEY=sk-sk-<...>
++ curl -sk -H 'X-LiteLLM-API-Key: sk-sk-<...>' https://genai-litellm.stratio-genai:4040/credentials
jq: error (at <stdin>:0): Cannot iterate over null (null)
++ curl -sk -H '...' https://genai-litellm.stratio-genai:4040/model/info
jq: error (at <stdin>:0): Cannot iterate over null (null)
Reconciling LiteLLM models for tenant stratio
Removing LiteLLM models no longer present in tenant stratio's configuration
```

The 401 itself is not in the log (`curl -s` hides the status and body); it is inferred from the `sk-sk-` header and the null arrays.

### Side finding: the Job log prints the master key

The script runs with shell tracing on (the `+` lines; `set -x` is not in the script, so it comes from `/etc/stratio/helpers.sh` or the
`keos-universe-utils` image, not verified), and the log shows the **full master key** in `MASTER_API_KEY=` and in the `curl -H` line. Anyone
who can read pod logs in `stratio-genai` gets the admin key. The same class of problem was noted for the charts' genai prerequisites job in
`TASK-litellm-salt-continuity.md`. Fix together with this task (see proposed change, item 3). Not verified: where the trace is switched on.

### Why option c is dangerous (evidence from the plugin and the operator)

`prepare-litellm` (`internal/prepare/step_litellm.go:18`) copies `master_api_key` -> `master-api-key` with `Length: 13`. The secrets operator
"keeps an existing value whose length matches and that has no `user`" (`secrets-operator/pkg/password/generic.go:96-121`, quoted
in `TASK-litellm-salt-continuity.md`, "Solution"). The chart declares `master-api-key` length 13 (`charts litellm/values.yaml:420-423`).
A value stripped to 10 characters no longer matches, so the operator would regenerate (rotate) it. Not re-verified in this task.

## Analysis of the options

| Option | What | Effect | Verdict |
|---|---|---|---|
| **a** keos-apps Job: prefix only when missing (the entrypoint's rule) + fail on non-2xx / null | The Job derives the same effective key as the server; the Vault value is read, never changed; checks make a wrong key a visible failure | Legacy key stays byte-identical; fresh installs (unprefixed 13-char value -> `sk-<value>`) and migrated clusters (prefixed -> as is) both send exactly what the server uses. Fixes both parts of the finding | **Preserve legacy** for the key, **accept new** for the Job hardening. **Recommended.** |
| **b** use the image's variable | The commit exposes no variable that carries the effective key out of the server pod, and the Job runs in another image (`keos-universe-utils:0.4.0-m.4`), so it cannot source the entrypoint or read `LITELLM_MASTER_KEY`. The only "reuse" possible is replicating the rule, which is option a. A shared helper in `keos-universe-utils` (e.g. `libs.litellm.masterKey`) would remove the duplicate rule but needs a new image release | Same behaviour as a, more moving parts | Same as a for the rule; the helper is **needs-decision** and optional |
| **c** `prepare-litellm` strips the `sk-` during the Vault copy | Vault would hold 10 characters | Breaks the byte-identity of the credential in Vault; any client reading the secret directly would change; the 13 vs 10 length mismatch makes the operator rotate the key (above), which would invalidate the master key every legacy client holds; fresh and migrated clusters would still differ in what Vault holds | **Reject.** Not a plugin change |

## Proposed change (keos-apps, NOT applied)

File `components/litellm/config/talk-to-your-data/postrequisites/job.yaml`.

1. Same rule as the entrypoint, without echoing the key:

```diff
-    MASTER_API_KEY="sk-$(cat /vault/secrets/litellm-master-api-key)"
+    # Same rule as the genai-litellm entrypoint (docker/entrypoint.sh, download_vault_passwords): the key the
+    # server uses is the Vault value, with "sk-" prepended only when it is missing. Migrated clusters carry the
+    # legacy key, which already starts with "sk-".
+    MASTER_API_KEY="$(cat /vault/secrets/litellm-master-api-key)"
+    case "${MASTER_API_KEY}" in sk-*) ;; *) MASTER_API_KEY="sk-${MASTER_API_KEY}" ;; esac
     AUTH_HEADER="X-LiteLLM-API-Key: ${MASTER_API_KEY}"
```

2. Fail on non-2xx and on an unexpected body for the two reads (and warn on the final deletes):

```diff
+    # GET helper: prints the body, fails (and stops the Job) on a non-2xx status.
+    litellm_get() {
+      local body status
+      body=$(mktemp)
+      status=$(curl -sk -o "${body}" -w '%{http_code}' -H "${AUTH_HEADER}" "${BASE_URL}$1")
+      if [[ ! "${status}" =~ ^2 ]]; then
+        echo "ERROR: GET $1 failed (HTTP ${status}); check that the Job's master key matches the server's" >&2
+        rm -f "${body}"; return 1
+      fi
+      cat "${body}"; rm -f "${body}"
+    }
-    EXISTING_CREDENTIAL_NAMES=$(curl -sk -H "${AUTH_HEADER}" "${BASE_URL}/credentials" | jq -r '.credentials[].credential_name')
+    CREDENTIALS_JSON=$(litellm_get /credentials) || exit 1
+    EXISTING_CREDENTIAL_NAMES=$(echo "${CREDENTIALS_JSON}" | jq -r '(.credentials // error("no .credentials in response"))[].credential_name') || exit 1
-    MANAGED_MODELS_JSON=$(curl -sk -H "${AUTH_HEADER}" "${BASE_URL}/model/info" \
-      | jq -c --arg prefix "${ID_PREFIX}" \
-        '[.data[] | select(.model_info.managed_by == "keos-gitops") | select(.model_info.id | startswith($prefix))]')
+    MODEL_INFO_JSON=$(litellm_get /model/info) || exit 1
+    MANAGED_MODELS_JSON=$(echo "${MODEL_INFO_JSON}" | jq -c --arg prefix "${ID_PREFIX}" \
+      '[(.data // error("no .data in response"))[] | select(.model_info.managed_by == "keos-gitops") | select(.model_info.id | startswith($prefix))]') || exit 1
```

   For the cleanup block (lines ~170 and 211-213): capture `%{http_code}` and `exit 1` when it is not 2xx (a `404` on the credential `DELETE` is
   acceptable), and end the script with an explicit `exit 0`. Because the Job has `restartPolicy: OnFailure` and `backoffLimit: 100`, a
   real auth problem then shows up as a failing Job instead of a green one. The two GETs also run when `LLM_MODELS_JSON=[]`, so a wrong key
   fails even with no model declared (the eosdev case).

3. Stop the key reaching the log: wrap the key handling so tracing is off (`{ set +x; } 2>/dev/null` before reading the file and `set -x` again after,
   if the trace is wanted) or turn the trace off for the whole script once its source in `helpers.sh` is found. Never print `AUTH_HEADER`.

No change in `flux-stratio`. Optional follow-ups (decide with the user): a doctor/preflight or a `[NOTE]` in `apps migrate litellm` that the
Job must not assume an unprefixed key; the same check for any other component that rebuilds a key from the Vault value.

## Acceptance criteria

- On eosdev (legacy key with `sk-`): the Job log shows `GET /credentials` and `GET /model/info` succeeding, no `jq` errors, no key in the log,
  and with a model declared in `config.models` the credential and model are registered (`POST /credentials`, `model/new`) and listed by the API.
- On a fresh install (bundle key without prefix): same result as before the change (regression check).
- With a deliberately wrong key (for example a test Job with `sk-wrong`): the Job fails with the explicit `ERROR: GET /credentials failed (HTTP 401)`
  message and does not end `Complete`.
- The Vault value is unchanged by the change (compare `sha256` prefix before and after; `prepare-litellm` and `legacy/master_api_key`
  untouched); the server keeps accepting the legacy key (`sk-` + 10 characters) with the existing clients (genai-api).
- `flux-operator build` of the postrequisites kustomization renders; `shellcheck`/`bash -n` on the extracted script passes.

## How to reproduce

```
kubectl -n stratio-genai get deploy genai-litellm -o wide                      # image genai-litellm:0.3.0-b7465f3
kubectl -n stratio-genai get cm genai-litellm-config -o jsonpath='{.data.GENAI_LITELLM_VAULT_MASTER_API_KEY_SECRET_NAME}'   # master-api-key
kubectl -n stratio-genai logs deploy/genai-litellm | grep -iE 'prepending|Retrieving pass for .master-api-key'             # no "prepending" WARN
# prefix check without printing the key (booleans and length only):
kubectl -n stratio-genai exec deploy/genai-litellm -c litellm -- sh -c 'tr "\0" "\n" </proc/1/environ | grep "^LITELLM_MASTER_KEY=" | awk -F= "{v=substr(\$0,index(\$0,\"=\")+1); print (substr(v,1,3)==\"sk-\"), (substr(v,1,6)==\"sk-sk-\"), length(v)}"'   # 1 0 13
# the symptom (careful: the current log prints the key; do not paste it anywhere):
kubectl -n stratio-genai logs job/talk-to-your-data-postrequisites-litellm | grep -E 'Cannot iterate over null|Reconciling'
kubectl -n stratio-genai get kustomization apps-litellm-postrequisites -o jsonpath='{.status.lastAppliedRevision}'          # which keos-apps revision runs
```

## Open questions for the user

- Accept option a (rule duplicated in the Job, hardened checks) now, or also ask for a shared helper in `keos-universe-utils`?
- Should a failing GET fail the Job even when `LLM_MODELS_JSON=[]` (recommended: yes, it is the only auth check)?
- The Job log prints the master key through shell tracing: fix here (item 3) or as its own task once the source of the trace is found?
- On eosdev the key has been in a pod log since 2026-10-13: rotate it after the fix, or accept the risk for a test cluster? (Rotating changes the
  legacy key, so only with the user's go-ahead.)

## Related

- `TASK-00` index; `TASK-litellm-salt-continuity.md` (why Vault holds the legacy key; the length-matching operator behaviour);
  `TASK-litellm-gosec-groups.md` (the same Job, groups part); `docs/config-reference.md` and `internal/config/seed.go` (litellm `Notes`).
