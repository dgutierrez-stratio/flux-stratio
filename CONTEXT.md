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
flux stratio doctor                            # preflight: binaries (+ optional meld), config, repo layout, cluster, tenant file

flux stratio config init --base --cluster --tenant [--output] [--force]
                                                 # seed a config file from the known 16-app Stratio catalog

flux stratio tenant import [--size] [--output] [--force]
                                                 # scan the live cluster → tenant RSIP skeleton

flux stratio apps backup <id> | --catalog | --all [--dir]
                                                 # capture live state; --catalog = config apps, --all = everything discovered

flux stratio apps diff <id> [--baseline <path>] [--drift <path>] [--view unified|patch|meld]
                                                 # --baseline: pre-migration (desired vs. a backup instead of live)
                                                 # --drift: post-migration (live now vs. a backup, no GitOps render)

flux stratio apps migrate <id> | --all [--dry-run] [-y/--yes] [--continue-on-error]
                                                 # diff (running any declared prepare step first), then splice the patch
```

Persistent flags on every command: `--config`, `--base`, `--cluster`, `--tenant`, `--kubeconfig`,
`--kube-context`, `-v/--verbose`. Run `flux stratio apps diff --help` for the fullest single
explanation of the baseline-vs-drift distinction in the codebase — it's worth reading before
touching `internal/appdiff` or `internal/drift`.

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
  mocking, `runner.Fake` for subprocess mocking, `t.TempDir()` for filesystem state. 20 packages,
  ~316 tests, all currently green.
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

- **`internal/config`** — `Config`/`App`: the *external*, version-controlled app catalog (never
  embedded in the binary at runtime — see the design rationale in the git history if curious why).
  `Resolve`/`Load` implement the config-file lookup ladder (`--config` →
  `$FLUX_STRATIO_CONFIG` → `~/.fluxcd/flux-stratio/config.yaml` (a sibling of `~/.fluxcd/plugins/`, never inside it) → `./flux-stratio.yaml`), strict
  `KnownFields(true)` decoding. `Seed` (new, `config init`) is the *one* place a hardcoded app list
  legitimately lives in this codebase — a one-time scaffold, ported from the legacy `config.json`,
  never consulted at runtime by any other command.
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
  declared env vars against a live workload's resolved ones. `CoerceValue` handles the
  octal-looking-string/float/`yes`-`null` typed-value edge cases the Python client got wrong.
- **`internal/appdiff`** — orchestrates `internal/render` + `internal/diff` for `apps diff`'s
  default/`--baseline` comparison and for `apps migrate`'s patch computation — **the one place both
  commands compute a diff, so they can never disagree about what a migration would do.** Dispatch
  is by `App.ChartPath` (chart-mode vs. manifest-mode), never a flag the operator sets. Exports
  `MergeLiveEnv` and `FetchLiveWorkloads` (chart-mode's "fetch every live Deployment/StatefulSet/
  DaemonSet a chart declares, applying `Renamed`/`PreviousNamespace`") for reuse by
  `internal/backup`'s own chart-mode capture — the one legitimate cross-package dependency from
  backup back into appdiff, and only for this pure "given rendered docs, fetch the matching live
  objects" helper, never appdiff's render-triggering entrypoints. `LiveManifestObject`/
  `LiveChartWorkloads` are also exported but **currently unused outside this package's own
  tests** — see §10, this is a known stale spot.
- **`internal/discovery`** — cluster-wide, name-indexed live-object scanner: lists every
  Kustomization, HelmRelease, Deployment/StatefulSet/DaemonSet, and 14 hardcoded Stratio operator
  CRDs (verified against a real cluster's installed CRDs, not copied from the Python client's own
  differently-cased dict) — **with no namespace filter and no tenant-file dependency at all**. This
  is what lets `apps backup`/`apps diff --drift` work on an app whose component isn't declared in
  the tenant file yet, unlike `internal/render`. `Index.Find{CR,Workload,HelmRelease,
  Kustomization}` are per-kind lookups; `Index.Names()` is the union across all four, used by
  `apps backup --all`.
- **`internal/backup`** — captures an app's live state to
  `<dir>/<App.ID>/<UTC-timestamp>/{cr.yaml | deployment.yaml+env-vars.env |
  helmrelease.yaml+values.yaml}`, dispatching on `App.ChartPath` with a graceful fallback cascade
  (mirroring the Python client's own CR → Deployment → HelmRelease-only priority) when the live
  object isn't backed by the expected kind — logging a warning, not failing, since `apps backup
  --all`/`--catalog` must not abort on one app's shape surprise. Chart-mode capture sources `helm
  template`'s values from the **live** HelmRelease it just found via `internal/discovery`, never a
  flux-rendered *desired* one — capturing desired-state values would defeat backup's own purpose.
  `DiscoveredApps(cfg, idx)` builds the app list for `--all`: a catalog entry when a discovered
  identity's live name matches one (so it's captured identically to `--catalog`), a minimal
  synthetic `App{ID, Name, Object}` otherwise. `ResolveBaseline(root, appID)` is the shared
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
  `prepare-datarest`, `prepare-dlc`, `prepare-genai`), each a `Step{Satisfied, Run}` pair. No CLI
  surface of its own — `internal/cli`'s `ensurePrepared` (in `apps_migrate.go`) is the only caller,
  wired as the first stage of `apps migrate`. `prepare-genai` is a manual Postgres data rewrite no
  Kubernetes API can verify; its `Satisfied` always returns `false`, and its confirmation is never
  skipped by `--yes`.
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
- **Idempotency contract**: `apps migrate` recomputes its patch from live state on every run and
  replaces patches by `target.kind` at the anchor — running it twice converges, it never trusts "I
  already migrated this." `--dry-run` on every mutating command; `--yes` skips confirmation
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

## 10. Known rough edges / good first tasks

- `internal/appdiff.LiveManifestObject`/`LiveChartWorkloads` are exported with doc comments that
  still say "Exported for internal/backup" — that stopped being true once `internal/backup` moved
  to `internal/discovery`. They're currently only called by their own package's tests. Worth either
  unexporting them or deleting them and inlining at the two test call sites, plus fixing the stale
  comments either way.
- `docs/config-reference.md` and this file can drift from the actual `config.App` struct fields —
  if you add/remove a field on `config.App`, grep for `docs/config-reference.md` and
  `internal/config/seed.go` in the same change.
