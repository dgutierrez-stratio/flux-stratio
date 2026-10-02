# Task: keos-apps rocket (and virtualizer) overlays must not replace the chart's env list

**Status: open, low priority.** Repo: **`Stratio/keos-apps`** (not flux-stratio; this file lives here
only so the task is next to its siblings). Branch off `main` in a new worktree; **not** part of
PR #147 (litellm). Related: `docs/TASK-chart-spec-comparison.md` (item 4 warns about this),
`docs/TASK-charts-universe-alignment.md` (makes this matter more, see Why now).

## Problem

Helm replaces lists wholesale. The size overlays set a whole env list, so the chart's default entries
are silently dropped for every tenant, not just migrated ones:

- `components/rocket/app/overlays/{hdfs,s3}/{S,M,L}/helm-patch.yaml` (6 files) set
  `controllers.rocket.containers.rocket.env` (13 entries, line 11) against a chart default of 2
  entries (`rocket/values.yaml` ~602-607): `SERVICE_ACCOUNT_NAME` and
  `ROCKET_POD_NAME` (a `fieldRef metadata.name`, enabled when `server.type == rocket`). The overlay
  list has `SERVICE_ACCOUNT_NAME` + the 12 `SPARTA_BOOTSTRAP_*` entries, so **`ROCKET_POD_NAME` is lost**.
- `components/virtualizer/app/overlays/{gcs,hdfs,s3}/{S,M,L}/helm-patch.yaml` (9 files) set
  `controllers.virtualizer.containers.virtualizer.env`; today the six `VR_*` entries are identical to
  the chart default, so nothing is lost yet, but any chart default added later is dropped (latent trap).

The drop was accidental: the overlay lists were created in keos-apps 4db1af4 (PR #18, 2025-06-24);
the chart gained `ROCKET_POD_NAME` a day later (charts #51, 9e41754, 2025-06-25; gated in charts #207,
PLT-4049). `git log -S ROCKET_POD` on keos-apps finds nothing. Whether rocket-api reads it at runtime
could not be determined (consumer is inside the image, no template reads it, single replica works
without it, no errors in the live logs): treat as probably low impact but unproven.

Why a base fix and not the migration patch: it is not legacy configuration. The GitOps desired state
itself omits a variable the chart intends to render, so every fresh install has the same hole, and any
list copied into a tenant patch freezes a snapshot of the overlay's list.

## Change

### Rocket (6 files)
Delete the `env:` list from each overlay and set the same values through the chart's own paths, which
feed the same variables in `rocket/config/rocket_server_env_vars.yaml:153-173`:

`rocketServer.environment.workers.{catalogService,debugService,externalService,validatorService}.*`
(e.g. `catalogService.catalogCpusLimit`, `catalogMemRequest`, `debugService.debugLimitCpus`,
`debugRequestMem`, `externalService.externalServiceCpusLimit/Request`,
`validatorService.validatorCpuLimit/Request`, `validatorMemRequest`; read lines 153-173 for the full
map of the 12 `SPARTA_BOOTSTRAP_*` variables and make sure each one has a `workers.*` path). Keep the
overlays' `resources` as they are. If any of the 12 variables has **no** `workers.*` equivalent, stop
and report; do not drop it.

### Virtualizer (9 files)
Per overlay, compare the `env` list to the chart default
(`virtualizer/values.yaml`, `controllers.virtualizer.containers.virtualizer.env`). Where identical,
delete the list. Where it differs, keep only what differs by moving it to the matching chart value, or
report if no such value exists.

## Verification (must be shown in the PR)
For each of the 6 rocket overlays and 9 virtualizer overlays, render before and after with the
chart worktree and compare the effective container env (ConfigMap data + `env`), e.g.
`helm template` with `ci/default-values.yaml` plus the overlay's values:
- rocket: the **only** difference is the addition of `ROCKET_POD_NAME` (fieldRef `metadata.name`).
  (Already checked once for `overlays/s3/M`; repeat for the other five.)
- virtualizer: no difference.

After merge: re-run `flux stratio apps migrate rocket` on eosdev; the generated tenant patch replaces
the whole entry and should now carry the chart default list (with `ROCKET_POD_NAME`) plus the
`PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN` inline pin. This was reasoned from the plugin code
(`internal/appdiff/chart.go:169-186`, `internal/diff/inline.go:145-210`), not run: confirm it.

## Why now (ordering with the charts alignment)
Once the charts are aligned (new env variables added to the chart defaults), an overlay that replaces
the list shadows every new default for rocket and virtualizer. Do this before or together with that
alignment's rocket/virtualizer changes.

## Do not
- Do not make the plugin copy live-only env entries to compensate (a flattened fieldRef would become
  the literal `rocket`, and Secret-backed values cannot be copied).
- Do not change the chart's env list to a map form: per-item `enabled:` gating only works on list items.
