# Investigation: chart-mode env-var diffing can mis-attribute values across sibling workloads

## Summary

`internal/diff` and `internal/appdiff`'s chart-mode diffing (used by `apps diff`/`apps migrate`
for any catalog type with `chart:` set) collapses environment variables into a single flat
`map[string]string` keyed only by **name**, across every sibling workload a chart renders. When
two sibling workloads define an env var with the same name but different meanings, a live value
found for one can get silently written into the *other's* `.Values` patch path. No error, no
warning — just a wrong value in the tenant file.

This is not a Go-port regression: the original Python client
(`/stratio/charts/charts/bin/migration/cmd_patch_chart.py`) has the exact same limitation,
almost line-for-line (see "Confirmed: inherited from the Python client" below). Fixing this
would be a genuine improvement over both tools, not a restoration of lost behavior.

## How this was found

While migrating `genai` (which was never fully live before — see below), `apps migrate genai`
wrote this into the tenant file's patch:

```yaml
genaiUi:
  general:
    identity:
      approlename: stratio-genai-genai-api   # genai-API's role name, not genai-ui's
```

`genai-ui`'s pod then crash-looped: its entrypoint tried to Vault-login as
`stratio-genai-genai-api` (403 Forbidden — that role isn't bound to genai-ui's ServiceAccount).

### Root cause, precisely

The `genai` chart renders three sibling workloads from one HelmRelease: `genai-api`, `genai-ui`,
`genai-developer-proxy`. All three name their Vault-role env var identically — `VAULT_ROLE` —
each mapping to a different `.Values` path:

```
genai/config/genai_api_env_vars.yaml:              VAULT_ROLE: {{ .Values.genaiApi.general.identity.approlename }}
genai/config/genai_ui_env_vars.yaml:                VAULT_ROLE: {{ .Values.genaiUi.general.identity.approlename }}
genai/config/genai_developer_proxy_env_vars.yaml:   VAULT_ROLE: {{ .Values.genaiDeveloperProxy.general.identity.approlename }}
```

Two separate collapsing points cause the mis-attribution:

1. **`internal/diff/chartvalues.go`, `BuildChartValuesMap`/`scanChartValueLines`** — scans every
   chart file for `KEY: {{ .Values.path }}` lines into one `map[string]string` keyed by `KEY`.
   With no `preferredRoot` (genai's catalog type doesn't set `ValuesRoot`), the tie-break is
   "last file scanned wins" (alphabetical `filepath.WalkDir` order) — `genai_ui_env_vars.yaml`
   sorts last among the three, so `VAULT_ROLE` *always* resolves to
   `genaiUi.general.identity.approlename`, regardless of which workload's value is actually being
   diffed.

2. **`internal/appdiff/chart.go`, `MergeLiveEnv`** (live side) and **`internal/diff/chartdiff.go`,
   `dedupeRenderedEnv`** (rendered side) — each merges every workload's env vars into one flat
   `map[string]string`, last-workload-wins on a name collision.

`genai-ui` never ran under the legacy CCT deployment for this tenant (it was never installed), so
`FetchLiveWorkloads` (`internal/appdiff/chart.go`) found no live counterpart for it and
contributed nothing — only genai-api's real live env vars entered `liveEnv`. But because of
collision point 1, the diff on `VAULT_ROLE` (rendered default vs. genai-api's real legacy value)
got written to `genaiUi.general.identity.approlename` instead of `genaiApi.general.identity.approlename` — the two are unrelated fields, and the tool had no way to know it picked the wrong one.

### Confirmed: inherited from the Python client

`/stratio/charts/charts/bin/migration/cmd_patch_chart.py`:

- `_build_chart_values_map` (env var name → `.Values` path): identical algorithm, identical
  last-write-wins tie-break, no per-workload scoping — this is what `BuildChartValuesMap` was
  ported from, faithfully.
- `_output_patch`'s `live_env = {}` + `.update(live_vars)` per live workload, and its
  `rendered_env` dedup (`if name not in rendered_env or not value.startswith("<")`) — the exact
  same flat-map, name-only collapsing as `MergeLiveEnv`/`dedupeRenderedEnv`.

Interesting detail: `_collect_env_vars` in the Python client *does* track a `source` (container
name or ConfigMap name) alongside each `(name, value)` pair — but that `source` is only used for
the human-readable table/list output (`_output_table`/`_output_list`), never for the actual
diff/patch computation. So the per-workload information was captured but never used to avoid the
collision, in the original tool either.

## Today's workaround (already applied, not a fix)

`internal/config/seed.go`'s `genai` catalog type now has an extra exclude entry,
`spec.values.genaiUi.general.identity.approlename`, alongside the pre-existing
`spec.values.genaiGateway.general.identity.approlename` (added for the same class of problem,
presumably hit before). This stops `apps migrate`/`apps diff` from ever recomputing *this specific
path* again — but it's a per-path patch, not a fix for the underlying collision. Any new sibling
workload, any new chart with same-named env vars across siblings, or any catalog type someone
adds without knowing to pre-emptively exclude every sibling's identity field is still at risk of
this exact silent mis-attribution, with no error or warning to catch it.

## What a real fix needs to answer

1. **Scope env-var collection per workload/container**, not globally by name. This likely means
   changing `internal/envvars.Extract`'s callers, `MergeLiveEnv`, `dedupeRenderedEnv`, and
   `BuildChartValuesMap`/`ChartDiff` to key on something like `(workloadIdentity, envVarName)`
   rather than `envVarName` alone — and reworking the `.Values` path resolution to disambiguate
   by which workload's chart template the mapping came from (the chart file path itself
   encodes this today, e.g. `genai_ui_env_vars.yaml` vs `genai_api_env_vars.yaml` — that's
   already available in `scanChartValueLines`, just discarded).
2. **Decide what "workload identity" means for matching** live values to rendered values when
   only *some* sibling workloads exist live (this migration's actual situation) — e.g. by
   container name, by which env-vars file defined the mapping, or by cross-referencing the
   `.Values` root each candidate path starts with against something workload-specific.
3. **At minimum, detect the ambiguity and refuse to guess.** If a fix that fully disambiguates
   turns out to be a bigger redesign than is worth it right now, a smaller fix is: when an env var
   name maps to *multiple* `.Values` paths across different chart files, don't silently pick
   one — route it to `UnmappedDiffs` (already exists, see `internal/diff/chartdiff.go`) for human
   review instead, the same way a value with no known path at all is handled today. This would
   have caught this exact bug loudly (a reported "can't auto-patch VAULT_ROLE, ambiguous between
   genaiApi/genaiUi/genaiDeveloperProxy" instead of a silently wrong value) without requiring the
   full disambiguation work.
4. **Check whether this affects other existing chart-mode catalog types** — grep every chart
   under the charts repo for env-var files that reuse the same key name across more than one
   workload-specific file (`grep -rn "^VAULT_ROLE:\|^<name>:" <chart>/config/*_env_vars.yaml`
   grouped by chart), to know how many catalog types are silently exposed to this today. `rocket`,
   `intelligence`, and `bdlDatarest` (multi-flavor, already has `ValuesRoot` for a related but
   different tie-break need) are worth checking first, since they're the other multi-workload
   chart-mode types in the seeded catalog.

## Where to start reading

- `internal/diff/chartvalues.go` — `BuildChartValuesMap`, `scanChartValueLines`
- `internal/diff/chartdiff.go` — `ChartDiff`, `dedupeRenderedEnv`
- `internal/appdiff/chart.go` — `chartDiff`, `MergeLiveEnv`, `workloadTargets`, `FetchLiveWorkloads`
- `internal/envvars` — `Extract` (per-workload env var extraction; already workload-scoped at this
  layer, the collapsing happens above it)
- Existing tests to extend, matching this repo's own conventions (in-package, table-driven,
  `client/fake`, `testdata/` fixtures): `internal/diff/chartdiff_test.go`,
  `internal/appdiff/chart_test.go`, `internal/backup/chart_test.go` (the latter already has a
  `deploymentWithEnv` fixture helper for genai's own siblings — a good starting point for a
  reproduction test)
- Python reference: `/stratio/charts/charts/bin/migration/cmd_patch_chart.py` (see above) — worth
  rereading in full before designing a fix, in case there's other context (e.g. why `source` was
  captured but never used) that explains a constraint this task isn't aware of yet.

## Non-goals for this task

- Don't touch the `genaiUi`/`genaiGateway` exclude entries already in place — they're a correct,
  low-risk stopgap regardless of how the underlying collision gets fixed.
- Don't assume every multi-workload chart needs fixing today — start by measuring how many are
  actually exposed (same env var name across more than one sibling's own file) before deciding
  scope.
