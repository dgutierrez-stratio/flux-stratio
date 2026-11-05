# TASK-15 — an app whose chart changes the Deployment selector has no prepare step, so the operator deletes the legacy workload by hand; and the `[ACTION]` that announces it is wrong for three of the apps it fires on

**Status: implemented 2026-10-14 on `feat/application-migration` (decisions recorded under "Decisions" below; verification against a cluster still to do). Analysed the same day. No cluster change made for this finding.**

> The other TASK files this one cites (TASK-00 to TASK-12 and the un-numbered `TASK-*.md`) were retired on 2026-10-14. Their content is summarised in `docs/MIGRATION-HISTORY.md` and the files remain in git history.

Found in the eosdev legacy->GitOps migration run of 2026-10-13/14 (see `docs/MIGRATION-HISTORY.md`; finding M2 of the migration report, "immutable selector with no prepare step").
Plugin to change: `flux-stratio` (this repo). Branch to work on: `feat/application-migration`.
Related, do not duplicate: `TASK-01` (prepare-step safety, backup of deleted Ingresses and other objects, undeclared-dependency guard) and `TASK-02` (gosec agent duplicates, same `[ACTION]` fix in its item 1).

## Requirement for this task (user constraint, read first)

The user declined a **generic** "delete the legacy workload" prepare step. The cutover is the operator's process (`CONTEXT.md` section 2: this plugin is not the cutover engine; standing rule in memory `legacy-cutover-stays-manual`).
What the user accepts is a **component-scoped** step **when requested**, the way `prepare-dlc` and `prepare-virtualizer` are: each one names its app, finds only that app's legacy objects, and leaves everything else alone
(`prepare-virtualizer` deletes only Deployments, found by `config.App.Family`, and leaves Ingresses, Services and the loadbalancer Service). The user decides **app by app**.

Consequences for the implementer:

- Do **not** add a catalog-wide default, a `--delete-legacy` flag, an automatic "if the selector differs then delete" rule, or a step shared by apps that nobody asked for. `replaceLegacyDeployment` (`internal/prepare/step_replace_deployment.go:20-31`) is a helper that builds one named step; it is not a mechanism that triggers by itself.
- Implement **only the rows of the table below the user marks "implement"**. The "needs user decision" column is open for every row; ask before coding a row, then record the answer here.
- The `[ACTION]` message fix and the `prepare-genai` backup fix are not new deletion behaviour; they correct what the plugin says and does today, and need no per-app approval.
- Say in each step's `Description`, in `config-reference.md` and in the catalog `Notes` exactly which objects the step deletes or scales and which it leaves.

## What happened

The GitOps charts give each Deployment the selector `app.kubernetes.io/{name,instance,component}`; CCT's legacy Deployments select on `cct.stratio.com/application_id=<name>.<namespace>`. `spec.selector` is immutable, so a HelmRelease that meets a legacy Deployment **of the same name in the same namespace**
fails; the legacy one must be removed before Flux installs the release. `apps migrate` prints, for each such app:

```
[ACTION] N workload(s) can't be updated in place: their selector is immutable
    Delete the legacy workload before Flux installs the release. Only an app with a prepare step does it for you.
```

On eosdev the orchestrator handled each by hand, keeping copies of the manifests (`logs/W2`, `W5`, `W6`, `W7` under the session scratchpad):

| App (wave) | What the plugin printed and did | What was done by hand |
|---|---|---|
| `dg-datarest-pgi` (W2) | `[ACTION]` selector; `prepare-datarest` ran and deleted only the Ingress `dg-datarest-pgi-admin.<host>` (`W2/dg-datarest-pgi-migrate.log:80-89`) | Deployment deleted by hand. The `[ACTION]` stayed on screen next to a step that "completed", which made the gap easy to miss |
| `eureka-agent` (W2) | `[ACTION]`, no step | Deployment deleted by hand (legacy was crash-looping, unrelated) |
| `genai-api`, `genai-ui` (W5) | `[ACTION]` for both; `prepare-genai` ran its SQL: `UPDATE 7` (`W5/genai-migrate.log`) | Both Deployments deleted by hand. **No backup was taken before the SQL** (see "The prepare-genai backup fix") |
| `rocket` (W6) | `[ACTION]`, no step | `rocket` deleted by hand; its runtime-created children `rocket-catalog`, `-debug`, `-externalservice`, `-validator` were garbage-collected (ownerReference to `rocket`) and the new server recreated them. `rocket-catalog-standalone` is a separate legacy app and stayed |
| `intelligence` (W6) | `[ACTION]`, no step | Deployment deleted by hand |
| `psql-agent` / `opensearch1-agent` | `[ACTION]` printed although the new Deployments are `psql-gosec-agent` / `opensearch1-gosec-agent` | Nothing clashes; they ran in duplicate (TASK-02) and were scaled to 0 by hand |
| `connectors-management` | `[ACTION]` printed for `connectors` | Nothing clashes (see evidence); left running on purpose: the dg-hdfs-agent and eureka jar URLs point at it |
| `governance-datamarket-agent` (W7) | `[ACTION]` printed; `prepare-datamarket-agent` suspended/scaled the legacy `datamarket-agent` with a backup (`W7/governance-datamarket-agent-migrate.log`) | Nothing clashes: the release lives in `stratio-apps`, the legacy Deployment in `stratio-datastores` |

## Evidence (code, verified 2026-10-14 at `b879e75`)

### The prepare-step framework (`internal/prepare`, `internal/cli`)

- **Registry and shape.** `prepare.Steps` (`prepare.go:187-196`) lists the steps; `Find` looks one up by name; the catalog type names it in `config.App.Prepare` (`internal/config/seed.go`: `Prepare:` at 173, 204, 221, 237, 276, 306, 367, 427). A step is a `Step` (`prepare.go:74`): `Automated`+`Plan`, or `Instructions` (manual), or `Query`; `TestSteps_ExactlyOneShapePerStep` (`prepare_test.go:105`) pins "exactly one shape" and the comment on `Query` says it is mutually exclusive with `Automated`/`Plan`.
- **Declaring operations (this is what `--dry-run` shows).** `Plan(ctx, Options)` reads live state and returns `[]Operation` (action, live object, UID-pinned `apply`). `deleteOp` (`prepare.go:274`) deletes only that UID, `Background` propagation; `patchOp` (`:288`) merge-patches `spec`. `ensurePrepared` (`internal/cli/apps_migrate.go:420-483`) prints them as an `[ACTION]` table (`prepareOpsSection`, `internal/cli/review_prepare.go:45-70`) and stops under `--dry-run` (`:456-459`, the section at `:456`).
- **Finding the legacy objects.** `legacyObjects` (`prepare.go:234`) lists a kind by `cct.stratio.com/application_id=<LiveName>.<LiveNamespace>` in the live namespace and drops anything with a Flux label (`fluxManaged`, `:222`). `planDeleteLegacy` (`:258`) = that plus `deleteOp`. `Options.IsLegacy` (`:63`) is `components.MatchesAny(app.Family, obj)` (`apps_migrate.go:489-495`): the type's `match` plus its chart siblings.
- **Idempotent ("already satisfied").** An empty `Plan` means the precondition holds: `apps_migrate.go:451-454` returns after `logger.Debugf("prepare step %q already satisfied")`. **It is a `Debugf`**, visible only with the verbose flag; an acceptance check that "a second run reports already satisfied" needs `--debug`/verbose, or promote that line to a normal-level message (recommended: `Successf`, one line, in this task).
- **Backup, then confirmation, then apply.** For an automated step: plan (`:446`), show (`:456`), `--dry-run` stops, confirm unless `--yes` (`:460-468`), **then `backupFirst()` (`:470-475`) and the operations (`:476-480`)**. `backupBeforePrepare` (`:324-339`) runs `backup.Run` (the same capture as `apps backup`: workloads of the app and its siblings as `deployment.yaml` / `<Kind>.<name>.yaml`, env files, or the CR) and prints `backed up "<app>" to <dir> before its prepare step`. The prepare step is always run **after** the patch is saved (`saveThenPrepare`, `:309-318`) and before anything is pushed, so the legacy workload is gone until the operator pushes and Flux reconciles.
- **Component-scoped Deployment deleters that already exist.**
  - `prepare-virtualizer` (`step_virtualizer.go:13-62`): lists every Deployment in the live namespace (`listIn`, `:66`) and deletes those for which `opts.IsLegacy` holds, skipping Flux-managed ones **and any with ownerReferences** (`:56`); `ReplacesWorkloads: true`.
  - `prepare-dlc` (`step_dlc.go:5-25`): `planDeleteLegacy(ctx, opts, gvkIngress, gvkDeployment)`, `ReplacesWorkloads: true`.
  - `prepare-dg-agent` / `prepare-discovery` (`step_replace_deployment.go:20-37`): `replaceLegacyDeployment(name, what)` = `planDeleteLegacy(..., gvkDeployment)`, `ReplacesWorkloads: true`; the Ingress and everything else stay.
  - `prepare-datarest` (`step_datarest.go:5-26`): **Ingresses only**, `ReplacesWorkloads` false. `TestPlanDatarest_OnlyTheAppsLegacyIngresses` (`step_datarest_test.go`) even creates a labelled Deployment and asserts it is not planned.
  - `prepare-datamarket-agent` (`step_datamarket_agent.go:15-75`): suspends the legacy HelmRelease if there is one and **scales** every legacy Deployment to 0 (`scaleToZeroOp`, `:81-100`, waits for the pods), keeps the objects; `ReplacesWorkloads` false.
- **`ReplacesWorkloads` downgrades the selector diff.** `specDiffs` (`internal/appdiff/chart.go:255-274`) sets the selector `SpecDiff` to `SpecInfo` when the app's step has `ReplacesWorkloads`, so the `[ACTION]` disappears for dlc, virtualizer, dg-agent, discovery, litellm. For datarest and genai the step exists but is not `ReplacesWorkloads`, so the `[ACTION]` stays, and its hint still says "Only an app with a prepare step does it for you", which is false for those two.
- **prepare-genai** (`step_genai.go:40-57`, SQL at `:13-29`) is `Query`-shaped, so `ensurePrepared` returns at `apps_migrate.go:426-428` into `ensurePreparedQuery` (`:504-549`).

### Why `prepare-genai` took no backup

`backupFirst` is a closure built in `migrateOne` (`apps_migrate.go:232-234`) and passed to `ensurePrepared` (`:243`), but `ensurePrepared` hands a `Query` step to `ensurePreparedQuery` **without it** (`:426-427`; the function signature at `:504` has no `backupFirst`). Only the automated branch calls `backupFirst()` (`:470-475`). Hence in `W5/genai-migrate.log` there is no `backed up ... before its prepare step` line, while `prepare-dlc` (`TASK-01`) and `prepare-datamarket-agent` (`W7/governance-datamarket-agent-migrate.log`) have one.
Even with that call added, the workload backup would not contain what the SQL rewrites: the rows of `"genai-api.<tenant>-genai".chain` (and the `genai-gateway` `endpoint` row, when that schema exists). That needs its own data backup (below). The 7 rows rewritten on eosdev (`UPDATE 7`) have no copy.

### Where the selector `[ACTION]` comes from, and why it misfires

- `internal/diff/workloadspec.go:84-95` `compareSelector` compares `spec.selector.matchLabels` of each rendered workload with its **paired live** workload and returns a `SpecRisk`. It checks neither namespace nor name.
- The pairing is built in `internal/appdiff/chart.go:404-420` `workloadTargets`: the rendered workload named `App.Object` is looked up under `App.LiveName()` (so `governance-datamarket-agent` is paired with the legacy `datamarket-agent`, `psql-gosec-agent` with `psql-agent`, `connectors` with `connectors-management`), and `getWithLiveNamespaceFallback` (`internal/appdiff/manifest.go:76-82`, used at `chart.go:422-424`) retries in `App.LiveNamespace()` when the object is not found in the rendered namespace (that is how the legacy datamarket-agent in `stratio-datastores` is found when the release lives in `stratio-apps`).
- Section: `internal/cli/review_sections.go:239-290` `specSections`, title and hint at `:284-285`, `[ACTION]` level.
- So the message fires when the **diff pairs two different objects**. A Helm install only fails when it meets a live Deployment with the **same kind, namespace and name** as the one it renders. Cases:

| App | Rendered (release) | Live legacy | Same ns+name? | Real blocker? |
|---|---|---|---|---|
| `governance-datamarket-agent` | `stratio-apps/governance-datamarket-agent` | `stratio-datastores/datamarket-agent` | no, both differ | no |
| `psql-gosec-agent`, `opensearch1-gosec-agent` | `stratio-datastores/<x>-gosec-agent` | `stratio-datastores/psql-agent`, `opensearch1-agent` | name differs | no (they run in duplicate: TASK-02) |
| `connectors` | `stratio-apps/connectors` | `stratio-apps/connectors-management` | name differs (`W2/connectors-migrate.log`: `INSTANCE_NAME connectors-management.stratio-apps`) | no. The `seed.go` Notes for `connectors` (line 262) say "under the same name"; that is wrong and should be corrected |
| `dg-datarest-pgi`, `eureka-agent`, `genai-api`, `genai-ui`, `rocket`, `intelligence` | same name, same ns as legacy | same | yes | **yes** |

## Per-app proposal

"Needs user decision" is **yes for every row that changes behaviour**: the user decides app by app and has not decided any of these yet. Order of rows follows the report's recommendation table.

| App | Today | Proposal | Class | Needs user decision |
|---|---|---|---|---|
| `dg-datarest-pgi` (`bdl-datarest`) | `prepare-datarest` deletes the Ingress only | Extend `prepare-datarest`: also delete the legacy Deployment, set `ReplacesWorkloads: true`. Ingress backup is TASK-01 defect A, not repeated here | **extend** | yes |
| `eureka-agent` | no step | New `prepare-eureka-agent` = `replaceLegacyDeployment` (Deployment only) | **implement** | yes |
| `genai-api`, `genai-ui` | `prepare-genai` (SQL), no backup | Extend `prepare-genai`: Deployment deletes for the whole genai family, a backup before the SQL, a read-only "already satisfied" check | **extend** | yes (and the order of SQL vs deletes) |
| `rocket` | no step | New `prepare-rocket`: delete `rocket` and, explicitly, its owned children; never `rocket-catalog-standalone` | **implement** | yes |
| `intelligence` | no step | New `prepare-intelligence` = `replaceLegacyDeployment`; legacy Ingress stays | **implement** | yes |
| gosec agents (`psql-agent`, `opensearch1-agent`) | none; duplicates | Fix the message (below) so it says the legacy agent keeps running next to the new one. Keep the scale-to-0 manual and document it; an optional `prepare-gosec-agent` stays TASK-02 item 4 | **implemented by TASK-16** (`prepare-gosec-agent`) | yes, only if a step is wanted |
| `connectors-management` | none | No step. Not a selector problem (names differ); legacy must keep running until the jar-URL dependency (dg-hdfs-agent `customSSCCDriverJar`, eureka `bdlAgentAdditionalJars`) points at the new service. Fix the wrong `seed.go` Notes | **blocked** | n/a |
| `governance-datamarket-agent` | `prepare-datamarket-agent` suspends/scales to 0 with a backup | No step change. The message fix removes the false `[ACTION]` (nothing clashes) | **none** (message only) | no |

## Step designs

Common to all new or extended steps:

- **Idempotency.** `Plan` returns operations only for non-Flux-managed legacy objects (`fluxManaged`), so a re-run after the cutover plans nothing and `ensurePrepared` reports the step satisfied.
- **Order vs the HelmRelease.** The step runs inside `apps migrate`, after the patch is saved and before any push; the legacy workload is down from then until the operator pushes and Flux reconciles (`preparedSection`, `review_prepare.go:97-103` already says so). Nothing in a step touches the HelmRelease. Document "run the step and push in one sitting" in the runbook. TASK-01 defect B (refuse while Flux cannot render the replacement, e.g. an undeclared dependency) applies to every step below and is **not** repeated here; do it there.
- **Backup.** `backup.Run` captures the app's workloads (the anchor and its chart siblings) before any operation, which covers every Deployment deleted below. Objects the backup does not capture (Ingresses, rocket's runtime children) belong to TASK-01 defect A's `<backup>/prepare/<kind>.<namespace>.<name>.yaml`; list them in each step's section of that task rather than inventing a second mechanism.
- **Rollback hint (all Deployment deletions).** Restore from the backup's `deployment.yaml` (or `Deployment.<name>.yaml`): strip `status`, `uid`, `resourceVersion`, `creationTimestamp`, `generation`, `managedFields` and the `deployment.kubernetes.io/revision` annotation, then `kubectl apply -f`. The recipe is the "Reference" section of `TASK-01`. Add it to the step's `[ACTION]` hint, not as a new command (no `apps restore`, the user did not ask for one).
- **Leave alone, by default:** Services, Ingresses, ServiceAccounts, ConfigMaps, Secrets, PVCs, Vault entries and every Deployment that is not the app's own legacy workload.

### `prepare-datarest` (extend)

- Find: the app's legacy Ingresses and Deployments by CCT app id (`legacyObjects`), i.e. `planDeleteLegacy(ctx, opts, gvkIngress, gvkDeployment)`; this is exactly `planDLC`, so share the implementation or write it so that the two stay consistent.
- Delete: Ingress `dg-datarest-pgi-admin.<host>` first (existing behaviour), then Deployment `dg-datarest-pgi`. Keep: Service, ServiceAccount, everything else.
- `ReplacesWorkloads: true` (this removes the false `[ACTION]`). Update `Description`, `seed.go:204-205` Notes, `config-reference.md` row, `help.go` list.
- Evidence it is right: the new chart keeps the same Deployment name and namespace; the legacy and new Ingress share host/path/class (`findings.md` line 68), which is why the Ingress has to go too.
- Tests: change `TestPlanDatarest_OnlyTheAppsLegacyIngresses` (it asserts the labelled Deployment is not planned) and `datarestApp` in `internal/cli/apps_migrate_prepare_test.go`.

### `prepare-eureka-agent` and `prepare-intelligence` (new, one line each)

```go
stepEurekaAgent  = replaceLegacyDeployment("prepare-eureka-agent",  "the legacy eureka agent Deployment")
stepIntelligence = replaceLegacyDeployment("prepare-intelligence", "the legacy intelligence Deployment")
```

- Find: the one legacy Deployment by CCT app id (`eureka-agent.stratio-datastores`, `intelligence.stratio-intelligence`; `W6/legacy-intelligence.yaml`: selector `cct.stratio.com/application_id=intelligence.stratio-intelligence`, no ownerReferences).
- Keep: intelligence's legacy Ingress `intelligence-rocket.<host>` (the chart's admin Ingress has another host; the legacy one is the only one that works today, `findings.md` line 137).
- Register in `prepare.Steps`; add `Prepare:` to the `eureka-agent` (`seed.go:177-189`) and `intelligence` (`seed.go:403-419`) types plus Notes; `TestFind`, `TestSteps_ExactlyOneShapePerStep`, `TestOnlyTheStepsThatDeleteWorkloadsReplaceThem` (`prepare_test.go:94, 105, 259`) list steps by name and need the new names.

### `prepare-rocket` (new, not `replaceLegacyDeployment`)

Facts (`W6/legacy-rocket-ns-deployments.yaml`, namespace `stratio-rocket`):

| Deployment | ownerReferences | CCT app id label | Selector |
|---|---|---|---|
| `rocket` | none | `rocket.stratio-rocket` | `cct.stratio.com/application_id=rocket.stratio-rocket` |
| `rocket-catalog`, `-debug`, `-externalservice`, `-validator` | `Deployment/rocket` | none | `app.kubernetes.io/instance=rocket-rocket-<child>` |
| `rocket-catalog-standalone` | none | `rocket-catalog-standalone.stratio-rocket` | its own CCT label |

- Find the parent with `legacyObjects` (label `rocket.stratio-rocket`, not Flux-managed). That never returns `rocket-catalog-standalone` (other app id) and never the children (no label).
- Children: list explicitly the Deployments whose `ownerReferences` name the **legacy parent's UID** and delete them after the parent, so the dry-run table shows what will go (the acceptance rule is "deletes exactly the listed objects"; relying on silent garbage collection would break it). They are runtime-created by the rocket server, so the new server recreates them; the old ones must be gone first or the new server meets `AlreadyExists`.
- **Safety rule (must be tested):** derive the children only from the legacy parent found in this run, never "every Deployment with an ownerReference in the namespace". After the cutover the new server's children have no Flux label and an ownerReference to the new `rocket`; they must never be planned. Because the new `rocket` is Flux-managed, the parent is not found, so no children are listed and the plan is empty.
- `ReplacesWorkloads: true`. Keep `rocket-catalog-standalone`, all Ingresses (`rocket-rocket.<host>`, the oauth2redirect ones), Services.
- Backup: `backup.Run` covers `rocket`; the four children are not rendered by the chart and are not captured, so they go into TASK-01's `<backup>/prepare/` capture (they are recreatable, still say so in the hint).
- Rollback: restore `rocket` from `deployment.yaml`; the restored server recreates its children.
- Note for the implementer: `planVirtualizer` skips Deployments that have ownerReferences (`step_virtualizer.go:56`); the rocket step must not copy that filter for the children.

### `prepare-genai` (extend: Deployments, backup, check)

Shape. A step is either `Plan` or `Query` today. Allow a `Query` step to also carry a `Plan` (the family Deployment deletes) and update the "exactly one shape" test and the `Step.Query` comment accordingly. (Alternative: let `App.Prepare` name several steps; larger, not recommended.)

Operations, in this order:

1. **Backup (new).** `backupFirst()` (workloads of `genai-api`, `genai-ui`, `genai-developer-proxy` if present) plus the data backup below, **before the SQL**. If the data backup fails or comes back empty, stop before any statement.
2. **SQL** with its two confirmations, unchanged (`--yes` never answers them).
3. **Deployment deletes** under the usual `[y/N]` (answered by `--yes`): every legacy Deployment of the family, found like `prepare-virtualizer` (`Options.IsLegacy`, because `genai-ui` is its own CCT app and the app-id lookup of `legacyObjects` finds only `genai-api`; `config.App.Family` already includes both siblings, `seed.go:296-300`). Skip Flux-managed. Ingresses and Services stay: the legacy genai Ingresses (host `genai.<domain>`) kept serving after the cutover on eosdev.

Why SQL before the deletes (decision to confirm): the SQL is one transaction and either rewrites everything or nothing, so a failure leaves the cluster untouched; the deletes are the less reversible part and come last. The opposite order (deletes first) stops the writers but leaves the app down if the SQL then fails.

Read-only check (so a second run says "already satisfied"): `DBQuery` gets a read-only count with the same `WHERE` as the `UPDATE` (rows holding `dg-businessglossary-api`). When it returns 0 and the Plan is empty, the step is satisfied and asks nothing. This needs one read-only exec in `--dry-run` (today `RunQuery(..., dryRun=true)` resolves the pod and execs nothing, `prepare.go:134-160`); decide whether that is acceptable (recommended yes: it is a `SELECT`).

Data backup content. Save to `<backup dir>/prepare/genai-<tenant>-chain.sql` (and `...-endpoint.sql` when `genai-gateway.<tenant>-genai.endpoint` exists) the rows the statements will change: `pg_dump --data-only --column-inserts -t '"genai-api.<tenant>-genai".chain' genai` via the same `Command` env as the SQL (`PGPASSFILE=/etc/stratio/config/pgpass`), captured from `Execer.Exec` stdout. The table is small (7 rows rewritten on eosdev). Not verified: that `pg_dump` exists in the `postgresql` container of `psql-0` (fall back to `psql -c "COPY (SELECT * ... WHERE <same predicate>) TO STDOUT"`), and the `chain` primary key (needed to write a restore snippet). Rollback hint: `psql genai < genai-<tenant>-chain.sql` after truncating the affected rows, spelled out in the step's hint.
`backupBeforePrepare` must return the backup directory (`result.Dir`, `apps_migrate.go:331-338`) so the step can write there; TASK-01 defect A needs the same, implement it once.

Questions the operator sees: pre-SQL, post-SQL (never skipped), then the Deployment delete confirmation (skipped by `--yes`). Declining any of the first two skips the step as today (`errPrepareDeclined`).

### Gosec agents (keep manual, fix the message)

No step. After the message fix the operator gets, for `psql-gosec-agent` and `opensearch1-gosec-agent`, a note that the legacy `psql-agent` / `opensearch1-agent` keeps running next to the new agent. Scaling them to 0 stays manual (TASK-02, "Decisions already made"). If the user later asks for `prepare-gosec-agent`, reuse `scaleToZeroOp` (`step_datamarket_agent.go:81-100`), find the legacy agent by CCT app id, scale not delete, and run it only after the new agent is verified; do not start it here.

### Datamarket and connectors

No step work. `prepare-datamarket-agent` already backs up and scales; only the false `[ACTION]` goes. `connectors` stays blocked; correct its catalog Notes (`seed.go:262`) and the comment above it (`:250-252`), which claim a same-name clash that does not exist.

## The `[ACTION]` message fix

Applies to this task and to TASK-02 item 1 (same condition: implement once, tick both).

1. **Condition** (`internal/diff/workloadspec.go:84` `compareSelector`, or in `specDiffs`, `internal/appdiff/chart.go:255`, which already knows `opts` and can be given the release namespace): emit the selector `SpecRisk` only when the live workload has the **same kind, namespace and name** as the rendered one (rendered namespace, or the release namespace when the rendered object has none). Otherwise emit no selector diff.
2. **When they differ but the legacy workload is still live** (gosec agents, datamarket, connectors): a `[NOTE]`, not an `[ACTION]`: "legacy `Deployment <ns>/<name>` (<replicas> replica(s)) keeps running next to the new `<ns>/<name>`" and, when the app has a prepare step, which one deals with it ("`prepare-datamarket-agent` scales it to 0"); otherwise "remove or scale it down after verifying the new one" (TASK-02 wording).
3. **When they clash** (datarest, eureka, genai, rocket, intelligence): keep the `[ACTION]`, but name exactly what blocks: one row per blocker, `KIND NAMESPACE/NAME` of the legacy object (not the rendered name), its selector, and one of three hints depending on the app's step: no step ("no prepare step exists for this app: delete `<kind> <ns>/<name>` by hand before the release is installed; the backup holds its manifest"), a step that handles it (`ReplacesWorkloads`: the downgrade to info stays), or a step that exists but does not delete the Deployment. Replace the fixed sentence at `review_sections.go:285`; the review struct needs the app's `Prepare` name (`chartReview`, built at `apps_migrate.go:212`) to choose.
4. Update `help.go:262-279` (the "Today:" list) and `CONTEXT.md` section 6.

## Tests to add (follow `internal/prepare/*_test.go`)

Fixtures: `fakeClient`, `object`, `legacyDeployment`, `assertPlan`, `applyAll` (`prepare_test.go`); family stand-in like `isVirtualizerFamily` (`step_virtualizer_test.go:17`); `kubeclient.FakeExecer` for SQL (`step_genai_test.go`).

For every implemented step (`step_<name>_test.go`):
- plan lists exactly the legacy Deployment(s), in name order, and nothing of another app, another namespace or another app id (copy `TestPlanDatarest_OnlyTheAppsLegacyIngresses`);
- after `applyAll`, the plan is empty (idempotent);
- a Flux-managed Deployment of the same name is never planned (`TestPlanDLC_NeverTheGitOpsDeployment` pattern);
- `Find(name)` is non-nil; `ReplacesWorkloads` is true; update `TestSteps_ExactlyOneShapePerStep` and `TestOnlyTheStepsThatDeleteWorkloadsReplaceThem` (`prepare_test.go:105-140, 259-265`).

Specific:
- datarest: Ingress then Deployment planned; update the existing "only ingresses" test.
- rocket: with `rocket`, four owned children and `rocket-catalog-standalone` (own app id): plan = `rocket` then the four children, never the standalone; after the cutover (new Flux-managed `rocket`, new children owned by the new UID, no Flux label on the children): plan is empty.
- genai: with `genai-api`, `genai-ui`, an unrelated app and Flux-managed Deployments: family only; SQL runs only after the data backup returned rows; a failing data backup means no `Exec` of the SQL; the count query 0 plus an empty plan means "already satisfied" and no question (`internal/cli/apps_migrate_prepare_test.go`, same style as the datarest app fixture there); `ensurePreparedQuery` calls `backupFirst` (regression for the missing backup).
- `[ACTION]`: `internal/diff/workloadspec_test.go` / `internal/cli/review_prepare_test.go`: selector diff only for same ns+name; different name or namespace gives the legacy-keeps-running note and no `[ACTION]`; the hint changes per step kind; a test per case in the table "Why the `[ACTION]` misfires".
- the "already satisfied" line is visible without the verbose flag.

## Docs to update

`docs/migration-runbook.md` (the paragraph at lines 145-153: list the new steps, say a step deletes the legacy workload until the push, add the rollback recipe pointer; step 7), `CONTEXT.md` section 6 (`internal/prepare` paragraph, lines 326-345), `docs/config-reference.md` (Prepare table, lines 187-227: new rows, `prepare-datarest` and `prepare-genai` rows rewritten, the "idempotent" note), `internal/cli/help.go:262-279`, `internal/config/seed.go` Notes (datarest, eureka, genai, rocket, intelligence, connectors), `CHANGELOG.md`.

## Acceptance criteria

For each step the user chose to implement (datarest, eureka-agent, genai, rocket, intelligence):

- `flux stratio apps migrate <app> --dry-run` lists every operation the step will perform (for rocket: the parent and the four children; for genai: the SQL and its pod, then the Deployments), changes nothing, asks nothing.
- A real run takes the backup first (the `backed up ... before its prepare step` line appears **before** the first operation, and for genai before the SQL, with the data file present), then deletes or scales exactly the listed objects and nothing else (`kubectl get` shows `rocket-catalog-standalone`, Ingresses and Services untouched).
- A second run reports the step already satisfied (visible at the normal log level) and changes nothing.
- The false `[ACTION]` no longer appears for `governance-datamarket-agent`, the two gosec agents and `connectors`; the real one names the legacy `Kind ns/name` and the right hint for `dg-datarest-pgi`, `eureka-agent`, `genai`, `rocket`, `intelligence`, and disappears for those whose step now replaces the workload.
- No catalog type gets a `Prepare` the user did not approve; no generic step or flag exists. `go test ./...` passes; docs and `CHANGELOG.md` updated.

## How to reproduce

```
# from a cluster with the legacy apps (eosdev before 2026-10-13) and the tenant file entry enabled
flux stratio apps migrate eureka-agent --dry-run      # [ACTION] 1 workload(s) can't be updated in place, no step offered
flux stratio apps migrate dg-datarest-pgi --dry-run   # [ACTION] selector + a step that lists only an Ingress delete
flux stratio apps migrate genai --dry-run             # [ACTION] selector for genai-api and genai-ui + the SQL step; no backup line in a real run
flux stratio apps migrate governance-datamarket-agent --dry-run   # false [ACTION]: release in stratio-apps, legacy in stratio-datastores
flux stratio apps migrate connectors --dry-run        # false [ACTION]: connectors vs connectors-management
# the log evidence: <scratchpad>/logs/W2, W5, W6, W7 (*-migrate.log, legacy-*.yaml)
```

## Decisions (user, 2026-10-14) and what was implemented

1. Steps for **all five**: `dg-datarest-pgi` (extend `prepare-datarest`: Ingress then Deployment), `eureka-agent` (`prepare-eureka-agent`), `genai` (extend `prepare-genai`), `rocket` (`prepare-rocket`), `intelligence` (`prepare-intelligence`).
2. `prepare-genai`: backup, then SQL, then the Deployment deletes (SQL first); one read-only `SELECT` also under `--dry-run`. Done. Deviation from the proposal: the data backup uses `psql ... COPY (SELECT ...) TO STDOUT` (written as a `COPY ... FROM stdin` script) instead of `pg_dump`, so it needs only the `psql` the SQL already uses; the `chain` primary key was not checked, so the restore recipe says "delete the rewritten rows by primary key, then load the file".
3. `prepare-rocket`: the four children are listed explicitly in the plan, derived from the legacy parent's UID found in this run. Done.
4. Gosec agents: stay manual; only the corrected message (a `[NOTE]` that the legacy agent keeps running). `prepare-gosec-agent` stays TASK-02 item 4.
5. "already satisfied" promoted from `Debugf` to `Successf`, for every step.

Not done, on purpose: no generic step, flag or catalog default; no `apps restore`; the objects the workload backup doesn't capture (Ingresses, rocket's four children) still wait for TASK-01 defect A's `<backup>/prepare/` capture (the genai data rows are the first thing written there).
To verify on a cluster (not done): `flux stratio apps migrate <app> --dry-run` for each of the five, and a real run on the tenant file that is the user's test bed.

## Related

- `TASK-00` index (the orchestrator adds the row); `TASK-01` (Ingress and other object backups in `<backup>/prepare/`, undeclared-dependency guard, the Deployment restore recipe); `TASK-02` (gosec duplicates; item 1 is the same `[ACTION]` condition); `TASK-virtualizer-decoupled-connector-jars.md` (why `connectors-management` cannot be retired yet).
- Memories: `legacy-cutover-stays-manual`, `migration-preserves-legacy`.
