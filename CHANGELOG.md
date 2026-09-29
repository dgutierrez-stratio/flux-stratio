All notable changes to this project will be documented in this file.

## 0.1.0-SNAPSHOT

* **Config split into a typed component catalog and an environment file.** `config init` now
  writes `~/.fluxcd/flux-stratio/catalog.yaml` and `environment.yaml` (`--dir`, `--force`,
  `--charts`) instead of one `config.yaml` whose flat `apps:` list mixed static coordinates with
  per-environment instance values:
  * `catalog.yaml` holds 19 **component types** — static facts only (`component`, `rset`, `chart`,
    `exclude`, `prepare`), `entry`/`object`/`kustomization` name templates, and `match` selectors
    (`kinds` + label/annotation selectors) that recognize a type's live legacy objects. Every
    seeded selector comes from the CCT `application_service`/`application_model` annotations on
    real captured legacy objects. `renamed`/`previousNamespace` are gone: the live object's own
    name and namespace are used.
  * `environment.yaml` holds `base`/`chartsBase`/`cluster`/`tenant` (`--env-config`,
    `$FLUX_STRATIO_ENV`); `--base/--cluster/--tenant` still override it.
  * New `internal/components` classifies live objects against the catalog and resolves each
    instance against the tenant file, asking — only when it can't infer it — which type or which
    declared tenant entry a live object is. `--as <type>[/<entry>]` answers up front; `apps migrate
    --yes` fails naming `--as` instead of asking. Answers are never stored.
  * `apps diff/backup/migrate <name>` take a live name (`psql-agent`) or a GitOps object name
    (`psql-gosec-agent`); `migrate --all`/`backup --catalog` cover every classified instance,
    skipping (with a warning) types the tenant file doesn't declare.
  * `doctor` checks the catalog and environment separately, and every type's `component` key and
    `prepare` step against the real `keos-use-cases` templates.
  * A pre-catalog `config.yaml` is recognized and rejected with a pointer to `config init --force`.
* New seeded type `opendashboards` (OsDashboards, `Dashboards-Opensearch`): no excludes — the
  legacy image pin and `admin.<tenant>.<domain>` exposition host are deliberately kept.
* New seeded type `litellm` (Deployment, `genai` / `genai-litellm`). It migrates under its legacy
  name, so the chart's identity (cert CN = Postgres user, gosec user) is the legacy one, and the
  patch keeps the legacy Postgres database/schema, gosec groups and `autoUvicornWorkers`; the
  networking/SSO URLs and vault `approlename` are excluded. The legacy Vault secrets are not
  reused — the chart's SecretsBundle prunes every undeclared key in its folder — so the rows
  LiteLLM encrypted with the legacy salt are cleaned before cutover and the models re-registered
  from the tenant entry's `config.models`. Needs keos-apps/keos-use-cases with a configurable
  litellm name (`LITELLM_NAME`) and the litellm chart whose GosecPolicy follows `postgresDatabase`.
* Fix: `apps diff`/`migrate <name>` failed with "none is selected by any catalog type" on a
  chart-mode component already migrated under its legacy name (virtualizer): the HelmRelease adopts
  the legacy Deployment and Helm strips the CCT annotations the selectors need. A named lookup now
  falls back to a live object rendered by a HelmRelease deploying the type's chart and labelled as
  the run's tenant; `--all` still selects by the catalog's selectors alone.
* Fix: `dg-agent`'s HelmRelease name is fixed by its storage-type overlay (`dg-hdfs-agent`,
  `dg-s3-agent`), not derived from the tenant entry: mapping a live agent to a differently named
  entry failed with "object not found". `object` is now the live name, `kustomization`
  `apps-<entry>`.
* `apps migrate`'s prepare steps now plan before acting: every operation and the live manifest of
  each object it touches are shown (`--dry-run` stops there), then exactly those run, each pinned to
  the planned object's UID. Targets are the app's CCT objects (by `cct.stratio.com/application_id`),
  never Flux-managed ones or hardcoded names. Fixes: `prepare-dlc` looked for the wrong Ingress name
  (it deleted only the Deployment and left the colliding Ingress) and, like
  `prepare-datamarket-agent`, could hit the same-named GitOps workload on a re-run;
  `prepare-datamarket-agent` waited on a label its pods don't carry, so it never waited.
* Fix: confirmation prompts were written to stdout, so with stdout redirected (e.g. to capture
  manifests) the prompt landed in the file and the command waited unseen. They go to stderr.
* An object CCT annotated as another tenant's (`cct.stratio.com/application_tenant`) is never an
  instance: eosdev's platform `opensearch1` (tenant `keos`, `keos-core`) no longer shadows the
  `stratio` tenant's own — name-only discovery had been backing up the `keos-core` copy.
* Fix: re-running `apps migrate` on a component whose tenant entry already carried a patch diffed
  base+patch against live and spliced the *leftover delta* in as a replacement, silently dropping
  every field the existing patch carried. The base is now rendered without the existing patch for
  the object's kind, so the whole patch is always recomputed; one the tenant file already carries
  exactly is reported as up to date.
* Fix: `apps diff --baseline` decoded a backup's `cr.yaml` into a plain map, turning every integer
  into a float64 — every unchanged integer field (probe thresholds, `minAvailable`, …) was reported
  as a difference and back-ported into the patch. It now decodes through `internal/yamldocs.Decode`
  like the rendered and live sides.
* `apps migrate --baseline <backup>` (with `--dir`): compute the patch from a pre-cutover backup
  instead of live, for a component Flux already reconciled unpatched. `apps diff`/`migrate` warn
  when the live object is already Flux-managed.
* Fix: `apps backup` captured a same-named `PgDatabase` as the `genai` and `rocket` apps (name-only
  lookup, CRs first). A classified instance is now captured from the exact live object it was
  classified from, and `apps backup --all` captures the unrelated same-named object separately.
* Initial release: application migration ported from the Python `migrate.py` client's `tenant`,
  `backup`, `patch`/`patch --chart-path` and `migrate-app` commands, redesigned around
  `flux stratio tenant import`, `apps backup`, `apps diff` and `apps migrate`.
* Fix: the generated tenant `ResourceSetInputProvider` now carries the label
  `keos.stratio.com/resourceset-type: tenant-config`, matching what every current
  `keos-use-cases` template actually selects on. The ported client emitted
  `flux.stratio.com/tenant: "true"` instead, making every tenant it generated invisible to every
  ResourceSet.
* Fix: a postgres/opensearch gosec agent's patch is now spliced into its parent component's
  `config.agent.patches`, not a nonexistent top-level `gosecAgentPostgres`/`gosecAgentOpensearch`
  component entry — `keos-use-cases` dropped those component keys; the gosec agent is derived from
  `postgres`/`opensearch`'s own `config.agent` block instead.
* Fix: `pgbackupLogical`/`pgbackupPhysical` (both deployed from the shared `pgbackup` chart) are
  disambiguated explicitly via `internal/catalog.ChartMapping.Ambiguous`, rather than silently
  collapsing into the same sentinel value gosec-agent detection also used.
* Fix: `apps migrate` edits the tenant file's `patches:` sequence in place via a comment-preserving
  YAML node tree, instead of decoding into a plain map and re-dumping the whole file — every
  comment and commented-out component block in the tenant file survives.
* Fix: rendered (chart/`flux build`) and live (cluster-fetched) numeric values now decode through
  the same convention (`k8s.io/apimachinery`'s int64-for-whole-numbers, not the JSON-default
  float64), so an unchanged integer field never shows up as a spurious diff.
* `apps migrate --all` orders apps by the dependency edges already declared in the tenant file
  (a dependency migrates before its dependent) instead of config-file order.
* `apps migrate` runs an app's declared `prepare` step automatically, under the same
  `--dry-run`/`--yes` gate as the migration itself; `prepare-genai` (a manual Postgres data
  rewrite no Kubernetes API can verify) always asks its own separate confirmation instead, never
  skipped by `--yes`.
* `apps diff --baseline <dir>` diffs against a previous `apps backup` capture instead of the live
  cluster.
* `flux stratio doctor`: a single preflight pass over required binaries, the config file, the
  `--base` repo layout, cluster access and the tenant file.
* `flux stratio config init`: writes a starting config file seeded with the known 16-application
  Stratio catalog (ported from the legacy client's `config.json`), instead of requiring the whole
  `apps:` list to be authored by hand on first use. Fixes two gaps found in that source: `dlc-entity`
  now declares `prepare: prepare-dlc` (the step exists specifically to gate it, but the legacy
  config never wired it up), and `datamarket-agent`'s `previousNamespace` is derived from the
  tenant instead of hardcoded to `stratio-datastores`.
* Fix: `apps backup` no longer requires the app's component to be declared in the tenant file. It
  previously reused `apps diff`'s render-first live-object lookup purely to learn a namespace/GVK,
  which meant a backup could only ever succeed for an app already active in the tenant's
  `ResourceSetInputProvider` — exactly backwards for a command whose job is capturing state
  *before* migration. `internal/discovery` restores the legacy Python client's own approach
  (cluster-wide discovery by name, no render, no tenant file) instead, with the same CR > Deployment
  > HelmRelease-only fallback cascade `backup.py` used. This also restores the `helmrelease.yaml`/
  `values.yaml` capture shape the Go port had dropped entirely, including the "00"-prefixed
  ConfigMap values resolution `backup.py` falls back to when a HelmRelease has no inline
  `spec.values`.
* `apps backup <id>` becomes `apps backup <id> | --catalog | --all`: `--catalog` is the old `--all`
  (every app in the config catalog, renamed to avoid ambiguity with the new flag); `--all` backs up
  every distinct live identity `internal/discovery`'s scan finds, catalog or not — matching how
  Python's own `list`/`discover_workloads` was always cluster-wide and never scoped to
  `config.json`'s curated application list. A catalog app is captured identically whichever flag
  reaches it (same directory, same shape `apps diff --baseline` expects) — `--all` only widens
  *which* names get looked up, never *how*.
* `apps diff --drift <backup>`: compares the live cluster right now directly against a stored
  backup, with no GitOps rendering at all — for checking operational drift after a cutover, when
  the desired-vs-live comparison `apps diff`'s default (and `--baseline`) does is no longer the
  interesting question. Reuses `apps backup`'s exact capture logic (via a throwaway temporary
  directory) for the live side, so drift is always compared against the same file it would have
  written; a shape mismatch (e.g. the live object resolves to a CR now but the stored backup was a
  HelmRelease) is reported clearly instead of comparing unrelated files.
* `apps diff --baseline`/`--drift` no longer require the exact timestamped backup path:
  `--baseline latest`/`--drift latest` auto-locates an app's most recent `apps backup` capture, and
  a partial path (an app's own backup directory, or the overall `backups/` root) resolves the same
  way.
* `apps diff --patch`/`--meld` (two independent bools that could never sensibly be combined, and
  needed a runtime check to reject `--patch --meld`) become one `--view unified|patch|meld` flag
  (default `unified`, today's behavior). `meld` opens the comparison in
  [meld](https://meldmerge.org/) instead of printing a unified diff; `flux stratio doctor` reports
  meld's availability as a non-blocking, informational check — it only gates `--view meld`, never
  doctor's own pass/fail. `apps diff --help` now documents the full baseline-vs-drift distinction
  (pre-migration vs. post-migration) in detail, not just a one-line flag description.
* Fix: `Makefile`'s `clean` target now removes exactly `bin/flux-stratio` and
  `bin/flux-stratio-*.tar.gz` (matching `.gitignore`), instead of an unqualified `rm -rf
  flux-stratio*` that both failed to clean the actual build output (nested under `bin/`, never
  matched from the repo root) and could delete an unrelated `flux-stratio.yaml` sitting in the
  working directory.
* The config file's stable, outside-the-repo location moves from `~/.config/flux-stratio/` to
  `~/.fluxcd/flux-stratio/config.yaml` — a sibling of `~/.fluxcd/plugins/` (where the plugin binary
  itself lives per RFC 0013), never a subdirectory of it. `apps backup`'s default `--dir` (always
  "next to the resolved config file") follows automatically, so a config kept there — instead of a
  `flux-stratio.yaml` in whatever directory happens to be the working one — keeps both config and
  backups permanently outside any flux-stratio checkout, safe from `make clean`, `git clean`, or a
  fresh clone.
* Fix: a single-app failure (`apps diff`, `apps diff --drift`, `tenant import`) no longer prints the
  same error up to three times. Two causes, both fixed: the root command left cobra's own default
  error-printing on (`SilenceErrors: false`) even though `cmd/flux-stratio/main.go` already prints
  `Error: <err>` once itself after `Execute` returns; and three call sites additionally narrated the
  exact same error via `logger.Failuref` right before returning it verbatim. A multi-app loop
  (`apps backup --all`, `apps migrate --all`) still narrates each failure via `✗` and returns a
  separate, aggregated summary error — that's intentional, not a duplicate.
* Fix: a generated patch (`apps diff --view patch`, and what `apps migrate` splices into the tenant
  file) is now 2-space indented, matching every other YAML file this plugin writes. The patch body
  is a pre-rendered string embedded verbatim inside the tenant file's literal `patch: |` block, so
  it was never re-flowed by `internal/tenantfile`'s own already-correct 2-space encoder — it needed
  fixing at the source (`internal/diff.MarshalPatchYAML`/`PatchEntryNode`), not just at the file
  writer.
* `apps diff --view meld` now opens meld with its two temp files named after what each side actually
  is — `rendered`/`live` (or `rendered`/`backup` for `--baseline`), `backup`/`live` for `--drift` —
  instead of a generic `before`/`after`, which meant something different depending on the comparison
  and read backwards for a pre-migration diff (the rendered GitOps state isn't chronologically
  "before" anything).
* `apps diff --view meld` (both the desired-state comparison and `--drift`) now opens meld even
  when there are no differences, after the "no differences"/"no drift" line, instead of silently
  skipping it. When the tenant file already carries exactly the patch needed, the two sides still
  differ by that patch (the base is rendered without it), so meld shows what it covers.
* `config init --charts <path>` seeds an optional top-level `chartsBase` config field: when set, it
  overrides `base` for resolving a chart-mode app's `chartPath` into an on-disk Helm chart
  directory. Fixes chart-mode apps (`apps diff`/`--baseline`/`--drift`, `apps backup`,
  `apps migrate`) failing with "could not find `<base>/<chartPath>`" whenever the chart-source repo
  isn't checked out as a sibling of `base`'s `keos-apps`/`keos-use-cases`/`keos-fleet`/
  `keos-system-services`. Leaving `chartsBase` unset keeps `chartPath` resolving relative to `base`
  exactly as before.
* Code review follow-ups:
  * Fix: `apps backup --all`, mid-migration, could assign the same `App.ID` to two different live
    objects (a Renamed app's legacy name and its new, already-live GitOps name both present at
    once), silently mixing two captures into one `backups/<id>/` directory. `DiscoveredApps` now
    disambiguates the colliding synthetic entry with a `-live` suffix.
  * Fix: chart-mode `apps backup` templated the chart with the live HelmRelease's own (possibly
    legacy, Renamed) name instead of `App.Object`, unlike `apps diff`'s own chart-mode render —
    for a Renamed, multi-workload chart whose sibling names derive from `.Release.Name`, this could
    miss or mis-resolve sibling workloads during backup. Both now use `App.Object`, consistently.
  * `flux stratio doctor` now validates that every chart-mode app's on-disk chart directory
    (`chartsBase`-or-`base` + `chartPath`) actually exists, instead of only the four `base` sibling
    repos — a misconfigured `chartsBase`/`chartPath` is now caught up front, not mid-run.
  * `apps diff` gained a `--dir` flag so `--baseline latest`/`--drift latest` can resolve a backup
    captured with a matching `apps backup --dir <custom>`; previously `latest` could only ever look
    under the default backups location.
  * `internal/discovery`'s ambiguous-name resolution (more than one live object sharing a name,
    e.g. across tenants reusing the same app id) now sorts matches by namespace before picking one,
    so the pick is deterministic instead of dependent on the cluster API's own unspecified list
    order.
  * `flux stratio config init` is now 2-space indented too, matching every other YAML file this
    plugin writes.
  * A single-app `apps migrate <id>`/`apps backup <id>` failure no longer narrates the same error
    twice (once via `✗ "<app>": <err>`, again via the aggregated `Error: migration/backup failed
    for: <id>`) — that aggregation only adds information for the genuinely multi-app `--all` case,
    where it still applies.
  * `config.App.LiveName()` replaces two byte-identical copies of the same Renamed-or-Object
    fallback that had drifted apart into `internal/appdiff` and `internal/backup`.
