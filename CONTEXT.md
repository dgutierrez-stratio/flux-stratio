# flux-stratio — Context for AI-assisted development

This file exists to let another LLM (or a human seeing this repo for the first time) get
productive fast — what this plugin is, why it's shaped the way it is, and where to look for what.
It complements, not replaces, [`README.md`](README.md) (user-facing install/usage) and
[`docs/`](docs/) (config reference, migration runbook).

## 1. What this is

`flux-stratio` is a [Flux CLI plugin](https://github.com/fluxcd/flux2/blob/main/rfcs/0013-cli-plugin-system/README.md)
(`flux stratio ...`) that migrates **Stratio applications** from an Ansible-based deployment onto a
Flux/GitOps tenant. It's a Go port of the **application-migration half** of a legacy Python client
at `/stratio/charts/charts/bin/migration/` (`migrate.py` and friends) — see §9 for the full
list of correctness bugs the port fixed along the way.

The workflow it supports, end to end:

1. `tenant import` — scan a live, not-yet-migrated cluster, render a tenant
   `ResourceSetInputProvider` skeleton.
2. `apps backup` — capture every app's live legacy state to disk, for the record and as a
   comparison point.
3. `apps diff` — compare an app's rendered GitOps desired state against its live legacy state (or
   check post-migration drift — see §6).
4. `apps migrate` — do the diff again, then splice the resulting patch into the tenant file.

Every command re-derives its own state from the live cluster / rendered templates on every run —
there is no `status.json`-equivalent persisted state anywhere. Running any command twice is safe;
re-running `apps migrate` on an already-migrated app finds nothing to change instead of trusting a
prior result.

## 2. Scope boundary — what this plugin does *not* do

This is the single most important thing to internalize before touching this codebase, because it's
easy to accidentally scope-creep into one of these:

| Out of scope | Lives in | Why |
|---|---|---|
| Cluster-wide system-services migration (namespace/CRD bootstrap, legacy Flux/Capsule/Kyverno/VPA cleanup) | [`flux-keos`](https://github.com/Stratio/flux-keos) (sibling repo) | A different layer entirely — flux-stratio assumes flux-keos's `cluster migrate` already ran |
| Chart/CCT descriptor tooling (`charts analyze/values/environment/import/commons/validate`) | the legacy Python client | Explicitly never ported; still actively used for that purpose |
| The Python client's TUI (`status.json`, `tui.py`) | — | Dropped entirely; no persisted-state model at all in this plugin |

If a change request sounds like "add a command to bootstrap a namespace" or "add CCT descriptor
validation," that's almost certainly the wrong repo.

## 3. Command reference

```
flux stratio version
flux stratio doctor                            # preflight: binaries (+ optional meld), catalog, environment, repo layout,
                                                 # chart paths, catalog types vs. templates, cluster, tenant file

flux stratio config init --base --cluster --tenant [--charts] [--dir] [--force]
                                                 # write catalog.yaml (19 seeded component types) + environment.yaml

flux stratio tenant import [--size] [--output] [--force]
                                                 # scan the live cluster → tenant RSIP skeleton

flux stratio apps backup <name> | --catalog | --all [--dir] [--as]
                                                 # capture live state; --catalog = classified instances, --all = everything discovered

flux stratio apps diff <name> [--baseline <path>] [--drift <path>] [--view unified|patch|meld] [--as]
                                                 # --baseline: pre-migration (desired vs. a backup instead of live)
                                                 # --drift: post-migration (live now vs. a backup, no GitOps render)

flux stratio apps migrate <name> | --all [--dry-run] [-y/--yes] [--continue-on-error] [--as] [--baseline <path>] [--dir]
                                                 # diff (running any declared prepare step first), then splice the patch
```

`<name>` is a live object's name (`psql-agent`) or an instance's derived GitOps object name
(`psql-gosec-agent`); the component type is detected from the live object (§6, `internal/components`).
`--as <type>[/<entry>]` answers the type/tenant-entry question up front.

Persistent flags on every command: `--config` (catalog), `--env-config`, `--base`, `--cluster`, `--tenant`, `--kubeconfig`,
`--kube-context`, `-v/--verbose`. Run `flux stratio apps diff --help` for the fullest single
explanation of the baseline-vs-drift distinction in the codebase — it's worth reading before
touching `internal/appdiff` or `internal/drift`. Likewise `flux stratio apps migrate --help` is the
canonical user-facing explanation of how a patch is computed (base rendered without the existing
same-kind patch, whole-patch replacement, up-to-date detection) and of recovering a component Flux
already reconciled unpatched (`--baseline latest`) — keep it, README's "How `apps migrate` computes
a patch" section and `docs/migration-runbook.md` step 6 in sync when changing that behavior.

## 4. Requirements

Go 1.26.0, module `github.com/Stratio/flux-stratio`. `GOWORK=off` must prefix every `go` invocation
(the Makefile already does this) — this repo sits inside a larger Go workspace on disk.

External binaries flux-stratio shells out to (never `kubectl` — see §6):

| Binary | Used by | Required? |
|---|---|---|
| `flux-operator` (≥0.45.1) | `internal/render` — `build rset` | yes, checked by `doctor`/`Preflight` |
| `flux` (≥2.9.0) | `internal/render` — `build kustomization --dry-run` | yes |
| `helm` | `internal/diff.HelmTemplate` — chart-mode rendering | yes |
| `kubectl` | version-check only; cluster access is via its own Go client | yes |
| `meld` | `internal/ui.Meld` — `apps diff --view meld` | **optional** — `doctor` reports it as a non-blocking, informational check |

## 5. Repository layout & conventions

Mirrors `flux-keos` deliberately (same author's other Flux plugin) — when in doubt about a
convention, that repo is the reference:

- `internal/`-only, no `pkg/`. `cmd/flux-stratio/main.go` is ~20 lines: builds
  `internal/cli.NewRootCommand()`, prints `Error: <err>` to stderr, `os.Exit(1)`.
- Root cobra `Use: "stratio"` (the bare subcommand name, not the binary name) — RFC 0013 requires
  this; it's what makes `flux stratio ...` dispatch work once installed at
  `~/.fluxcd/plugins/flux-stratio`.
- **`internal/cli` holds zero business logic** and is deliberately untested — every `RunE` resolves
  flags into an `Options` struct and calls exactly one function in an `internal/<pkg>`. If you find
  yourself adding a loop, a decision, or a data transformation directly in `internal/cli`, it
  belongs in a feature package instead.
- Every feature package: one `Options` struct (with a `Log *log.Logger` field), one entrypoint
  function, its own `_test.go` files in the same package (not `_test` suffix — in-package tests
  throughout, per `TestFunc_Scenario` naming).
- `gopkg.in/yaml.v3` for everything written to or read from disk (including comment-preserving
  `yaml.Node` editing in `internal/tenantfile`). `sigs.k8s.io/yaml` only for manifest→unstructured
  decoding where apimachinery's numeric conventions matter (see §7).
- Flux and Stratio CRDs are **never** imported as typed API packages — always
  `unstructured.Unstructured`, resolved by a literal `schema.GroupVersionKind`.
- stdlib `testing` only — no testify, no gomega. `client/fake` (controller-runtime) for cluster
  mocking, `runner.Fake` for subprocess mocking, `t.TempDir()` for filesystem state. 22 packages,
  ~414 tests (counting subtests), all currently green.
- Verification loop for every change: `make fmt-check vet lint test build`. `golangci-lint` isn't
  installed by `make`; if `make lint` reports it missing, it needs manual install and may not be on
  `$PATH` by default — try `export PATH="$PATH:$(go env GOPATH)/bin"` first.

## 6. Package reference

### Cluster & process access (no business logic of their own)

- **`internal/log`** — `Logger`, copied verbatim from flux-keos. Symbol vocabulary on stderr:
  `► ✚ ◎ ✔ ⚠️ ✗`. Nil-safe. `Debugf` only prints under `-v`.
- **`internal/ui`** — the only place result *data* gets written (always stdout, never stderr — this
  split is load-bearing: it's what makes piping a command's output safe). Three renderers, each
  reused by more than one caller: `Patch` (raw `patches:` YAML), `FileDiff` (hand-written unified
  diff — two identical inputs produce no output at all), `Meld` (writes two temp files, shells out
  to `meld`, blocks until the window closes).
- **`internal/kubeclient`** — `New` builds a controller-runtime client from kubeconfig flags, field
  owner `"flux-stratio"`. `GetUnstructured`/`ListUnstructured` are the two primitives everything
  else in this repo uses to read a Flux/Stratio CRD without importing its API package.
  `ListUnstructured(ctx, c, gvk, "")` — empty namespace — lists cluster-wide.
- **`internal/runner`** — `Runner` interface (`Run(ctx, name, args...) (stdout, stderr []byte,
  err error)` — **never merges stdout/stderr**, unlike the Python client's `stderr=STDOUT` bug that
  corrupted a captured patch with interleaved diagnostic output). `Exec` is the real `os/exec`
  implementation; `Fake` is the test double (canned per-binary-name response + call recording).
  `Preflight` checks the four required binaries' presence/version without touching a cluster.
- **`internal/envvars`** — pure port of the Python client's `EnvVarExtractor`: resolves a workload's
  effective env vars (`envFrom` ConfigMap/Secret expansion, then direct `env[]`, `fieldRef`/
  `secretKeyRef`/`configMapKeyRef`/`resourceFieldRef`), against a `Getter` interface so it's fully
  unit-testable with no live cluster.
- **`internal/yamldocs`** — `Decode` splits a multi-document YAML stream and decodes each into
  `*unstructured.Unstructured`. **Read the doc comment on `Decode` before touching it** — it
  unmarshals into the `*Unstructured` value itself, not a bare map, specifically so numeric fields
  decode via apimachinery's int64-for-whole-numbers convention instead of `encoding/json`'s
  float64-always default. Getting this wrong makes every unchanged integer field look like a
  spurious diff; this was a real bug caught during the original port (see §9).

### The catalog and tenant file (application-migration-specific)

- **`internal/config`** — two external, operator-owned files (never embedded in the binary at
  runtime) plus the resolved-instance type:
  - `Catalog`/`ComponentType` (`catalog.yaml`): the typed **component catalog** — per type, static
    facts only (`component` tenant key, `rset`, `chart`, `exclude`, `prepare`, `anchor`), the
    `match` selectors (`kinds` + label/annotation selectors — the seed selects on CCT's
    `cct.stratio.com/application_service`/`application_model` annotations) that recognize its live
    legacy objects, and `entry`/`object`/`kustomization` **name templates** (text/template over the
    live object). Nothing environment- or instance-specific: no object name, rename, namespace.
    `Resolve`/`Load`: `--config` → `$FLUX_STRATIO_CONFIG` → `~/.fluxcd/flux-stratio/catalog.yaml`
    (a sibling of `~/.fluxcd/plugins/`, never inside it) → `./flux-stratio.yaml`, strict
    `KnownFields(true)` decoding; a pre-catalog `config.yaml` (top-level `apps:`) is recognized and
    rejected with a pointer to `config init`.
  - `Environment` (`environment.yaml`): `base`/`chartsBase`/`cluster`/`tenant`, same ladder via
    `--env-config`/`$FLUX_STRATIO_ENV`; `LoadEnvironment` applies `--base/--cluster/--tenant` on top
    and needs no file at all if the flags supply everything.
  - `App`: one **resolved instance** (never read from or written to disk) — a type's facts plus the
    derived `Entry`/`Object`/`Kustomization` and `Live []ObjectRef` (the exact live objects it was
    classified from). `LiveName()`/`LiveNamespace()` replace the old `renamed`/`previousNamespace`
    config fields. It's what every diff/backup/migrate package operates on.
  - `SeedCatalog`/`SeedEnvironment` (`config init`) are the *one* place a hardcoded component list
    legitimately lives in this codebase.
- **`internal/components`** — live objects → `config.App`. `Classify` matches every discovered
  object against every type (skipping anything with `ownerReferences`), renders its entry, and
  groups by (type, namespace, entry); an object CCT annotated as another tenant's
  (`cct.stratio.com/application_tenant`) is skipped too. A chart type's `chart.siblings` (the
  chart's other workloads CCT deployed as separate apps, e.g. genai-ui) never anchor an instance:
  each joins the one instance of its type in its namespace, appended to `Live` after the anchor —
  which is how a legacy CCT backup (no HelmRelease to enumerate the chart's workloads by) captures
  them. `Resolve(opts, name)` picks the one instance `name` refers to
  (live/object name first, entry name as fallback, then an already-migrated instance: when a
  HelmRelease adopted the legacy workloads, Helm stripped the CCT annotations the selectors need,
  so `name` — the HelmRelease itself, or any workload its `helm.toolkit.fluxcd.io/{name,namespace}`
  labels say it renders (genai-ui finds the same instance genai-api does) — resolves to that
  HelmRelease's instance of every chart-mode type whose `path.Base(chart.path)` it deploys
  (`ManagedMatches`; types sharing a chart are asked about), labelled `keos.stratio.com/tenant` as
  the run's tenant: object = the HelmRelease's name, entry = the declared one whose object renders to
  it (inferred when exactly one does), live = every workload of the type it renders) and
  `ResolveAll` all of them (selectors only — `--all` never sees that fallback); with a tenant file
  (`Options.Doc`, set for diff/migrate, nil for backup/drift) each entry must be declared under
  `components.<component>`, else the `Prompter` asks which declared entry it is (`--as` answers up
  front; `NonInteractive` — used by `migrate --yes` — fails naming `--as`; siblings that joined no
  instance are warned about only for the type being resolved, including a migrated type's legacy
  leftovers its HelmRelease doesn't render; `ResolveAll` returns
  such instances as `unresolved` instead of aborting, so `--all` reports them all as failed apps).
  Nothing is persisted.
  Its fixtures (`testdata/live.yaml`) are metadata-only copies of real captured legacy objects,
  including same-named PgDatabases that must never classify as their chart app.
- **`internal/catalog`** — parses `keos-use-cases/apps/components/resourceset-apps-*.yaml`
  (Go-template `<< >>` syntax, regex-split rather than a real template evaluator, matching the
  Python client's own approach) into: component schemas, a chart→component-key map (with
  `Ambiguous` charts like `pgbackup` flagged explicitly, not silently collapsed), a CRD→component
  map, and — the highest-risk, most-tested part — a patch-anchor index: which tenant-file field a
  given Kustomization's patches actually live at. Almost every app's anchor is the default
  (`components.<key>[name=<object>].patches`); the one real exception in production is a
  postgres/opensearch gosec agent, whose patches live one level deeper at
  `config.agent.patches` on its *parent* component's entry, because the agent has no top-level
  component entry of its own.
- **`internal/tenantfile`** — reads/writes the tenant `ResourceSetInputProvider` via `yaml.Node`
  throughout, never a plain `map[string]any`, so comments and commented-out component blocks
  survive an edit byte-for-byte. `Splice` is the one write path `apps migrate` uses. `OwnerName`
  recovers a Kustomization's true owning tenant-file entry name, correcting for the gosec-agent
  anchor case above.
- **`internal/render`** — the only place `flux-operator build rset` / `flux build kustomization
  --dry-run` get shelled out to. `Render(ctx, Options{Base, Cluster, Tenant, Rset, Kustomization,
  Object, ...})` needs the tenant file to already declare the app's component (it's rendering the
  *desired* state) — **this is the render `internal/backup`/`internal/drift` deliberately do not
  use** (see §6's next section for why that distinction exists and matters).

### Diff, migrate, backup, drift — the four verbs

- **`internal/diff`** — the deep-diff engine, used by `internal/appdiff` (never called directly by
  `internal/backup`/`internal/drift`, which only need file-level text comparison, not patch
  generation). `ManifestDiff`/`NeedsJSON6902`/`ToJSON6902Ops` auto-detect JSON 6902 vs. strategic
  merge patch by whether a shared top-level `spec` key is a list on *both* sides — this is never a
  user choice. `ChartDiff` + `HelmTemplate` are chart-mode's equivalent, comparing a chart's
  declared env vars against a live workload's resolved ones — **per workload**: each live workload
  against its own rendered workload, and each differing value patched through the `.Values` path of
  the chart file its rendered ConfigMap was built from (`ScanChartFiles` + `AttributeConfigMaps`,
  matching a ConfigMap to the file with exactly its keys). Sibling workloads' same-named variables
  (genai's `VAULT_ROLE`) never cross over; anything it can't attribute to one path goes to
  `UnmappedDiffs` with a reason, never guessed. `CoerceValue` handles the
  octal-looking-string/float/`yes`-`null` typed-value edge cases the Python client got wrong.
- **`internal/appdiff`** — orchestrates `internal/render` + `internal/diff` for `apps diff`'s
  default/`--baseline` comparison and for `apps migrate`'s patch computation — **the one place both
  commands compute a diff, so they can never disagree about what a migration would do.** Dispatch
  is by `App.ChartPath` (chart-mode vs. manifest-mode), never a flag the operator sets. Exports
  `MergeLiveEnv`, `LiveWorkloadEnvs`, `WorkloadEnvFile` and `FetchLiveWorkloads` (chart-mode's "fetch every live Deployment/StatefulSet/
  DaemonSet a chart declares, translating `App.Object` to `App.LiveName()` and falling back to
  `App.LiveNamespace()`") for reuse by
  `internal/backup`'s own chart-mode capture — the one legitimate cross-package dependency from
  backup back into appdiff, and only for this pure "given rendered docs, fetch the matching live
  objects" helper, never appdiff's render-triggering entrypoints. `LiveManifestObject`/
  `LiveChartWorkloads` are also exported but **currently unused outside this package's own
  tests** — see §10, this is a known stale spot.
- **`internal/discovery`** — cluster-wide, name-indexed live-object scanner: lists every
  Kustomization, HelmRelease, Deployment/StatefulSet/DaemonSet, 14 hardcoded Stratio operator
  CRDs, plus any extra kinds passed in (the catalog's `match.kinds`, via `config.Catalog.Kinds`) (verified against a real cluster's installed CRDs, not copied from the Python client's own
  differently-cased dict) — **with no namespace filter and no tenant-file dependency at all**. This
  is what lets `apps backup`/`apps diff --drift` work on an app whose component isn't declared in
  the tenant file yet, unlike `internal/render`. `Index.Find{CR,Workload,HelmRelease,
  Kustomization}` are per-kind name lookups; `Index.Get(gvk, ns, name)` is the exact lookup for an
  already-classified object; `Index.Objects()` feeds `internal/components`; `Index.Names()` is the
  union across all four, used by `apps backup --all`.
- **`internal/backup`** — captures an app's live state to
  `<dir>/<App.ID>/<UTC-timestamp>/{cr.yaml | deployment.yaml+env-vars.env+env-vars.<kind>.<name>.env
  per workload | helmrelease.yaml+values.yaml}` (the per-workload env files are what `--baseline`
  and drift compare when present; the merged `env-vars.env` is the backup marker and the fallback
  for older backups), dispatching on `App.ChartPath` with a graceful fallback cascade
  (mirroring the Python client's own CR → Deployment → HelmRelease-only priority) when the live
  object isn't backed by the expected kind — logging a warning, not failing, since `apps backup
  --all`/`--catalog` must not abort on one app's shape surprise. Chart-mode capture sources `helm
  template`'s values from the **live** HelmRelease it just found via `internal/discovery`, never a
  flux-rendered *desired* one — capturing desired-state values would defeat backup's own purpose.
  A classified app (`App.Live` set) is captured from its exact live object (`Index.Get`) before any
  name-only cascade — the cascade alone once captured a same-named `PgDatabase` as the genai/rocket
  apps. `DiscoveredApps(catalogApps, idx)` builds the app list for `--all`: the classified catalog
  instances, plus a minimal synthetic `App{ID, Name, Object}` for every live name none of their
  `Live` refs covers. `ResolveBaseline(root, appID)` is the shared
  auto-locate logic both `apps diff --baseline` and `--drift` use.
- **`internal/drift`** — answers a genuinely different question than `internal/appdiff`: not "what
  would migrating this app change" (desired vs. live-ish), but "has this app's live state changed
  since I backed it up" (live *right now* vs. a stored backup, directly, no GitOps rendering at
  all). `Run` captures live state via `internal/backup.Run` itself (to a throwaway temp dir), then
  diffs file-for-file against the given backup — so both sides are always read through the exact
  same capture logic. A shape mismatch between the two captures (e.g. a CR now, a HelmRelease at
  backup time) is a clear error, not a silent wrong comparison.
- **`internal/appmigrate`** — `apps migrate`'s orchestration: `Plan` (read-only preview) and `Apply`
  (writes), sharing one internal `plan()` so they can never diverge. `OrderApps` topologically
  sorts `--all` by the dependency edges already declared in the tenant file itself (DFS post-order,
  cycle-safe), not catalog order.
- **`internal/prepare`** — the four ported one-time preconditions (`prepare-datamarket-agent`,
  `prepare-datarest`, `prepare-dlc`, `prepare-genai`). An automated step's `Plan` reads live state
  and returns `[]Operation`, each carrying the live object it acts on (empty = already satisfied).
  `ensurePrepared` (in `internal/cli/apps_migrate.go`, the only caller, and the first stage of
  `apps migrate`) prints those operations and their manifests, stops there under `--dry-run`, and
  otherwise applies exactly them. Targets are found by CCT's `cct.stratio.com/application_id` label
  (`<App.LiveName()>.<App.LiveNamespace()>`), skipping Flux-labelled objects; there are no
  hardcoded names, since the Python client's hardcoded DLC Ingress name was wrong on eosdev. Every
  delete or patch is UID-pinned. `prepare-genai`, a Postgres data rewrite no Kubernetes API can
  verify, has a `Query` (`DBQuery`): `RunQuery` finds the tenant's PgCluster primary pod by label
  (`pgcluster.stratio.com/pgcluster-name`/`-role`, the same idiom `legacyObjects` uses for CCT's app
  id) and execs the SQL there via `kubeclient.Execer` (a thin `client-go/tools/remotecommand`
  wrapper — the Go equivalent of `kubectl exec`, avoiding a shell-out). Its confirmation shows the
  real captured output and is never skipped by `--yes`, though `--dry-run` only resolves the pod and
  shows the SQL, asking nothing. A future component's own manual DB rewrite reuses this shape
  (a new `DBQuery` value) rather than one-off exec code.
- **`internal/tenantimport`** — `tenant import`'s live-cluster scan (CRD instances → Deployments →
  HelmReleases, three passes feeding one `Components` map), fixpoint-expanding mandatory
  dependencies, then rendering a tenant-file skeleton. Deliberately **not** reused by
  `internal/discovery` — different job (infer *which catalog component* a live object belongs to,
  scoped to a tenant's own namespaces) than discovery's (find *this exact name*, cluster-wide, no
  catalog involved).

## 7. Design decisions worth knowing before you change something

- **No persisted state, anywhere.** Every command re-derives everything it needs from the live
  cluster or rendered templates each run. If you're tempted to add a status file or a cache, that's
  almost certainly the wrong direction for this codebase — it was a deliberate reaction to bugs in
  the Python client's `status.json` model (see §9).
- **stdout is data, stderr is narration — never mixed.** `rootLogger(cmd)` binds to
  `cmd.ErrOrStderr()`; `internal/ui`'s renderers write to `cmd.OutOrStdout()`. This is what makes
  `flux stratio apps diff psql --view patch | some-other-tool` safe.
- **Backup/drift vs. diff/migrate is the sharpest architectural line in this codebase.**
  `internal/backup` and `internal/drift` go through `internal/discovery` (cluster-wide, no render,
  no tenant-file dependency). `internal/appdiff` (used by `apps diff`'s default/`--baseline` and by
  `apps migrate`) goes through `internal/render` (needs the tenant file to declare the component,
  because it's rendering *desired* state to compute a patch against). Don't casually swap one for
  the other — they answer different questions and have different prerequisites on purpose.
- **Idempotency contract**: `apps migrate` recomputes its patch from live state (or a `--baseline`
  backup) on every run and replaces patches by `target.kind` at the anchor — so `internal/render`
  renders the base *without* the tenant file's existing patches for the object's kind
  (`Result.ReplacedPatches`), making every computed patch the whole one, never a leftover delta that
  would drop what the existing patch carried; `appdiff.Result.UpToDate` is "the tenant already
  carries exactly this patch". Running it twice converges; it never trusts "I already migrated
  this." `--dry-run` on every mutating command; `--yes` skips confirmation
  (blank line/EOF both mean "no," ported from flux-keos's own `Confirm` semantics) — except
  `prepare-genai`'s confirmation, which `--yes` never skips.
- **JSON6902 vs. strategic-merge-patch is auto-detected, never a flag.** See
  `internal/diff.NeedsJSON6902`'s doc comment for the exact rule.

## 8. Testing conventions

In-package tests (`package backup`, not `backup_test`), `TestFunc_Scenario` naming, table tests via
an anonymous `cases := []struct{...}` + `t.Run`. `client/fake` (controller-runtime) for the cluster,
`runner.Fake` for subprocesses, `t.TempDir()` for the filesystem, `testdata/` fixtures for anything
resembling a real `keos-use-cases` template. `internal/catalog` and `internal/tenantimport` each
carry an `integration_test.go` that runs (not skipped) against the real, adjacent
`/stratio/gitops/keos-use-cases` checkout when present — this is where the real regression risk
lives, since `internal/catalog`'s fixture-based tests alone can't catch a template shape this
plugin hasn't seen yet.

`internal/cli` has **no** test files, by design (see §5) — don't add any there; put the logic
being tested in a feature package first.

## 9. Bugs fixed during (and after) the original port — for context on *why*, not just *what*

| Symptom in the Python client | Fix here |
|---|---|
| Generated tenant carried label `flux.stratio.com/tenant: "true"`, invisible to every current ResourceSet | `keos.stratio.com/resourceset-type: tenant-config` |
| Gosec agent patches routed via a `chart_map[x] is None` sentinel + name heuristics, silently mis-targeted or dropped | Derived from `postgres`/`opensearch[].config.agent`; ambiguity is a typed catalog state |
| `stderr=STDOUT` on a subprocess whose stdout was parsed as a patch YAML | `runner.Runner` never merges the two streams |
| Whole-file `yaml.dump` on every tenant-file edit, destroying every comment | `internal/tenantfile`'s `yaml.Node` in-place edit |
| `_output_patch` silently returned empty output on a missing `kubectl`, read by the caller as "no diff, migrated" | Every failure is a real Go `error`, propagated, never swallowed |
| Rendered vs. live numeric decoding used different conventions (float64 vs int64), causing spurious integer diffs | `internal/yamldocs.Decode`'s `*Unstructured` unmarshal target (§6) |
| `apps backup` (this plugin's own earlier version) required the tenant file to declare a component before it could find the live object — backwards for a pre-migration capture tool | `internal/discovery`, decoupling backup/drift from any render (this was a regression introduced *during* the Go port itself, not inherited from Python — see git history around "restore legacy backup behavior" for the full story) |
| `prepare-genai`'s own SQL (`internal/prepare/step_genai.go`), never actually executed before it was automated, connected to the wrong database (`psql`, the cluster's own database) — genai's schemas live in its own `genai` PgDatabase. Only found by running it for real against eosdev: both statements failed with "relation ... does not exist" | `Command`'s database arg fixed to `genai`; the DELETE for `genai-gateway` (a deprecated component superseded by litellm) is also now guarded with `to_regclass(...) IS NOT NULL` — a migration whose genai already moved to litellm has no such schema at all, and that's not an error |

## 10. Known rough edges / good first tasks

- `internal/appdiff.LiveManifestObject`/`LiveChartWorkloads` are exported with doc comments that
  still say "Exported for internal/backup" — that stopped being true once `internal/backup` moved
  to `internal/discovery`. They're currently only called by their own package's tests. Worth either
  unexporting them or deleting them and inlining at the two test call sites, plus fixing the stale
  comments either way.
- `docs/config-reference.md` and this file can drift from the actual `config.ComponentType` fields —
  if you add/remove one, update `docs/config-reference.md` and `internal/config/seed.go` in the
  same change.
- `tenant import`'s `ExtraConfig` scaffolding (`internal/catalog.Schema.ExtraConfig`) only
  understands a flat scalar read directly off `$componentConfig`, with an optional quoted-string
  default — a nested sub-config (a variable assigned from `$componentConfig` and read for several
  leaf keys elsewhere, e.g. genai's own `llmModels.{chat,governance,translate}`) is silently
  dropped from the generated skeleton with no warning, not just under-scaffolded. See
  `docs/TASK-tenant-import-nested-config-scaffolding.md`.
