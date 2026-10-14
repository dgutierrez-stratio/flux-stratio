# TASK-01 — prepare steps delete live objects they did not back up, and run ahead of an unresolved Flux dependency

**Status: open (plugin work). Cluster side: decided, no action.**
Found in the eosdev batch-migration audit of 2026-10-09 (see `TASK-00-eosdev-audit-index.md`).
Plugin to change: `flux-stratio` (this repo). Branch to work on: `feat/application-migration`.

## Why this exists (what happened)

On 2026-10-09 the user ran `flux stratio apps migrate --all` against eosdev (tenant `stratio`),
answering `y` to every prompt, without a `--dry-run` survey first. While it ran, `dlc-entity` printed:

```
[REVIEW] dlc-entity depends on 1 entr(y/ies) the tenant file doesn't declare
    Flux holds apps-dlc-entity back until each name is fixed.
  DEPENDENCY  NAME    DECLARED
  rocket      rocket  none declared
```

and then, because the user answered `y` to the next question, the prepare step ran anyway:

```
[ACTION] prepare step "prepare-dlc": remove the legacy DLC ingress and deployment ...
  dlc-entity-dlc-entity.stratio.k8s.e…  delete  Ingress     stratio-dlc
  dlc-entity                            delete  Deployment  stratio-dlc
✔ backed up "DLC Entity dlc-entity" to ~/.fluxcd/flux-stratio/backups/dlc-entity/2026-10-09T10-54-14Z before its prepare step
◎ 1/2 delete Ingress ...   ◎ 2/2 delete Deployment ...
```

The user does **not** want to migrate `rocket` yet, so the tenant file the user then committed
(`keos-fleet` commit `a3cc8605`) declares neither `dlcEntity` nor `rocket`. Result: the legacy
Deployment and Ingress were deleted and Flux has nothing to replace them with. Only
`service/dlc-entity` and the PgDatabase `dlc-entity` remain; the DLC feature is down.

Two plugin defects came out of this.

### Defect A — the Ingress was deleted without any backup

The prepare step's backup contains only `deployment.yaml` and the env-var files:

```
~/.fluxcd/flux-stratio/backups/dlc-entity/2026-10-09T10-54-14Z/
  deployment.yaml  env-vars.env  env-vars.deployment.dlc-entity.env
```

The deleted object `Ingress stratio-dlc/dlc-entity-dlc-entity.stratio.k8s.eosdev.labs.stratio.com`
is recorded nowhere (not in `backups/`, `backups_old/`, `tenant-file-backups/`, events). It can only be
reconstructed by hand from the CCT descriptor, which is why the output text "Changes the live cluster,
**after a backup of the app**" over-promises: the backup covers the app's workload/CR, not the objects
the step deletes.

Code locations found by the audit subagent (verify before relying on them):

- `internal/backup/backup.go` (~lines 435, 557): `backup.Run` captures workloads and CRs only.
- `internal/cli/apps_migrate.go` (~line 335): `backupBeforePrepare` only checks that *something* was captured.
- `internal/prepare/step_dlc.go`: deletes Ingress objects (`ReplacesWorkloads: true`) with no capture.
- `CONTEXT.md` section 6 ("Diff, migrate, backup, drift"), `internal/cli/review_prepare.go`.

### Defect B — a prepare step runs while Flux cannot render the replacement

The review already knew, before the prepare question, that Flux would hold the app back (undeclared
dependency). Nothing connected the two: the destructive step was offered with the same `[y/N]` as any
other app, and with `--yes` it would not even have asked (`--yes` stops only on `warning` severity
diffs, and an undeclared dependency is a `[REVIEW]`, not a warning).

## Decisions already made (do not re-litigate)

- **Cluster:** the user decided to leave `dlc-entity` down until `rocket` is enabled, and to include it
  with `rocket` then. No restore. Nothing else in the cluster references `dlc-entity` (searched
  Deployments, StatefulSets, ConfigMaps, Services, Ingresses). Saved as memory
  `dlc-entity-down-until-rocket`.
- **Do not re-run `apps migrate dlc-entity` (or `--all`) before `rocket` is declared**: `prepare-dlc`
  would delete whatever is there again.
- **Restoring legacy objects stays manual** (user's standing rule: no generic "delete/restore legacy
  workload" step; per-component prepare steps only on request). Do not add an `apps restore` command
  unless the user asks.
- The solutions belong in `flux-stratio` (reference tool for legacy to GitOps migration).

## Proposed changes (implement all of A and B; C is optional, ask first)

**A. Back up everything a prepare step deletes or modifies.**
Before running a step's `[]Operation`, serialise each target object (`kubectl get -o yaml` equivalent
via the existing `kubeclient`) into the same backup directory, e.g.
`<backup>/prepare/<kind>.<namespace>.<name>.yaml`. Keep the existing UID-pinning. Then make
`backupBeforePrepare` fail if any object the step is about to delete has no file in the capture.
Update the `[ACTION]` text so it says what the backup contains. Add the objects to `apps diff --drift`
only if cheap; otherwise out of scope.

**B. Refuse (or escalate) a prepare step that Flux cannot follow with a replacement.**
When the app's review reports an undeclared dependency (the same data that produces
`dlc-entity depends on 1 entr(y/ies) the tenant file doesn't declare`), the prepare step must not run
on a plain `y`. Choose one and implement consistently for interactive and `--yes`:
(1) error out naming the missing dependency and how to fix it, with `--skip-prepare` as the documented
escape; or (2) ask a second, explicit question that states "this deletes live objects and Flux will not
recreate them until X is declared". Recommended: (1) for `--yes`, (2) interactively.

**C. (optional, ask the user) two-phase `--all`.**
Plan every app first (patch, review, prepare operations), print all `[REVIEW]`/`[ACTION]` together,
take one confirmation, then apply. The runbook (step 7) already tells people to survey with
`--dry-run`; this would make the safe path the default. Larger change; do not start without approval.

## Acceptance criteria

- A test (use `runner.Fake` / the existing fake kube client patterns in `internal/prepare` and
  `internal/cli`) where `prepare-dlc` plans an Ingress + Deployment delete: both objects appear in the
  backup directory before the first delete; if the Ingress capture fails, nothing is deleted.
- A test where the app has an undeclared dependency: the prepare step does not delete anything under
  `--yes`, and the error names the dependency; interactively the second question appears.
- `[ACTION]` wording matches the backup's real contents.
- `CONTEXT.md` section 6 (and `docs/migration-runbook.md` steps 6-7) updated; `CHANGELOG.md` entry.
- `go test ./...` passes.

## How to reproduce / verify

```
# on a cluster with a legacy dlc-entity and a tenant file with no components.rocket
flux stratio apps migrate dlc-entity --dry-run        # shows the REVIEW and the [ACTION] together
flux stratio apps migrate dlc-entity                  # before the fix: deletes Ingress + Deployment
ls ~/.fluxcd/flux-stratio/backups/dlc-entity/<ts>/    # before the fix: no Ingress file
```

## Reference: how to rebuild the lost Ingress by hand (if the user ever asks)

From the descriptor `/stratio/p/kubernetes-universe/packages/dlc/dlc-entity.json`
(`data.networking.expose`) and a comparable live legacy Ingress
(`stratio-apps/discovery-discovery.stratio.k8s.eosdev.labs.stratio.com`):
name `dlc-entity-dlc-entity.stratio.k8s.eosdev.labs.stratio.com`, ns `stratio-dlc`,
`ingressClassName: default-ingress-class`, no TLS, host `dlc-entity.stratio.k8s.eosdev.labs.stratio.com`,
path `/dlc-entity(/|$)(.*)` Prefix, backend service `dlc-entity` port name `app` (8080; 8091 is
metrics), annotations `rewrite-target: /$2`, `backend-protocol: HTTPS`, `force-ssl-redirect: "true"`,
`proxy-body-size: "0"`, connect/send/read timeouts 300, `x-forwarded-prefix: /dlc-entity`, plus the
`cct.stratio.com/*` labels/annotations copied from the Deployment and labels
`capsule.clastix.io/managed-by=stratio`, `projectcapsule.dev/tenant=stratio`. Not verified: whether the
descriptor also adds oauth `auth-url`/`auth-signin` annotations (discovery has them). The Deployment
restores from the 10:54:14Z `deployment.yaml` after stripping `status`, `uid`, `resourceVersion`,
`creationTimestamp`, `generation`, `managedFields` and `deployment.kubernetes.io/revision`.

## Requirement: investigate before changing (see "Migration principle" in `TASK-00`)

Do not take the legacy behaviour as the target. Before implementing A and B, investigate and record, with
evidence, whether each of these makes sense in the GitOps scenario and why:

- **Why `prepare-dlc` deletes the Ingress at all.** The catalog text says the legacy Ingress "would collide
  with the GitOps-managed one". Compare the legacy name (`dlc-entity-dlc-entity.<host>`, pattern
  `<app>-<service>.<host>`) with what the `dlc-entity` chart renders (name, host, path, annotations,
  class). If the names differ there is no collision and the deletion may be unnecessary; if they are the
  same, say what must be preserved from the legacy one (annotations such as `rewrite-target`,
  `backend-protocol`, timeouts) and whether the chart already reproduces them.
- **Whether a prepare step should delete at all when the replacement cannot exist yet** (Defect B), or
  scale to 0 / keep the legacy objects until Flux has taken over. Consider the same question for the other
  automated prepare steps (`prepare-datarest`, `prepare-discovery`, `prepare-virtualizer`, dg-agent): each
  deletes a legacy object; for each, note what makes the deletion necessary (immutable selector, collision)
  and whether a safer order exists.
- Any other object type a prepare step removes that the backup does not capture (Defect A is the Ingress;
  check ServiceAccounts, Roles, ConfigMaps, ServiceMonitors for the other steps).

## Related

- `TASK-00` index: the PgDatabase name-collision defect (issue 06) is why `apps diff dlc-entity` errors
  in every mode today; fix that separately.
- Audit artifact with all raw outputs: https://claude.ai/artifact/7TbG1ew9A6CGCMUDShFDx4
