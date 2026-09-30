# Investigation: chart-mode env-var diffing mis-attributed values across sibling workloads

**Status: resolved.** Chart mode now matches env vars per workload (see "The fix" below). This
file keeps the investigation for context.

## What happened

While migrating `genai`, `apps migrate genai` wrote this into the tenant file's patch:

```yaml
genaiUi:
  general:
    identity:
      approlename: stratio-genai-genai-api   # genai-API's role name, not genai-ui's
```

`genai-ui`'s pod then crash-looped: its entrypoint tried to log in to Vault as
`stratio-genai-genai-api` and got 403 Forbidden, because that role isn't bound to genai-ui's
ServiceAccount.

The `genai` chart renders sibling workloads from one HelmRelease: `genai-api`, `genai-ui` and
`genai-developer-proxy`. Each one's `config/*_env_vars.yaml` sets the same names from its own
`.Values` root:

```
genai/config/genai_api_env_vars.yaml:              VAULT_ROLE: {{ .Values.genaiApi.general.identity.approlename }}
genai/config/genai_ui_env_vars.yaml:                VAULT_ROLE: {{ .Values.genaiUi.general.identity.approlename }}
genai/config/genai_developer_proxy_env_vars.yaml:   VAULT_ROLE: {{ .Values.genaiDeveloperProxy.general.identity.approlename }}
```

Chart mode collapsed env vars into flat maps keyed by name alone. It did this in three places:
- the chart-wide name → `.Values` path map (`BuildChartValuesMap`);
- the merged live env of every workload (`MergeLiveEnv`);
- the deduped rendered env of every ConfigMap and container (`dedupeRenderedEnv`).

A value from one sibling could therefore be written into another sibling's `.Values` path, with no
error and no warning.

## Did the Go port break it?

The algorithm was ported faithfully from the Python client
(`/stratio/charts/charts/bin/migration/cmd_patch_chart.py`: `_build_chart_values_map`,
`live_env.update(...)`, and the `rendered_env` dedup). **The tie-break order is what changed, and
that flipped genai onto the wrong sibling:**

- **Python** walks files with `Path.rglob("*")`, which follows the filesystem's directory order:
  arbitrary, and different on each machine. On the checkout checked, it visits
  `genai_ui` → `genai_developer_proxy` → `genai_api`, so **genai-api** wins every shared name.
- **Go** walks with `filepath.WalkDir`, which is always alphabetical, so `genai_ui` /
  `genai_developer_proxy` win.

Rebuilding both maps against the real chart shows **15 genai keys** that Python sends to
`genaiApi.*` and Go sent to `genaiUi.*` or `genaiDeveloperProxy.*`: VAULT_ROLE/HOST/PORT/PROTOCOL/ENABLE,
GOSEC_SERVER_HOST/PORT, GOSEC_CACHE_TTL, SSO_HOST, VIRTUAL_HOST, SERVICE_NAME, KUBERNETES_NAMESPACE,
DEPLOYMENT_ENVIRONMENT, EOS_TENANT and INGRESS_PROXY_TIMEOUT.

Python only looked right by luck. It would still mis-attribute values once genai-ui is live too
(the normal installation), because its live env merge is also by name only.

The Python-era excludes carried into `internal/config/seed.go` show the same bug being hit before,
with whatever file order each author's machine produced:
- `genaiGateway…approlename`, `virtualizerMonitor…approlename`;
- `genaiUi.settings.generalProperties.governanceUrl`, which is genai-api's `GOVERNANCE_URL`
  mis-attributed to genai-ui.

Other catalog charts had keys the two tools resolved differently: virtualizer 6, rocket 22,
dg-agent 23, bdl-datarest 118 (with `ValuesRoot`). Many of these came from scanning flavor files the
release doesn't even render (`rocket_{{ .Values.server.storage.type }}`,
`dg-agent_{{ .Values.agent.type }}`).

A second port regression turned up during the investigation. `UnmappedDiffs` and the live-only count
were computed but never printed. When every difference was unmapped, `apps diff`/`migrate` reported
"no differences". The Python client printed both.

## The fix

The chart itself records which file feeds which workload. Every catalog chart uses the bjw-s
`configMapsFromFile` helper: each `config/*_env_vars.yaml` becomes its own ConfigMap, and each
workload reads its ConfigMaps through `envFrom`. For example, Deployment `genai-api` reads
`genai-api-config`, which is built from `genai_api_env_vars.yaml`.

- **`internal/diff`**
  - `ScanChartFiles` keeps the chart files apart.
  - `AttributeConfigMaps` matches each rendered ConfigMap to the file whose top-level keys equal the
    ConfigMap's data keys. `ValuesRoot` settles identical flavor files.
  - `ChartDiff` compares each live workload with its own rendered workload. A differing value is
    patched through the path in the file it actually came from.
  - What can't be told is reported in `UnmappedDiffs` with a reason, never guessed:
    - ambiguous between candidate paths;
    - conflicting live values from siblings sharing one path;
    - no path in its own file.
- **`internal/appdiff`**
  - Pairs each rendered workload with its live counterpart.
  - Before/After lines are prefixed with the workload's name when a chart renders several.
  - `MissingWorkloads` names rendered workloads with no live side.
- **`internal/backup`**
  - Also writes one `env-vars.<kind>.<name>.env` per live workload.
  - `env-vars.env` (merged) stays as the backup marker and as the fallback for older backups.
- **Baseline** (`--baseline`) uses the per-workload files. An old flat-only backup is compared
  against every rendered workload at once, with shared names reported as ambiguous.
- **Drift** compares the per-workload files when both sides have them, so a change in one sibling
  isn't hidden by another sibling's same-named variable.
- **Siblings in the catalog** (`chart.siblings`): a legacy CCT install has no HelmRelease to
  enumerate the chart's workloads by, and CCT deployed each sibling as its own app. genai's
  `genai-ui`/`genai-developer-proxy` and virtualizer's `virtualizer-monitor`/`virtualizer-ui` are
  declared as siblings. Classification attaches each one to its anchor's instance, so a backup
  captures them too and `--baseline`/`--drift` compare every sibling with its own rendered
  workload.
- **CLI**: `apps diff`/`apps migrate` list the unmapped differences, missing workloads and live-only
  count, and no longer say "no differences" when some need manual review.
- **Catalog**: Vault roles follow the GitOps naming. The genaiUi/genaiGateway approlename excludes
  stay, and `genaiApi…approlename` and `genaiDeveloperProxy…approlename` are added.

Checked against a local render of each catalog chart (placeholder values): every env-vars ConfigMap
is attributed to exactly one file. The one exception is bdl-datarest's oracle/oracle11 flavors, which
have identical key sets and aren't in the catalog; those go to review.

## Remaining limits

- **Pairing is by name.** A backup's per-workload files pair with rendered workloads by live name.
  CCT and GitOps name genai's and virtualizer's workloads the same; a sibling renamed by the redesign
  would show as missing, not mis-matched.
- **Containers within one workload are still merged** (as `envvars.Extract` does). No catalog chart
  has a sidecar setting the same name as its main container.
- **Key-set attribution** fails *safely*: an unmatched ConfigMap falls back to the chart-wide
  candidates, and more than one candidate is reported for review.
