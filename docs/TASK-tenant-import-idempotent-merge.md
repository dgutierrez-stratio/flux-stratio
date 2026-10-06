# Task: `tenant import` that works on a fresh tenant and, idempotently, on an existing tenant file

**Status: implemented (unit-tested). Verified on eosdev against a scratch copy of the tenant file: `--dry-run` and the write add exactly `uiEnabled: true` to virtualizer; a second run says "nothing to add" and leaves the file untouched; Create to stdout and to a new path work. Still to do: `apps diff`/`apps migrate --dry-run virtualizer` and `helm template` against the completed file in keos-fleet (verification 2-3), which needs the keos-fleet tree written.** Repo: `flux-stratio` (this one), branch `feat/application-migration`.
Related: `docs/TASK-tenant-import-nested-config-scaffolding.md` (the same silent-drop mechanism, found earlier; this task
subsumes its point 3), `docs/TASK-chart-spec-comparison.md` (where this was found), `docs/migration-runbook.md` §tenant import.

## Why this task exists

Migrating virtualizer on eosdev left the new server pod logging
`permission denied for database "virtualizer-monitor"`. Root cause, traced through the repos:

1. The chart's `virtualizer` release renders the monitor-DB Gosec policy and binding **only when
   `controllers.monitor.enabled` is true**. The server user's grant on the monitor database lives in that binding.
2. keos-apps `components/virtualizer/app/base/helm.yaml` (lines 45-49) sets both `controllers.monitor.enabled` and
   `controllers.ui.enabled` to `${STRATIO_VIRTUALIZER_UI_ENABLED}`.
3. keos-use-cases `apps/components/resourceset-apps-apps.yaml:246` fills that variable from the entry's
   `config.uiEnabled`, and **defaults it to `false`** when the key is absent.
4. The eosdev tenant file's virtualizer entry has no `uiEnabled`. The live cluster still runs the legacy
   `virtualizer-monitor` and `virtualizer-ui`, so the migration silently dropped both from the desired state.

`tenant import` is the command that scaffolds the tenant file, so it should have written `uiEnabled`. It did not, and
nothing told the operator. The Python client emitted it (`cmd_tenant.py`: `_AFTER_DEPS_FIELDS = {"uiEnabled"}`, default
`false`, with a reminder when virtualizer is present); the Go port lost it.

Two further problems make the command unusable on the file the operator actually has:

- `tenant import --output <existing>` refuses without `--force`, and `--force` regenerates the whole file, **dropping
  every hand-written field, comment and patch** (`internal/cli/tenant.go`, `runTenantImport`). The eosdev file is
  mid-migration: roughly half of it is commented-out component blocks and it carries migrated patches. Regenerating is
  not an option, so today there is no way to fix a missing field except by hand.
- Fields the scan cannot fill are dropped without any warning (below).

## What the command must do after this task

`flux stratio tenant import --output <file>` has two modes, chosen by whether `<file>` exists:

| Mode | When | Behaviour |
|---|---|---|
| **Create** | `<file>` absent, or no `--output` (stdout) | Today's behaviour, plus the scaffolding fixes below. Output is a fresh skeleton. |
| **Complete** | `<file>` exists | Load it with `internal/tenantfile` (comment-preserving), scan the cluster, and **add only what is missing**. Never remove, reorder, uncomment or overwrite anything already there. Report what it added and what it left alone. |

`--force` keeps its current meaning (regenerate, drop hand-written content) and is the only way to get that. Complete
mode needs no flag: it is the safe default when the file exists.

### Idempotency contract

- Running Complete twice in a row, with the cluster unchanged, changes nothing: the second run writes no bytes and says
  "nothing to add".
- The result of Complete depends only on (file content, cluster state, catalog), never on a previous run.
- Existing values always win. A value the operator wrote (including `uiEnabled: false`, an `enabled: false`, any patch,
  any dependency name) is never changed. If the scan disagrees with it, that is a **warning**, not an edit.
- Anything written is a pure addition of a key, an entry or a dependency, so reviewing `git diff` is trivial.

## What counts as "missing" (Complete mode)

Applied to every **active** (uncommented) entry under `spec.defaultValues.components`:

1. **Config fields the component's template reads** (`catalog.Schema.ExtraConfig`) that the entry lacks. Scaffolded with
   the template default, or with a value derived from the live cluster where one exists (item 2). A field with no usable
   default is added as a placeholder only when its shape is known; otherwise it is reported (see "Scaffolding fixes").
2. **`virtualizer.config.uiEnabled`**: `true` when the scan finds a live `virtualizer-monitor` or `virtualizer-ui`
   Deployment (legacy **or** Helm-managed: once those run under Helm, an absent `uiEnabled` would make the next release
   remove them), `false` otherwise. Never overrides an existing value; warns when the file says `false` and either
   Deployment is live.
3. **Dependencies** declared by the schema that the entry lacks, resolved from the scan exactly as Create mode does.
   An unresolvable one is not added (an empty `name: ""` would only be broken YAML for the render) and is reported.
4. **`config.type`** (storage type) when missing and inferable, as in Create mode.
5. **Top-level fields** of the RSIP that are absent (`tenantName`, `size`, `sopsSecretProvided`, `internalS3BucketName`,
   `userEmail`, `userId`, `userName`). Never touches the ones that exist, nor fields the import does not own
   (`universeVersion`, `universePrereleaseSuffix`, whatever else is there).
6. **Components discovered live but not declared**: added as new entries (decided after implementation: no `--add-missing` flag). The operator
   may have left a component out on purpose.

Never touched, never re-added:

- **Commented-out component blocks** (`tenantfile.CommentedOut`). The operator is enabling components one at a time; an
  uncommented duplicate would be a bug. Reported as "left commented out". Their config is not edited either (they are
  comments): a note says to run `tenant import` again after uncommenting, which Complete mode makes safe.
- **Skeleton entries for components the scan did not find.** Create mode adds them (`expandMandatoryComponents`); Complete
  must not, because the component may already exist under another name or be commented out. Measured on eosdev:
  the import proposes `dg-agent` while the file has `dg-hdfs-agent` (commented out) and proposes a `litellm` skeleton.

## Measured on eosdev (2026-10-05)

Command run read-only, output to a scratch file: `flux stratio tenant import > scratch.yaml`, keos-use-cases at
`keos-use-cases-plt-4838` (`a1b042a`). Compared with `keos-fleet/clusters/eosdev/tenants/config/stratio.yaml`.

- `virtualizer` entry: **no `uiEnabled`**, no warning. Confirmed empirically.
- Cause, from a throwaway dump of `catalog.Schemas` (kept out of the tree): the template reads the field as
  `<< if hasKey $componentConfig "uiEnabled" >><< get $componentConfig "uiEnabled" >><< else >>false<< end >>`.
  `extraConfigRe` matches the `get` but finds no `| default "…"`, so the field is recorded with `HasDefault: false`, and
  `enrichEntry` (`internal/tenantimport/enrich.go`) skips every field without a default **silently**.
- The same silent drop affects every field the extraction sees without a quoted-string default:

  | Component | Field(s) dropped today |
  |---|---|
  | virtualizer | `uiEnabled` |
  | genai | `llmModels`, `governanceOpenSearchEnabled` |
  | litellm | `models` |
  | opensearch | `backup`, `agent` |
  | postgres | `recoveryEnabled`, `agent` |
  | osbackup, osrestore | `indices` |
  | pgbackuprepository, pgbackupLogical, pgrestore | `storageClassName` |

  (Some are nested structures, which is the subject of `TASK-tenant-import-nested-config-scaffolding.md`.)
- Coverage gap to investigate, not assumed: the import found no `bdlDatarest` (the file has `dg-datarest-pgi`, commented
  out) and no `datamarketAgent`, although both run on the cluster. Check whether they live outside the tenant
  namespaces the scan reads or are missing from the chart/namespace mapping.
- Naming: the import's `dg-agent` skeleton vs the file's `dg-hdfs-agent`; the import also leaves
  `governancePgbouncer`/`governancePostgres` empty with a warning.
- The eosdev file has components commented out as `# <key>:` blocks and a `universeVersion`/`universePrereleaseSuffix`
  pair the import knows nothing about. Both must survive a Complete run byte-for-byte.

## Scaffolding fixes (apply to both modes)

1. **Never drop a field silently.** For every `ExtraConfig` field without a default, `enrichEntry` warns
   (`<entry>: config "<key>" has no default; set it by hand`) or, where the shape is known, scaffolds it. This is the
   cheapest and highest-value change and is point 3 of the nested-config task.
2. **Recognise the `hasKey … else <literal>` pattern** as a default, so `uiEnabled` carries `false`. Extend `extraConfigRe`
   (`internal/catalog/extract.go`) or add a second pattern; add a table test with this exact template line.
3. **Live-derived values** (item 2 above) come from a new scan of the tenant's Deployments by name, using the catalog
   siblings (`config.Chart.Siblings` for virtualizer and genai). Which sibling set switches which config field has to be
   declared somewhere; recommended: an optional catalog field on the chart (for example `enabledBy: uiEnabled` on the
   virtualizer type) instead of hard-coding virtualizer in the importer. See "Open decisions".
4. **Document `uiEnabled`** in the generated file's trailing reminder, as the Python client did.

## Implementation outline

- `internal/tenantimport/merge.go` (new): `Complete(doc *tenantfile.Doc, scanned Components, cat, opts) (Report, error)`.
  Works on the `yaml.Node` tree so comments and order survive. Reuses `tenantfile.EntryNames`, `FindComponentEntry`,
  `CommentedOut`, `DependencyNames`, `mapGet`/`mapSet` (export what is needed from `internal/tenantfile`, or add the
  small mutators there: `SetConfigField`, `SetDependency`, `AppendEntry`).
- `internal/tenantimport/tenantimport.go`: `Run` splits into `Scan` (the existing scan → expand → enrich pipeline, minus
  skeleton expansion in Complete mode) and `Render` (Create) / `Complete` (merge).
- `internal/cli/tenant.go`: when `--output` exists and `--force` is not set, run Complete and `Doc.Save` (atomic, refuses
  if the file changed on disk meanwhile, already implemented). Add `--dry-run` (print what would be
  added as a diff, write nothing). Update the `--output`/`--force` help and the `tenantImportLong` text in
  `internal/cli/help.go`.
- Report (stderr, same style as `reportChartReview`): grouped lines `added`, `left alone`, `warning`, and a final
  `nothing to add` when idempotent.
- Writes into the operator's keos-fleet working tree only through `Doc.Save`; the command never commits.
- Docs: `CHANGELOG.md`, `README.md` (tenant import), `docs/migration-runbook.md` (the import section now describes both
  modes), `CONTEXT.md` if the package map changes.

## Tests

Table-driven, fixtures under `internal/tenantimport/testdata/`.

- **Idempotency:** Complete(file) then Complete(result) is byte-identical to the first result, for every fixture.
- **Preservation:** a fixture shaped like the eosdev file (commented-out `# key:` blocks, patches, `universeVersion`,
  inline comments, mixed indentation of comments). Only the intended lines differ; use a golden diff.
- **uiEnabled:** live legacy monitor/ui → `true`; none → `false`; Helm-managed monitor/ui → `true`; existing `false` with
  live siblings → untouched plus a warning; existing `true` → untouched.
- **Commented-out component:** never uncommented, never re-added; a note is reported.
- **No skeleton in Complete:** the `dg-agent` vs `dg-hdfs-agent` case adds nothing.
- **Missing dependency:** resolved one added; unresolved one reported, not added.
- **Dropped fields:** every `HasDefault: false` field produces a warning (or a scaffold) in both modes.
- **`hasKey … else false` extraction:** unit test in `internal/catalog/extract_test.go` with the real template line.
- **CLI:** exists + no flag → Complete; exists + `--force` → regenerate; `--dry-run` writes nothing; missing → Create.
- `make fmt-check vet test build`, golangci-lint.

## Verification on eosdev (your keos-fleet tree is only written where stated)

1. `flux stratio tenant import --output <scratch copy of stratio.yaml> --dry-run`: the diff is `uiEnabled: true` on
   virtualizer plus whatever else item 1-5 find; nothing else changes. Re-run: "nothing to add".
2. Same command with `--output` pointing at `keos-fleet/.../stratio.yaml` (uncommitted write, never committed by the tool):
   `git diff` shows only additions. A second run produces no diff.
3. `flux stratio apps diff virtualizer` / `apps migrate virtualizer --dry-run` against the completed file: monitor and ui
   are rendered and compared (the sibling machinery already exists), and a `helm template` of the result contains the
   monitor-DB Gosec policy and binding.
4. Fresh-tenant path: `tenant import` to stdout with no file, and with `--output` to a new path: same as today plus the
   new fields and warnings.

## Open decisions (for the owner)

1. **Where the "which config field enables which sibling" knowledge lives.** Recommended: an optional catalog field on the
   chart type next to `siblings` (`enabledBy`), so virtualizer is not special-cased and genai can use it if it needs to.
   Alternative: hard-code `uiEnabled` for virtualizer (smaller, but it repeats the original mistake).
2. **`--add-missing`.** Decided: no such flag; Complete always adds live components the file does not declare (never under a
   commented-out key, with a warning when the key already has another entry).
3. **`--force` semantics.** Recommended unchanged (full regenerate). A softer default would hide data loss.

## Not in this task (separate follow-ups)

- `apps diff`/`apps migrate` warning when a catalog sibling is live but the chart renders no workload for it (the check
  that would have caught this on an already-written tenant file even without `tenant import`).
- Normalising `fieldRef` env values between the live and rendered sides (`VR_ACCOUNT`, `VR_IP`, `VR_NAMESPACE`,
  `VR_POD` show as differences after migration; they are the same references) and resolving
  `metadata.annotations['…']` references (the `VR_DEPLOYMENT`/`VR_SERVICE` review warnings).
- Charts: make the server's monitor-DB grant depend on `monitoringEnabled` rather than on the monitor controller.
- Deleting the legacy monitor/ui Deployments: stays the operator's manual cutover.
