# TASK-10 — one null value in a rendered ConfigMap makes the diff read the whole ConfigMap as empty: genai's 119 `genai-ui` variables show as "lost" and the patch drops 13 real legacy values

**Status: open. Analysed 2026-10-14 (read-only; root cause confirmed, a fix tested in a scratch build, awaiting the user's decision on the open points). No repo, cluster, Vault or tenant-file change made for this finding.**
Found in the eosdev legacy->GitOps app migration run of 2026-10-13 (finding H1, high, in the run's findings; see `TASK-00-eosdev-audit-index.md`,
the index row is the orchestrator's to add). It blocks the `genai` migration.
Tools to change: `flux-stratio` (this repo, branch `feat/application-migration`; the primary fix) and `keos-apps`
(`components/genai/app/base/helm.yaml`, a separate secondary hardening). Related: `TASK-04` (live-only variables the chart does not render: this bug
makes every variable of one workload look like that), `TASK-02` (probe variables, a different cause of "lost" variables).

## Requirement for this task

Read "Migration principle" in `TASK-00`. The comparison must report what the migration really loses, nothing more and nothing less: legacy values
that the chart delivers must be compared, and the ones that differ must be carried into the patch (the legacy configuration is kept, even over
the GitOps overlay's values). A comparison that silently reads a ConfigMap as empty breaks that in the dangerous direction: it first floods the
review with false "lost" values, then (once the user accepts them) it writes a patch that lacks the real differences.

## What happened

`apps migrate genai --dry-run` (the `genai` entry in the tenant file is `enabled: false`; the numbers below come from a scratch copy of the
tenant file with that one line removed, see "How to reproduce") printed:

```
[REVIEW] 120 variable(s) in the live cluster aren't rendered by the chart
    Their values are lost after migration.
  genai-api/NOT_USED   (empty)   not rendered by the chart
  genai-ui/APP_ID      genai-ui.stratio-genai   not rendered by the chart
  ... (119 genai-ui rows)
```

All 119 `genai-ui` variables are delivered by the chart: the `ui` container takes its whole env from `envFrom: configMapRef: genai-ui-config`, and every
one of the 119 keys has a line in `genai/config/genai_ui_env_vars.yaml` (charts worktree `charts-align-15.1.0`). Only `genai-api/NOT_USED` (an empty
variable no template renders) is really obsolete. The 119 are a false positive, and the generated patch had no `genaiUi.*` value at all
(only the two containers' resources, `genaiApi` networking and `ingressProxyTimeout`).

## Root cause (confirmed in the current code)

1. `keos-apps` `components/genai/app/base/helm.yaml:71,73,75` sets `genaiLitellmModel: ${LLM_MODEL_CHAT}`, `chainGovernanceLitellmModel: ${LLM_MODEL_GOVERNANCE}`
   and `translateLitellmModel: ${LLM_MODEL_TRANSLATE}`. `keos-use-cases` `apps/components/resourceset-apps-genai.yaml:318-320` defines them as
   `"<< get $llmModels "chat" | default "" >>"`, and the eosdev tenant file has `llmModels: {chat: "", governance: "", translate: ""}` (tenant file lines
   ~273-278). So the three are empty.
2. `flux build kustomization --dry-run` (and so the plugin's render) turns an **empty** substitution into a YAML **null**, quoted or not. Tested with
   flux v2.9.2 (`$S/envsubst/`, X = ""): `a: "${X}"`, `a: ${X}`, `a: ${X:-}`, `a: ${X:=}` and `a: !!str ${X}` all render `null`; only
   `a: ${X:-""}` renders `""`. (Not tested on the cluster, where kustomize-controller applies the same substitution code.)
3. The HelmRelease values then carry `genaiLitellmModel: null`. The chart's `config/genai_ui_env_vars.yaml:42,57,104` renders
   `KEY: {{ .Values... | quote }}`; sprig's `quote` drops a nil, so the line becomes `KEY:` and the ConfigMap `genai-ui-config` has
   `GENAI_GOVERNANCE_CHAT_LITELLM_MODEL`, `GENAI_LITELLM_MODEL` and `TRANSLATE_LITELLM_MODEL` set to null.
4. The plugin reads ConfigMap data with `unstructured.NestedStringMap`, which returns an error at the **first** non-string value and an **empty map**.
   Both call sites discard the error:
   - `internal/diff/envvars.go:45` (`indexRendered`): `data, _, _ := unstructured.NestedStringMap(d.Object, "data")` -> `r.configMaps["genai-ui-config"]` is
     empty, so `workloadEnv` resolves none of the 119 keys and every live variable is "live only".
   - `internal/diff/chartvalues.go:133` (`AttributeConfigMaps`): same call, `len(data) == 0` -> `continue`, so the ConfigMap is never attributed to its chart
     file and the values fall back to the chart-wide lookup (`pathsFor`), where a shared name such as `VIRTUAL_HOST` looks ambiguous.
5. What the API server does with the same object: it decodes `data: {K: null}` into `map[string]string` as `K: ""` (checked with the strict
   `k8s.io/apimachinery` JSON serializer into `corev1.ConfigMap`: no error, `data` = `{"A":"a","B":"","C":""}`). A `bool` or a number in `data` is
   **rejected** (`json: cannot unmarshal bool into Go struct field ConfigMap.data of type string`). So a null is a legal, stored-as-empty value, and
   the plugin must read it as `""`; a non-null non-string is a chart that would not install.
   (`kubectl create --dry-run=client` keeps the null as is; it never decodes into the typed struct. The live `genai-ui-config` does not exist, because
   GitOps has not rendered genai yet and the legacy Deployment gets its env from the legacy objects, so there is no live object to compare.)

Which keos-apps checkout the plugin renders from: `~/.fluxcd/flux-stratio/environment.yaml` has
`keos-apps: /stratio/aws/flux/.worktrees/keos-apps-env-list-overlays` (branch `nojira/env-list-overlays`, `fad47b6`, the one the cluster follows).
`/stratio/gitops/keos-apps` (branch `main`, `61db0a0`) has the identical lines (71-75), so the bug is the same in both; `--repo keos-apps=<path>`
overrides it.

## Evidence

Scratch outputs in `/tmp/claude-1001/-stratio-gitops-flux-stratio/8951e99e-48a0-404d-9a81-f913f1865961/scratchpad/h1/` (`$S` below): `dryrun-installed.txt`,
`dryrun-fixed.txt`, `dryrun-fixed-models.txt`, `diff-fixed.txt`, `diff-fixed-models.txt`, `dryrun-installed-hardened.txt`, `fix.diff`, `risky-unquoted.txt`,
`can-be-empty.txt`. Earlier work: `.../scratchpad/genai/` (`genai-ui-vars.md` has all 119 variables, no secret-looking names).
No secret values are involved (genai-ui has no `*PASS*`/`*SECRET*`/`*TOKEN*`/`*KEY*` variable).

| Run | not rendered ("lost") | excluded by catalog | patched `genaiUi.*` values |
|---|---|---|---|
| installed plugin, real tenant file (llmModels all `""`) | **120** (119 genai-ui + `NOT_USED`) | 1 (`genai-api/VAULT_ROLE`) | 0 |
| scratch fix, real tenant file | **1** (`genai-api/NOT_USED`) | 6 (the 5 genai-ui below + `genai-api/VAULT_ROLE`) | 15 |
| scratch fix, tenant file with `llmModels.chat/governance: gpt-5.4-mini` (translate empty) | 1 | 6 | **13** |
| installed plugin + keos-apps hardened with `${LLM_MODEL_*:-""}` (secondary change alone) | **1** | 1 | n/a (no plugin change) |

Breakdown of the 119 genai-ui variables with the models set to the legacy `gpt-5.4-mini` (the earlier scratch run, confirmed here by
`diff-fixed-models.txt`: 18 `-genai-ui/` lines): **101 same + 18 different**; the 18 are 13 patched + 5 excluded by the catalog
(`DISCOVERY_DATABASE`, `GOVERNANCEAPI_FULL_NAME`, `GOVERNANCEAPI_URI`, `GOVERNANCE_URL`, `VAULT_ROLE`). With the real tenant file (`llmModels` empty)
`GENAI_LITELLM_MODEL` and `GENAI_GOVERNANCE_CHAT_LITELLM_MODEL` also differ from live (`gpt-5.4-mini`), so it is **99 same + 20 different = 15 patched
(`genaiLitellmModel`, `chainGovernanceLitellmModel` among them) + 5 excluded**. The scratch warning lists the three null keys on the real tenant file
and only `TRANSLATE_LITELLM_MODEL` with the models set.

The 13 values the patch used to drop (paths in the generated patch): `genaiUi.environment.gosec.myProfileUrl`,
`genaiUi.general.networking.{haProxyHost,haProxyPath,serviceUrl}`, `genaiUi.settings.chainChat.{opensearchEnabled,opensearchHost,opensearchPort}`,
`genaiUi.settings.externalDashboards.{discoveryEnabled,discoveryInternalUrl,discoveryUrl}`,
`genaiUi.settings.generalProperties.{governanceOpenSearchEnabled,governanceOpenSearchHost,governanceOpenSearchUrl}`. Without them a migrated genai-ui would
point at `admin.k8s...` instead of `genai.stratio...`, turn discovery and opensearch off, and use `/genai-ui.stratio-genai` as its path.

The other four held components (rocket, intelligence, governance-datamarket-agent, dlc-entity) were also dry-run with the installed and the fixed
binary (`dry-<app>-{installed,fixed}.txt`, tenant file with every `enabled: false` removed): identical output, and no null ConfigMap value in any of them
(the scratch warning never fired). So this bug is **only** genai's; rocket's 13 "not rendered" variables (probe thresholds and periods, `CUSTOMER_SIZE`,
`MIGRATION_STORAGE_PROFILE`, `SSCC_S3A_GLOBAL_CREDENTIALS`, `NOT_USED`) and intelligence's `NOT_USED` are another cause (see `TASK-02`/`TASK-04`).

## Affected call sites (the whole repo)

Searched `NestedStringMap`, `NestedString`, `NestedMap` and the apimachinery accessors that wrap them (`GetLabels`, `GetAnnotations`), non-test code.
`NestedStringMap` is the one that fails on null/non-string values (`NestedMap` deep-copies and accepts null; `NestedString` returns `""` for a null, which is
benign for the fields it reads). Nothing else reads ConfigMap/Secret `data` from an unstructured object: live ConfigMaps/Secrets are read typed
(`internal/envvars/clientgetter.go:19-21`, `internal/render/substitute.go:81-82`, corev1 decode), tenantimport, components, backup, drift and patch
generation never read `data`.

| # | Site | Reads | On a null / non-string value | Risk |
|---|---|---|---|---|
| 1 | `internal/diff/envvars.go:45` (`indexRendered`) | rendered ConfigMap `data` | error discarded, **whole ConfigMap empty**: every envFrom variable is "live only" | **high** (this bug) |
| 2 | `internal/diff/chartvalues.go:133` (`AttributeConfigMaps`) | rendered ConfigMap `data` | error discarded, empty -> ConfigMap unattributed -> ambiguous/unmapped paths (`VIRTUAL_HOST`) | **high** (this bug) |
| 3 | `internal/diff/settle.go:79` (`SettledByPatch`) and `:121` (`VerifyMapped`), through `indexRendered` on the **patched** render | same as 1 | an emptied ConfigMap makes `VerifyMapped` see no rendered value, so it takes mapped paths **out of the patch** ("not reproduced"); `SettledByPatch` stops settling | high (follow-on of 1; hidden today because no genai value is mapped) |
| 4 | `internal/diff/workloadspec.go:331` (`CheckEnvAgainstLimits`), through `indexRendered` | same as 1 | the `-Xmx` vs memory-limit check silently sees no env | medium (follow-on of 1) |
| 5 | `internal/diff/workloadspec.go:85` (`compareSelector`, rendered side; `:86` is the live side) | `spec.selector.matchLabels` | `rOK` false -> **selector-immutable risk silently not reported** | low (a null selector label is not realistic; the failure mode is silent) |
| 6 | `internal/diff/inline.go:58`, `:109`; `internal/diff/resourcepatch.go:122` | rendered pod-template `labels` | `labels` nil -> falls back to "no controller matched" | low |
| 7 | `internal/render/substitute.go:30` | `postBuild.substitute` | **returns the error** (loud) | none (keep as is: Flux's CRD types it `map[string]string`) |
| 8 | `internal/prepare/step_datamarket_agent.go:87` | live Deployment `matchLabels` | returns the error | none |
| 9 | `GetLabels`/`GetAnnotations` (`components/classify.go:233,253,263,272,306,416`, `components/resolve.go:261,264`, `appdiff/appdiff.go:256`, `backup/backup.go:386`, `prepare/prepare.go:224`) | metadata of live objects | apimachinery swallows the error, returns nil | none (the API server never stores a null label) |

Items 1 and 2 are the bug; 3-4 are what makes it worse and are fixed by the same change; 5-6 are cheap to move onto the same helper (rendered side only);
7-9 stay as they are. `NestedString` sites (`tenantimport/crdscan.go:60`, `helmreleasescan.go:38`, `components/classify.go:337`, `diff/helm.go:23`,
`doctor/doctor.go:628,634`, `backup/backup.go:651`, `render/render.go:204,209`, `render/kustomization.go:18`) read fixed string fields; a null there is the
same as absent. No change.

## Proposed change (flux-stratio, NOT applied; tested in the scratch copy `$S/plugin`, full diff in `$S/fix.diff`)

Decision: **read the rendered data the way the API server does, and say so.** Null -> `""`; a bool or number -> its text (consistent with
`renderedEnvValue` in `internal/diff/envvars.go`, which already stringifies `value: 8080`; the API server would reject it, but the comparison should show
what the chart says); a map or list as a value -> error. Do not keep an error that silently empties the ConfigMap. A null is not an error (it is stored
as empty) but it is a **warning**, because it nearly always means an unquoted/empty `${VAR}` and the user should see it. A value the API server rejects
is a hard error in `appdiff.chartDiff` (the chart would not install, so no comparison against it is meaningful).

New file `internal/diff/stringmap.go`:

```go
// stringMapAt reads the string map at fields of obj the way the API server
// decodes it into a map[string]string: a null value is "" and a bool or a
// number is its text. unstructured.NestedStringMap errors on the first
// non-string value and returns an empty map, so one null emptied a whole
// ConfigMap. A map or a list as a value is an error.
func stringMapAt(obj map[string]any, fields ...string) (map[string]string, bool, error) {
	raw, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil || !found || raw == nil {
		return nil, false, err
	}
	in, ok := raw.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%v is a %T, expected a map", fields, raw)
	}
	m := make(map[string]string, len(in))
	for k, v := range in {
		switch s := v.(type) {
		case nil:
			m[k] = ""
		case string:
			m[k] = s
		case bool, int, int32, int64, float32, float64:
			m[k] = fmt.Sprint(s)
		default:
			return nil, false, fmt.Errorf("%v: key %q holds a %T, expected a scalar", fields, k, v)
		}
	}
	return m, true, nil
}

// nullKeys lists, sorted, the keys of the map at fields of obj whose value is null.
func nullKeys(obj map[string]any, fields ...string) []string { /* see $S/fix.diff */ }

// NullConfigMapKeys lists, by ConfigMap name, the data keys docs render as null.
func NullConfigMapKeys(docs []*unstructured.Unstructured) map[string][]string { /* see $S/fix.diff */ }
```

Call sites (the whole change at the two high-risk ones is one line each):

```diff
--- internal/diff/envvars.go
-			data, _, _ := unstructured.NestedStringMap(d.Object, "data")
+			data, _, _ := stringMapAt(d.Object, "data")
--- internal/diff/chartvalues.go
-		data, _, _ := unstructured.NestedStringMap(d.Object, "data")
+		data, _, _ := stringMapAt(d.Object, "data")
```

and, the same one-line swap on the rendered side only, in `internal/diff/inline.go:58`, `inline.go:109`, `internal/diff/resourcepatch.go:122` and
`internal/diff/workloadspec.go:85` (not `:86`, the live object).

The warning, in `internal/appdiff/chart.go` right after `renderChart` in `chartDiff` (the scratch build prints exactly this on the real tenant file):

```go
	warnNullConfigMapKeys(opts, renderedDocs)
...
func warnNullConfigMapKeys(opts Options, docs []*unstructured.Unstructured) {
	nulls := diff.NullConfigMapKeys(docs)
	names := make([]string, 0, len(nulls))
	for name := range nulls {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		opts.Log.Warningf("rendered ConfigMap %s has null value(s) for %s; read as empty. Check the HelmRelease values behind them for an unquoted ${VAR} that came out empty", name, strings.Join(nulls[name], ", "))
	}
}
```

The hard error (not in the scratch build, add it next to the warning): a sibling of `NullConfigMapKeys`, for example `diff.RenderedConfigMapErrors(docs) []error`, that runs
`stringMapAt` on every rendered ConfigMap and returns its errors; `chartDiff` returns the first one wrapped as
`rendered ConfigMap %s: %w (the API server rejects it, the chart would not install)`. Keep the swallow in `indexRendered`/`AttributeConfigMaps` themselves (they
have no error return and the precheck has already run).

Scratch result: `go build ./...`, `go vet ./internal/diff` and `go test ./...` pass; the dry run prints the numbers in the Evidence table.

## keos-apps hardening (secondary, separate change, NOT applied)

File `components/genai/app/base/helm.yaml` (both checkouts), lines 71, 73, 75.

**Quoting does not help.** The obvious fix, `"${LLM_MODEL_CHAT}"`, still renders `null` (flux build behaviour above; tested end to end in a scratch copy:
the installed plugin still printed 120 "lost"). What works is a default of an explicit empty string:

```diff
-          genaiLitellmModel: ${LLM_MODEL_CHAT}
+          genaiLitellmModel: ${LLM_MODEL_CHAT:-""}
...
-          chainGovernanceLitellmModel: ${LLM_MODEL_GOVERNANCE}
+          chainGovernanceLitellmModel: ${LLM_MODEL_GOVERNANCE:-""}
...
-          translateLitellmModel: ${LLM_MODEL_TRANSLATE}
+          translateLitellmModel: ${LLM_MODEL_TRANSLATE:-""}
```

Tested with the installed (unfixed) plugin against a scratch copy of keos-apps with these three lines (`dryrun-installed-hardened.txt`): `[REVIEW] 1 variable(s)`
(only `NOT_USED`), so on its own this change also removes the false "lost" list. It is **not** a substitute for the plugin fix: any other chart env file with
`| quote` and an empty substitution produces the same null, and the plugin must not depend on every keos-apps line being written this way. It changes
what the chart's `genai-ui-config` really contains on a GitOps install too: `TRANSLATE_LITELLM_MODEL: ""` instead of a null that Helm stores as `""` anyway
(same stored value, so no runtime behaviour change expected; not verified against a running genai-ui).

Other values of the same kind in keos-apps component `helm*.yaml` (`$S/scan.py` extracted every whole-value `${VAR}` / `"${VAR}"`, quoted or not, and
matched it with its definition in `keos-use-cases/apps/components/resourceset-*.yaml`; 75 candidates, `risky-unquoted.txt`). The ones whose definition can really
produce an empty string (`| default ""`), and so would null the same way (`can-be-empty.txt`, 15 lines):

| Variable | Where (keos-apps `components/`) | Why it can be empty |
|---|---|---|
| `LLM_MODEL_CHAT`, `LLM_MODEL_GOVERNANCE`, `LLM_MODEL_TRANSLATE` | `genai/app/base/helm.yaml:71,73,75` | `llmModels.*` is optional |
| `DISCOVERY_NAME`, `OSCLUSTER_NAME` | `genai/app/base/helm.yaml:48,52`; `governance-datamarket-agent/app/base/helm.yaml:47`; `intelligence/app/base/helm.yaml:65`; `search-engine/app/base/helm.yaml:47`; `gosec-agent/app/overlays/opensearch/base/helm-patch.yaml:11` | optional dependency (`get $dependency "name" | default ""`) |
| `GENAI_NAME` | `intelligence/app/base/helm.yaml:57`; `rocket/app/base/helm.yaml:51` | optional dependency |
| `HDFS_NAME` | `dg-agent`, `rocket`, `spark-history`, `virtualizer` `app/overlays/hdfs/base/helm-patch.yaml` | optional dependency (the overlay is only included with hdfs) |

The rest of the 75 are `get $dependency "name"` / `get $component "name"` with no default (a required dependency: an empty value there is a mis-configured tenant file,
not a case to harden) and `STRATIO_VIRTUALIZER_UI_ENABLED` (`virtualizer/app/base/helm.yaml:47,49`, a boolean with a fallback in its definition). Whether the
dependency-name nulls matter depends on how each chart uses `.name` (a `quote` into a ConfigMap, or string concatenation): **not analysed per chart**. Recommend
`:-""` for the three `LLM_MODEL_*` now, and for the optional dependency names only where a chart renders them into a ConfigMap (needs a per-chart check, or wait for
the warning this task adds to tell which ones actually render null on a tenant).

The chart side could also be hardened (`{{ ... | default "" | quote }}` in `genai/config/genai_ui_env_vars.yaml:42,57,104`), but the same pattern is in every chart's env file, so it
belongs to a chart-wide decision, not here.

## Tests (flux-stratio; written and run in the scratch copy: they fail on the current code and pass with the change)

Style: table tests in `internal/diff/*_test.go`, fixtures built with the existing `configMapDoc`/`deploymentDoc` helpers (`envvars_test.go`) and
`map[string]any` data (`chartvalues_test.go`).

1. `TestIndexRendered_NullConfigMapValue` (`internal/diff/envvars_test.go`): a ConfigMap `ui-config` with `{"A": "a", "TRANSLATE_LITELLM_MODEL": nil, "EMPTY": ""}` and
   a Deployment with `envFrom: configMapRef ui-config`; `workloadEnvOf` must return all three keys with their `ConfigMap`/`Key`, the null one as `Value: ""`.
   Fails today with `workloadEnv = map[]`.
2. `TestAttributeConfigMaps` (`internal/diff/chartvalues_test.go`), a new case `"a null value does not hide the ConfigMap"`: files `sibling`, data
   `{"VAULT_ROLE": "r", "LOG_LEVEL": nil, "API_PORT": "8080"}`, want `config/api_env_vars.yaml`. Fails today with `attributed to ""`. This is the `VIRTUAL_HOST` attribution case.
3. `TestStringMapAt` (`envvars_test.go`): strings; null -> `""`; `int64`/`float64`/`bool` -> their text; a nested map -> error; absent field and a null `data`
   -> `found=false`, no error.
4. `TestNullKeys` (`envvars_test.go`): returns the null keys sorted.
5. To add when the hard error and the warning land (not in the scratch copy): an `internal/appdiff` test that a rendered ConfigMap with a map value makes `chartDiff`
   fail with the "API server rejects it" message, and that a null produces exactly one `Warningf` naming the ConfigMap and its null keys.
6. Regression guard for the follow-ons: a `VerifyMapped` case (`internal/diff`) whose patched render has a ConfigMap with a null key next to a mapped key must keep the mapped path.

## Acceptance criteria

- `flux stratio apps migrate genai --dry-run -v` (with the genai entry enabled) shows **no** false "lost": the review lists only `genai-api/NOT_USED`
  (`1 variable(s) ... aren't rendered`), for the real tenant file and for the one with the models set; the null-key warning names
  `GENAI_GOVERNANCE_CHAT_LITELLM_MODEL, GENAI_LITELLM_MODEL, TRANSLATE_LITELLM_MODEL` (or only `TRANSLATE_LITELLM_MODEL`).
- Counts for the genai-ui workload: **101 same / 18 different / 13 patched / 5 excluded** when `llmModels.chat/governance` are `gpt-5.4-mini` (the case in the findings
  run), and **99 / 20 / 15 / 5** with the tenant file as it stands today (`llmModels` all empty). The 5 excluded are `DISCOVERY_DATABASE`,
  `GOVERNANCEAPI_FULL_NAME`, `GOVERNANCEAPI_URI`, `GOVERNANCE_URL` and `VAULT_ROLE`; `genai-api/VAULT_ROLE` is excluded as before. The 13 patched paths are the ones listed in
  Evidence. (The dry run prints the excluded table and the patch; the same/different totals come from `apps diff genai -v`: count the `-genai-ui/` lines = different.)
- The patch carries the 13 (15) `genaiUi.*` values; with it applied, the chart rendered again reproduces the live value for each (`VerifyMapped` keeps them).
- `apps diff genai` still shows the immutable-selector `[ACTION]` for `genai-api` and `genai-ui`, unchanged.
- `go test ./...`, `go vet ./...`, `gofmt -l .` clean. `CHANGELOG.md` entry; `CONTEXT.md` section on chart-mode diffing states how rendered ConfigMap values are read
  (null as empty, with a warning).
- The `keos-apps` change, if made: `flux-operator build rset` + `flux build kustomization` of `apps-genai` renders `genaiLitellmModel: ""` (not null) with the models empty and the
  model name with it set.
- After the fix, the held components can be re-evaluated: genai becomes migratable as far as this finding goes. The other four held entries (`rocket`,
  `intelligence`, `governance-datamarket-agent`, `dlc-entity`) are unaffected by this bug (see Evidence); keep them `enabled: false` only for their own reasons.

## How to reproduce

```
S=/tmp/claude-1001/-stratio-gitops-flux-stratio/8951e99e-48a0-404d-9a81-f913f1865961/scratchpad/h1
# 1. tenant file copy with genai enabled (the real file is untouched; genai is enabled:false there)
mkdir -p $S/fleet && (cd /stratio/gitops/keos-fleet && git ls-files -z | xargs -0 -I{} cp --parents {} $S/fleet/)
sed -i '260d' $S/fleet/clusters/eosdev/tenants/config/stratio.yaml     # the `enabled: false` line of the genai entry (check with sed -n 258,262p first)
# 2. the bug: 120 "lost"
flux stratio apps migrate genai --dry-run -v --repo keos-fleet=$S/fleet | grep -E 'REVIEW|^ +2 to act'
flux stratio apps migrate genai --dry-run -v --repo keos-fleet=$S/fleet | grep -c 'not rendered by the chart'      # 120
# 3. why: empty substitution -> null, quoted or not
mkdir -p $S/envsubst/app    # app/kustomization.yaml (resources: [cm.yaml]); cm.yaml: data: {q: "${X}", d: ${X:-""}}; ks.yaml: postBuild.substitute X: ""
flux build kustomization t -n default --path $S/envsubst/app --kustomization-file $S/envsubst/ks.yaml --dry-run    # q: null, d: ""
# 4. the fix (scratch build of the repo with $S/fix.diff applied) -> 1 "lost", 15 patched genaiUi values
cd $S/plugin && GOWORK=off go build -o $S/flux-stratio-fixed ./cmd/flux-stratio
$S/flux-stratio-fixed apps migrate genai --dry-run -v --repo keos-fleet=$S/fleet | sed -n 3,5p
# 5. the hardening alone, with the installed plugin: scratch keos-apps with ${LLM_MODEL_*:-""} on lines 71,73,75
flux stratio apps migrate genai --dry-run -v --repo keos-fleet=$S/fleet --repo keos-apps=$S/keos-apps | grep -c 'not rendered by the chart'   # 1
```

## Open questions for the user

- Stringify a bool/number in rendered `data` (recommended, matches `renderedEnvValue`) or fail on it right away? (The API server rejects both, so the chart would not install either way.)
- Make a null in a rendered ConfigMap a warning only (recommended), or also stop `--yes` (like the other review items)?
- Apply the keos-apps `:-""` default now for the three `LLM_MODEL_*`, and ask for a per-chart review of the optional dependency names, or leave keos-apps alone and rely on the plugin fix?
- Should the eosdev tenant file get `llmModels` values (the live genai-ui uses `gpt-5.4-mini` for chat and governance)? Without them the patch carries them as legacy
  values (15 patched instead of 13); with them the GitOps value matches. Not a plugin change.

## Related

- `TASK-00` index (the orchestrator adds the row); `TASK-04` (live variables the chart does not render, the mirror of this); `TASK-02` (probe variables, another source of
  "not rendered" noise); `TASK-05` ("no differences" is one-directional); `docs/TASK-keos-apps-env-list-overlays.md` (overlays and env lists).
- Scratch fix and outputs: `/tmp/claude-1001/-stratio-gitops-flux-stratio/8951e99e-48a0-404d-9a81-f913f1865961/scratchpad/h1/` (`plugin/` = full copy of the repo with the change, `fix.diff`
  = the diff against the repo, `flux-stratio-fixed` = the built binary). Earlier scratch fix (debug-laden, superseded): `.../scratchpad/genai/plugin/`.
