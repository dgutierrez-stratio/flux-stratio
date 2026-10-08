# Task: `tenant import` must write `dependencies.<dep>.enabled: true` for the integrations the legacy environment already uses

**Status: implemented (unit-tested; `--dry-run` checked on eosdev against a scratch copy of the tenant file). Found on eosdev on 2026-10-07 after migrating rocket and intelligence; fixed there by hand (see "Manual fix").**

**What shipped:** catalog field `chart.uses` (`{dependency, values}`, seeded on `rocket` and `intelligence`, inherited by an older `catalog.yaml`), evaluated over the
entry's live HelmRelease `spec.values` (`internal/tenantimport/uses.go`). Create and Complete write `enabled: true` on that evidence (Complete through the new
`tenantfile.AddToDependency`, a hand-written `enabled: false` stays with a warning). On eosdev the dry-run proposes exactly rocket `virtualizer` and intelligence
`virtualizer`/`opensearch`/`litellm`; a second run adds nothing. **Still open:** an app that is not migrated yet has no HelmRelease, so `tenant import` only warns
("cannot tell whether it uses dependency"); reading the CCT Deployment env (rocket `GENAI_ENABLED`, ...) would decide before migration but needs the legacy env of a
real not-yet-migrated environment to write the rules from. Still to confirm after the user pushes the tenant file: `service_deploy` 200 in the genai-api audit (the contributor
role may not be the only requirement).
The fix belongs in `internal/tenantimport` (and the catalog schema), not in keos-use-cases or the charts.

Related: `docs/TASK-tenant-import-idempotent-merge.md` (how the command now adds a missing field to an existing tenant file without
regenerating it; reuse that mechanism), `docs/TASK-tenant-import-nested-config-scaffolding.md` (same class of silent drop: a field the
ResourceSet needs is missing from the scaffold and the operator is never told), `docs/migration-runbook.md` §tenant import.

## The symptom

After `apps migrate rocket` and `apps migrate intelligence`, both log (rocket: `DeployGenAIPackageActorExecutor`, attempt N, retry every 300 s):
`The GenAI packages and chains could not be registered ... Error registering service in GenAI API with status '403 Forbidden'`.
genai-api's audit log shows `POST /v1/service_deploy` from `rocket.stratio-rocket` and `intelligence.stratio-intelligence`:
`authorized=false, authType=mtls, responseCode=403`, every 5 minutes since the migration.

## Root cause (verified on the cluster; one point not verified, see below)

1. genai-api authorises callers by Gosec role (`GENAI_API_RBAC_AUTH_ENABLED=true`).
2. The rocket chart's prerequisites job assigns `rocket.stratio-rocket` to the genai role `contributor_genai_<genai>-api.<ns>` **only when
   `GENAI_ENABLED=true`** (`charts/rocket/values.yaml` ~574, `config/rocket_prerequisites.yaml`).
3. `GENAI_ENABLED` is `dependencies.genai.enabled` of the tenant entry. keos-use-cases (`apps/components/resourceset-apps-rocket.yaml:172,205`,
   same for intelligence) reads it with `get $genaiDependency "enabled" | default false`.
4. The tenant file written by `tenant import` has `genai: {name: genai}` and **no `enabled`**, so it is `false`: the prerequisites job ran
   with `GENAI_ENABLED=false` (its log shows no role assignment), the Kustomization drops its `dependsOn apps-genai`, and the live
   HelmReleases carry `dependencies.genai.enabled: false`.
5. But the legacy app's own configuration (kept in the migration patch) has the integration on (`genAI.genaiEnabled: true`; the legacy rocket backup has
   `GENAI_CHAIN_DEPLOY_ENABLED=true`), so rocket tries to register its chains and genai-api answers 403.

The same switch exists for every dependency the ResourceSets gate: rocket `virtualizer`/`genai`; intelligence `opensearch`/`virtualizer`/`genai`/`litellm`
(`resourceset-apps-intelligence.yaml:160-176,205-218`). On eosdev all of them were `false` in the migrated rocket/intelligence entries although the legacy apps
use them (at least virtualizer for rocket; check the rest). The eosdev genai entry shows the scaffolder (or a person) did write `enabled: true` for `discovery` and
`opensearch` but not for `litellm`/`virtualizer`: the behaviour is inconsistent, so look at how those two came to be.

Not verified: that the contributor role is the only thing `/v1/service_deploy` requires. Confirm after the fix (see Verification).

## What `tenant import` does today

`internal/tenantimport/enrich.go` `enrichEntry` resolves each `schema.Dependencies` entry to a **name** only (`entry.Deps[dep.Key] = names[0]`; empty plus a
warning when nothing is discovered) and fills `ExtraConfig` fields from schema defaults; `render.go` writes `dependencies: {key: {name: ...}}`. Nothing
ever writes `enabled`. The existing `chart.enabledBy` mechanism (`internal/tenantimport/siblings.go`) derives a sibling-driven flag (virtualizer's `uiEnabled`) from
what the cluster runs; dependency `enabled` has no equivalent.

## What to build

For every dependency that has a ResourceSet-level `enabled` switch, `tenant import` (fresh file **and** merge into an existing file) should write
`enabled: true` when the existing environment shows the dependency is in use by that component, and say so; and when it cannot tell, warn instead of leaving a silent `false`.

Signals to evaluate (pick the reliable ones, record what each proved):
- The dependency component exists in the discovered environment (e.g. a genai-api/genai-ui Deployment, a litellm release, a virtualizer, an OpenSearch) **and** the consumer's
  legacy configuration enables the integration: rocket `genAI.genaiEnabled` / `GENAI_CHAIN_DEPLOY_ENABLED`, its virtualizer/crossdata catalog host, intelligence's
  litellm/opensearch settings. The legacy values are in the captured deployment env and in the live HelmRelease/CCT config the scan already reads.
- Which ResourceSet keys actually gate something (grep `"enabled" | default` in keos-use-cases `apps/components/resourceset-apps-*.yaml`) — drive this from the catalog schema
  (a per-dependency `enabledBy`-style rule), not from hard-coded component names, like `chart.enabledBy`.
- The default must not flip anything that is genuinely off: only write `true` on positive evidence; otherwise leave it out (or write `false` explicitly) and warn.

The merge behaviour (idempotent, never overwrites a value the operator wrote, `--dry-run` shows the exact additions) comes from
`TASK-tenant-import-idempotent-merge.md`; on eosdev the new command must have added `enabled: true` to the rocket and intelligence entries without touching the rest.

## Manual fix applied on eosdev (to be superseded by this task)

`keos-fleet/clusters/eosdev/tenants/config/stratio.yaml` (working tree, the user commits/pushes): `enabled: true` added under `dependencies.genai` of the
`rocket` and `intelligence` entries. Not changed (decide with evidence): rocket `virtualizer`; intelligence `litellm`, `opensearch`, `virtualizer`.
After the push Flux upgrades both HelmReleases and the prerequisites jobs should run with `GENAI_ENABLED=true`.

## Verification

- Unit tests in `internal/tenantimport` with a fixture where the environment has genai/virtualizer/litellm/opensearch and a rocket/intelligence whose legacy config enables
  them (extend `internal/components/testdata/live.yaml` / `internal/tenantimport/testdata`): fresh import writes `enabled: true`; merge into a file that lacks it adds it; a file
  with `enabled: false` written by hand is left alone with a warning; a dependency that is absent or unused stays off.
- On eosdev against a scratch copy of the tenant file (as in the idempotent-merge task): `--dry-run` proposes exactly the additions; after applying the real change,
  the rocket prerequisites job log shows the assign-to-role step (HTTP 200), rocket logs the chain registration succeeding, and genai-api's audit shows
  `service_deploy` 200 instead of 403 for both identities.
- Watch genai-api memory: rocket/intelligence chains registering adds load (genai-api was raised to 4096Mi on 2026-10-07).

## Constraints

- Never commit or push keos-fleet; do not edit its `stratio.yaml` beyond what the user asked (it is their test bed).
- Do not change the ResourceSets in keos-use-cases to default these flags to `true`: `false` is the right default for an environment that does not use the integration.
