# TASK-14 — dg-hdfs-agent: its GosecPolicy is stuck in ERROR (gosec-services-daas NullPointerException) and HDFS discovery fails, and the two are NOT the same problem

**Status: open. Analysed 2026-10-14 (read-only; verdict and recommendation below, awaiting the user's decision on the open points). No cluster, GoSec, Vault or repo change made for this finding.**

> The other TASK files this one cites (TASK-00 to TASK-12 and the un-numbered `TASK-*.md`) were retired on 2026-10-14. Their content is summarised in `docs/MIGRATION-HISTORY.md` and the files remain in git history.

Found in the eosdev legacy->GitOps migration run of 2026-10-13 (see `docs/MIGRATION-HISTORY.md`).
Tools to change, by finding: `gosec-services-daas` (`/stratio/gosec/gosec-services-daas`, GoSec team), `gosec-operator` (source not available locally, image
`gosec-operator:0.2.1-rc.2`), the `dg-agent` chart (`Stratio/charts`, worktree `/stratio/aws/flux/.worktrees/charts-align-15.1.0`) and, for the discovery symptom, only
eosdev configuration. `flux-stratio` needs **no** code change for the 500 (see option F).
Related: `TASK-02` (gosec agents), `TASK-03` (dg-hdfs-agent values: `DFS_*` variables), `TASK-12` (chart variable decisions), `TASK-00` index.

## Requirement for this task

Read "Migration principle" in `TASK-00`. Keep the legacy configuration as close as possible; classify each option **preserve legacy**, **accept new** or **needs a product
decision**, with evidence. Governance data (datastore rows) is changed only through the Governance API, never in the database.

## Correction first

The finding as handed over said: the chart's HDFS policy "reuses or clashes with the legacy policy id or name created by Command Center", the policy 500 is the root suspect for
the failed HDFS discovery. **Both halves turned out wrong or incomplete:**

1. The operator did not clash with the legacy policy, it *adopted* it by name, which is its normal path (same as the other policies). The 500 is a bug in
   gosec-services-daas triggered by the **shape** of one ACL in the policy, not by the id or the name.
2. The 500 does not cause the discovery failure. GoSec already holds exactly the ACLs the chart wants (they come from the legacy descriptor), so the denial the namenode applies is
   the legacy policy's own, and the connector then refuses to publish an empty catalogue. The discovery failure and the 500 are two independent problems that happen to hit the same agent.

## What happened

`dg-hdfs-agent` (namespace `stratio-datastores`) went from the legacy Command Center app to `dg-agent@15.1.0-PR258-SNAPSHOT` at about 14:08Z on 2026-10-13. The chart creates
`GosecUser dg-hdfs-agent.stratio-datastores`, `GosecPolicy dg-hdfs-agent.stratio-datastores-hdfs-acl` and the matching `GosecBinding`. From that moment:

- the GosecPolicy has `globalStatus: ERROR` (`Gosec response: code 500 ... Internal server error`) and the operator retries it on every reconcile (every ~16.7 min);
- every HDFS discovery of the agent ends status 6 "Not Valid" (`[SSCC-HDFS] Can't open /`), requests 5 to 9 in `dg_discovery.discovery_request`.

## Evidence

### A. The GosecPolicy and the 500 (confirmed down to the source line)

CR (cluster-scoped, Helm-managed, created 2026-10-13T14:08:53Z):

```
kubectl get gosecpolicy dg-hdfs-agent.stratio-datastores-hdfs-acl -o yaml
spec.id: 5817a8f1-7e51-4e25-a650-75f1ee440a20      (written back by the operator, not by the chart)
spec.services[0] = hdfs / instance hdfs1.stratio-datastores
  acl 1: file "/"     recursive  actions: [Read]
  acl 2: file "/user" recursive  actionsDeny: [Read]            <- no `actions` at all
status: ERROR  "Gosec response: code 500, description: Internal server error. ..."
```

Operator (`keos-core/gosec-operator-*`, container `manager`), first reconcile, 14:08:53Z:

```
Getting policy info   {"policy id": null, "policy name": "dg-hdfs-agent.stratio-datastores-hdfs-acl"}
policy dg-hdfs-agent.stratio-datastores-hdfs-acl just exists, proceed to alter it
GosecPolicy ... with id 5817a8f1-... succesfully patched - policy id updated
gosec returned an invalid code while altering the policy. Response: {"code":500,"message":"internal_server_error",...}
```

So the policy was found **by name** (it pre-existed in GoSec, created by Command Center) and its id was adopted. For comparison, a brand-new chart policy logs `GoSec Policies not found in
response body` on the first lookup and is created; both paths end in the same `PUT /policies/<id>`.

gosec-services-daas (`gosec-services-daas-*`, image `2.5.6`; the retained log starts 06:39Z, so the 14:08Z occurrence has rotated out). Six occurrences kept, 06:54, 07:10, 07:27, 07:44, 08:00,
08:17Z, each:

```
ERROR ... com.stratio.gosec.services.daas.error.ErrorHandler {"@message":"internal_server_error","@data":{}}
java.lang.NullPointerException                 <- no stack trace (the JVM drops it for a repeated NPE)
AUDIT gosec-operator.keos-core ... action=PUT, resource=/policies/5817a8f1-..., responseCode=500,
  entity={... "acls":[{"resourceType":"file","name":"/","recursive":true,"actionsAllow":["Read"]},
                      {"resourceType":"file","name":"/user","recursive":true,"actionsDeny":["Read"]}] ...}
```

Why only this one: every other `PUT /policies/<id>` the operator sent (26 other policy ids, hundreds of calls) answered 204. Of all PUT/POST bodies in the retained log, the **only** ones with an ACL that
has `actionsDeny` and no `actionsAllow` are the six failing ones (`grep -E '"recursive":(true|false),"actionsDeny"'`). Policies that carry deny actions next to allow actions
(`genai-api.stratio-genai-governance-opensearch`, id `1840f39e`) answer 204.

Source (`/stratio/gosec/gosec-services-daas`, local checkout `master` @ `c312697`, 2025-02, **older than the deployed 2.5.6, re-check there**):

| File:line | What |
|---|---|
| `src/main/java/.../web/dto/AclRequest.java:52` | `private List<String> actionsAllow;` plain Lombok field: null when the JSON omits it |
| `AclRequest.java:72-86` | `getActionsDeny()` / `setActionsDeny()` are null-safe (return/store an empty list). **There is no equivalent for `actionsAllow`.** |
| `src/main/java/.../web/dto/DtoUtils.java:343-344` | `List<String> aclsAllow = acl.getActionsAllow(); for (String aclAllow : aclsAllow) {` -> NullPointerException when `actionsAllow` is null |
| `DtoUtils.java:213-218` | `postToPolicy` and `putToPolicy` both go through `reqToPolicy` (line 304), so a **create** of the same policy fails the same way |

The legacy Command Center task sends `actionsAllow [], actionsDeny [Read]` explicitly (see C), which is why the legacy policy could be created. The GosecPolicy CRD
(`kubectl get crd gosecpolicies.gosec.stratio.com`) has `actions` and `actionsDeny`, neither required; the operator evidently omits an empty/absent `actions` from the PUT (its source is not local).

### B. The policy in GoSec is already what the chart wants (read-only SELECTs on the GoSec schema `gosecschema` in `postgreskeos`)

```
dyplon_policy:  5817a8f1-7e51-4e25-a650-75f1ee440a20 | dg-hdfs-agent.stratio-datastores-hdfs-acl | description NULL | auditable t | tenant stratio | status Enabled
dyplon_service_policy: service hdfs1.stratio-datastores, service_version 4e472038-... (hdfs 1.1.4)
dyplon_user_policy:   dg-hdfs-agent.stratio-datastores   (no group, dyplon_group_policy empty)
dyplon_permission:    "/"      Read  is_allow t  is_recursive t
                      "/user"  Read  is_allow f  is_recursive t
dyplon_user:          dg-hdfs-agent.stratio-datastores, created 2026-10-13 10:11:16 (by Command Center), tenant stratio
```

| Field | Legacy policy (in GoSec) | Chart policy (CR) |
|---|---|---|
| id | `5817a8f1-...` | same (adopted by name) |
| name | `dg-hdfs-agent.stratio-datastores-hdfs-acl` | same |
| tenant / service / instance | stratio / hdfs / `hdfs1.stratio-datastores` | same |
| ACLs | allow Read `/` recursive; deny Read `/user` recursive | same |
| users / groups | user `dg-hdfs-agent.stratio-datastores`, no groups | binding subject = that user, no groups |
| description | NULL | "Data Governance Agent Policy to access HDFS" |

The **only** difference is the description, and it is the one change the failing PUT would have applied. Nothing about `stratio-BDL-governance` or any group is involved.

### C. Where the policy comes from

- Legacy: `kubernetes-universe` `packages/connectors/agent-hdfs-internal-default.json`, `lifecycles.install."install/pre"`, task `gosec/policies/create`, lines 227-250
  (15.1.0 pre-release commit `54ab1918`; the ACL strings date from `a4ec6ec1`):
  name `${Application.id}-hdfs-acl`, users `["${Application.id}"]`, ACLs `ACL(resourceType file, name /, actionsAllow [Read], actionsDeny [], recursive true)` and
  `ACL(resourceType file, name /user, actionsAllow [], actionsDeny [Read], recursive true)`. `${Application.id}` is `dg-hdfs-agent.stratio-datastores` on eosdev.
- Chart: `dg-agent/values.yaml:97-117` (`gosecPolicy.dg-agent-hdfs-internal-acl`, name `{{ .Release.Name }}.{{ .Release.Namespace }}-hdfs-acl`, deny ACL at lines 111-117) and
  `values.yaml:129-136` (`gosecBinding`). It is a faithful copy of the descriptor, except that `actions: []` is not written for `/user`.
- Rendering: `common/templates/classes/_gosecPolicy.tpl:18-58` (`bjw-s.common.class.gosecPolicy`). It never writes `spec.id`; name only (`:21`, `:41`) and
  `services` passed through unchanged (`:55-57`). The id comes from the operator, by name. So "how the chart derives the id": it does not, and that is correct.
- Only `dg-agent` (HDFS) has a deny-only ACL. `genai/values.yaml` also uses `actionsDeny`, always next to `actions`.

### D. Why HDFS discovery fails (confirmed from the namenode and agent logs; independent of the 500)

Namenode `hdfs1-namenode-1`, one failing run (07:46:08Z; identical at 14:09:07Z on 10-13, 00:00:00Z, 07:41Z, 07:47Z), Dyplon authorizer and HDFS audit:

```
AUDIT dg-hdfs-agent.stratio-datastores  resource=/      action=Read authorized=true      (cmd=listStatus src=/      allowed=true)
AUDIT dg-hdfs-agent.stratio-datastores  resource=/user  action=Read authorized=false     (cmd=listStatus src=/user  allowed=false)
ERROR DyplonAccessControlEnforcer  Permission denied to user dg-hdfs-agent.stratio-datastores with action Optional[Read] and resource /user
```

Those are two separate `getListing` calls on one connection: `/` is listed fine, the agent then tries to descend into `/user` and the deny ACL stops it. `/` and `/user` are the **only** paths ever
checked for this principal (no `/data`, `/tmp`, ...), so `/user` is the only child of the root that the walk attempted.

Agent `dg-hdfs-agent` (07:47:39Z run, connector `sscc-hdfs 1.1.1-922b277`):

```
[HDFS Extractor] The credentials may not read '/user': Permission denied ...                      (WARN, tolerated by DFS_ON_DENIED_NODE=delete)
[HDFS Extractor] Giving up on this extraction: 1 nodes of this tree were visited, 0 of them skipped, none of them failed to be read and none of them described anything
  either, and the walk produced no resources at all. ... set DFS_ALLOW_EMPTY_EXTRACTION to `true` if this path really does describe nothing ...   (refuseTheEmptyCatalogue)
Error retrieving labelled paths and resources at [/]. [SSCC-HDFS] Can't open /
```

So the root is readable, the one thing in it is a branch the policy forbids on purpose, the walk therefore describes nothing, and with `DFS_ALLOW_EMPTY_EXTRACTION=false` (the legacy value, `env-vars.env`
of backup `2026-10-13T11-00-17Z`, same in ConfigMap `dg-hdfs-agent-common-dfs-config`) the connector refuses to publish an empty catalogue. `DFS_ON_DENIED_NODE=delete` and
`DFS_MAX_SKIPPED_NODES_FRACTION=0` are also legacy values and not the cause. Kerberos login works (principal `dg-hdfs-agent.stratio-datastores`), the namenode logs `Auth successful`.

Discovery history (`dg_discovery.discovery_request`): request 1, 2026-10-13 10:12Z, path `hdfs1.stratio-datastores:`, status 3, `Result: 0 new assets, 1 updated assets and 0 deleted assets`
(Governance datastore row 101 has 0 children); requests 5, 6, 8 (agent name `dg-hdfs-agent`) and 7, 9 (`hdfs1.stratio-datastores`, after the 07:47Z `COMM_SERVICE_NAME` revert) status 6.
The tenant already carries `customServiceName: hdfs1.stratio-datastores` (and `customInitPath: "/"`, legacy `COMM_SERVICE_INIT_PATH=/`).

### E. Side effect: duplicate datastore in Governance

`dg_metadata.data_asset`: row 101 `hdfs1.stratio-datastores:` (legacy, discovered 10-13 10:12:03Z) and row 901 `dg-hdfs-agent:` (type HDFS, subtype DS, created 10-13 14:09:02Z by the first
post-migration agent start, name = the then `COMM_SERVICE_NAME`), both with 0 children.

## Root cause

**500 (confirmed in source, version caveat):** `gosec-services-daas` dereferences `AclRequest.actionsAllow` without a null guard (`DtoUtils.java:343-344`; the deny list is guarded, the allow list is not).
The chart/operator send an ACL with only `actionsDeny`, so the PUT (and any POST) of this policy throws a NullPointerException. The policy already exists in GoSec with the right content, so no
permission is lost, but the CR stays ERROR and the description is never applied. Unconfirmed: that 2.5.6 has the same code as the older local checkout (very likely, the log shows the same bare NPE);
the 14:08Z stack trace (rotated out); whether the operator would send `actionsAllow: []` if the chart wrote `actions: []` (operator source not available).

**Discovery (confirmed mechanism, one open fact):** the agent's policy (legacy and chart alike) denies Read on `/user`; `/user` is now the only entry under `/`; the connector's guard against publishing
an empty catalogue (`DFS_ALLOW_EMPTY_EXTRACTION=false`, legacy value) refuses the round. Neither setting changed in the migration. **Open:** why legacy request 1 reached status 3 at 10:12Z. Best
explanation: HDFS was empty then (`numBlocks 0`, TASK-05) and `/user` did not exist yet, so the walk did not hit a denial; but an empty root should also count as "visited, described nothing" and I could
not reproduce which branch of the connector accepted it. Also open: who created `/user` and when (namenode logs before 14:03Z on 10-13 are gone; `hdfs dfs -ls /` needs a Kerberos ticket and was not run).
If `/user` predates 10:12Z the explanation is wrong and a second cause exists, so check this first (see "Verify").

So the original migration is not what broke discovery: the legacy agent would hit the same refusal on today's HDFS. What the migration changed is that it made the problem visible (new agent
start, new discovery rounds, a stuck CR).

## Fix options

Classification uses the TASK-00 vocabulary.

### 1. The 500 (CR in ERROR)

| Opt | Repo / action | Class | Notes |
|---|---|---|---|
| A | `gosec-services-daas`: make `actionsAllow` null-safe like `actionsDeny` (getter/setter in `AclRequest`, or `Optional` in `DtoUtils.java:343`). Add a test: PUT/POST an ACL with only `actionsDeny`. | accept-new (bug fix, neutral for clients) | Root fix; also repairs POST. Needs a release that reaches eosdev (image bump in keos-system-services). Recommended. |
| B | `gosec-operator`: always send `actionsAllow` (empty list) in the DaaS body. | accept-new | Equivalent effect, but the source is not local; ask the operator owners. Worth doing too, it protects against every other DaaS validation that treats absence as null. |
| C | `dg-agent` chart `values.yaml:111-117`: write `actions: []` on the `/user` ACL, mirroring the descriptor's `actionsAllow []`. | preserve legacy (the descriptor says exactly that) | One-line, no cross-team dependency, **but unconfirmed**: if the operator drops empty `actions` it changes nothing. Test on eosdev by reconciling the HelmRelease and watching the CR status; harmless if it does not help. Good interim. |
| D | Chart: remove the `/user` deny. | needs-decision | Fixes the 500 and the discovery (the deny is gone) but changes what Governance may read (home directories become discoverable). Decide with the product side, see 2. |
| E | Manual GoSec action (UI/API: set the description, or delete and recreate the policy). | needs-decision | Not needed: the DB content already matches. Do not delete the policy, the agent's access depends on it. |
| F | flux-stratio `prepare-dg-agent` that removes or adopts the legacy policy. | **reject** | There is no clash to resolve: the operator adopts by name. Deleting the policy would cut the agent's access until the operator recreates it, and the recreate would fail with the same NPE (POST). No plugin change. |

Recommendation for the 500: A (and B) in the GoSec repos, C as a cheap interim in the chart to test the operator's behaviour.

### 2. The discovery failure

| Opt | Action | Class | Notes |
|---|---|---|---|
| G | Leave `DFS_ALLOW_EMPTY_EXTRACTION=false` and the `/user` deny. Rounds fail until HDFS holds something the agent may read; the catalogue is untouched. | preserve legacy | Safe, and matches the connector's design (it protects the catalogue). Status 6 on an HDFS whose only content is a forbidden branch is expected, not a migration bug. Recommended for production. |
| H | Set `agent.config.dfs.allowEmptyExtraction: true` for `dg-hdfs-agent` (tenant patch, `DFS_ALLOW_EMPTY_EXTRACTION` is in `dg-agent/config/dg-agent_common_dfs_env_vars.yaml:2`). | accept-new / needs-decision | Round reaches status 3 now. Risk stated by the connector itself: a later round that wrongly sees nothing (permissions broken) would delete every asset under the path. Fine as a temporary test on eosdev, then revert. |
| I | Move the discovery root off `/` (`agent.config.hdfs.customInitPath`, e.g. a data directory). | needs-decision | Changes the legacy scope (`COMM_SERVICE_INIT_PATH=/`); the product team must say what Governance should see in HDFS. |
| J | Drop the `/user` deny (same as D). | needs-decision | Governance would walk user home directories (for example `/user/rocket.stratio-rocket`). |

Recommendation: G as the end state. Use H only to prove the diagnosis on eosdev (a status 3 with the same denial WARN in the agent log confirms it), then revert or consciously keep it.

## eosdev remediation, in order

Nothing here has been applied. Each step is reversible. Apply after the user decides.

1. **Fix the policy (CR).** Interim option C: set `actions: []` on the `/user` ACL (chart `values.yaml:111-117`, then push, reconcile the HelmRelease with `flux reconcile hr dg-hdfs-agent -n stratio-datastores`), or wait for fix A/B.
   Check `kubectl get gosecpolicy dg-hdfs-agent.stratio-datastores-hdfs-acl` -> `OK` and `Policy created successfully`, and in daas a `PUT /policies/5817a8f1-... 204`. Re-run the SELECT in "Reproduction": the
   description should now be set, the two permission rows unchanged. If C does not help, leave the CR in ERROR (harmless, see root cause) and continue.
2. **Decide the discovery behaviour** (options G/H/I/J). For the diagnostic test choose H via the tenant file (`stratio.yaml`, `dg-hdfs-agent` patch, `agent.config.dfs.allowEmptyExtraction: true`).
3. **Restart the agent or wait for the next discovery.** The change is an env var, so the HelmRelease upgrade restarts the pod; otherwise trigger a discovery request from Governance
   (`discoveryCronPeriod` is 5000, the full discovery runs at midnight). Do not delete anything while a round runs.
4. **Verify**: a new `dg_discovery.discovery_request` row for `hdfs1.stratio-datastores:` reaches status 3; the agent log shows the `/user` denial WARN and no `Giving up`; the namenode audit still shows
   only `/` allowed and `/user` denied (the policy is unchanged). If it does not reach 3, `/user` predates the legacy success and the diagnosis in "Root cause" is wrong; stop and reopen.
5. **Delete Governance datastore row 901** (`dg-hdfs-agent:`) **through the Governance API**, never the DB, once step 4 shows a healthy request on `hdfs1.stratio-datastores:` (row 101). Confirm 901 has no
   children first (it has none today) and that no schedule still references the name `dg-hdfs-agent`.
6. If H was only a test, revert it and re-check that the CR and the catalogue stay as they were (rounds go back to status 6, expected).

## Other affected CRs

All six gosec kinds are cluster-scoped (no tenant namespace). Statuses taken from `kubectl get gosecpolicy,gosecuser,gosecgroup,gosecrole,gosecregistercn -A` on 2026-10-14:

| CR | Status | Same problem? |
|---|---|---|
| `gosecpolicy dg-hdfs-agent.stratio-datastores-hdfs-acl` | ERROR 500 | this task |
| `gosecpolicy genai-stratio-genai-opensearch1-stratio-datastores` | ERROR 409 `already exists` | **No, different cause.** Created 08:08Z today by the genai chart; GoSec already holds a policy of that name with status `Disabled` (id `1a52453f-...`, `spec.enable: false`). The operator's name lookup filters `active=true` (daas audit: `GET /policies/v3 ... active=true`), does not find it and POSTs, daas answers `duplicate key ... model_unique (name, id_tenant)` -> 409. 72 `already exists` lines in the operator log. Belongs to the genai/gosec task (TASK-02 area); here it is the id/name clash the original hypothesis expected. |
| `gosecrole editor-genai-genai-api.stratio-genai` | ERROR 409 `Role {name:'editor_genai_genai-api.stratio-genai'...} already exists` | No, same family as the previous (created 08:04Z by the genai chart, role already present in GoSec). Not investigated further. |
| every other GosecPolicy (26), GosecUser (17), GosecGroup, GosecRegisterCN (2) | OK | W1 gosec agents (`psql-gosec-agent`, `opensearch1-gosec-agent`) have no CR in ERROR. |

GosecBindings have no `status` block at all, so "no status" is normal, not an error. No other GosecPolicy has an ACL without `actions` (checked on every policy's JSON), so this NPE cannot hit another policy today; any future deny-only ACL will.

## Acceptance criteria

- `gosec-services-daas`: a PUT and a POST of a policy whose ACL has only `actionsDeny` (no `actionsAllow` key) answer 2xx (unit test on `DtoUtils.reqToPolicy`/the controller), and the allow list defaults to empty.
- Chart: with the chosen fix, `helm template` of `dg-agent` with `agent.type: hdfs` renders the ACL as agreed; the eosdev CR `dg-hdfs-agent.stratio-datastores-hdfs-acl` reaches `OK`.
- The GoSec rows for policy `5817a8f1-...` are unchanged apart from the description: same two permissions, same user, same service version.
- The discovery decision (G/H/I/J) is recorded here with its evidence, and a new discovery request of `hdfs1.stratio-datastores:` either reaches status 3 (H/I/J) or the status 6 is documented as expected (G).
- Governance row 901 is removed through the Governance API and `dg_metadata.data_asset` keeps only row 101 for HDFS; no row is edited in the database.
- Open items resolved or explicitly left: when `/user` was created; the 2.5.6 source check; whether the operator drops an empty `actions`.
- `CHANGELOG.md` updated if flux-stratio changes (none expected); the chart change, if any, goes in the charts repo's own change log.

## Reproduction

```
kubectl get gosecpolicy dg-hdfs-agent.stratio-datastores-hdfs-acl -o yaml                       # ERROR 500, spec.id adopted
kubectl -n keos-core logs deploy/gosec-operator -c manager | grep 'dg-hdfs-agent.stratio-datastores-hdfs-acl'      # "just exists, proceed to alter it", then 500
kubectl -n keos-core logs deploy/gosec-services-daas | grep -B1 -A2 'NullPointerException'      # bare NPE (stack trace rotated)
kubectl -n keos-core logs deploy/gosec-services-daas | grep 'action=PUT' | grep 5817a8f1        # responseCode=500, body shows actionsDeny without actionsAllow
# GoSec content (SELECT only, inside postgreskeos-0, credentials sourced from the pod, never printed):
#   select p.name, r.value, a.name, pm.is_allow from gosecschema.dyplon_policy p
#     join gosecschema.dyplon_permission pm on pm.id_policy=p.id
#     join gosecschema.dyplon_resource r on r.id=pm.id_resource
#     join gosecschema.dyplon_action a on a.id=pm.id_action
#    where p.id='5817a8f1-7e51-4e25-a650-75f1ee440a20';
kubectl -n stratio-datastores logs hdfs1-namenode-1 | grep -E 'AUDIT dg-hdfs-agent|Permission denied to user dg-hdfs-agent'   # "/" true, "/user" false
kubectl -n stratio-datastores logs deploy/dg-hdfs-agent | grep -E "may not read|Giving up|Can't open"   # denial WARN, then refuseTheEmptyCatalogue
# discovery history: select id,metadata_path,status,discovery_ended_at,left(discovery_msg,80) from dg_discovery.discovery_request order by id;
```

Raw captures of this analysis: scratchpad `.../scratchpad/t14/` (daas, operator, namenode and agent logs) and `.../scratchpad/f1/gov/` (the earlier Governance evidence).

## Open questions for the user

- Is the `/user` deny in the agent policy intended (hide home directories from Governance)? If yes, the end state is G (or I), not D/J.
- On eosdev, may `allowEmptyExtraction: true` be used temporarily to prove the diagnosis (option H)?
- Who owns the daas fix (GoSec team) and the operator change, and which release should eosdev wait for?
- Should the chart interim (option C, `actions: []`) be tried on eosdev before the GoSec fix lands?
