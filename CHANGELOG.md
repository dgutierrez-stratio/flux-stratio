All notable changes to this project will be documented in this file.

## 0.1.0-SNAPSHOT

* **`apps diff` / `apps migrate` list what `exclude` keeps out.** A chart-mode difference hidden by an
  app's `exclude` (genai's Vault roles) is no longer dropped without a word: it is printed as
  `excluded by the catalog: <workload>/<name> live "…", GitOps default "…" (<path>)`. Informational
  only — never a warning, so `--yes` isn't stopped — and the patch is unchanged.
* **`apps migrate` flags a gosec-agent patch the legacy client left on the parent entry.** A
  `HelmRelease` patch for `<agent>` in the parent's top-level `patches` matches nothing (the agent
  reads `config.agent.patches`); it is reported as a warning to remove by hand, never deleted.
* **`apps backup --all` captures what it used to drop.** An object is skipped only when a catalog
  app was classified from that exact object (kind, namespace, name), not when it merely shares a
  name: the PgDatabases named like their app (rocket, discovery, intelligence, dlc-entity,
  virtualizer...) and a second namespace's same-named objects (`opensearch1` in `keos-core`) now get
  a backup of their own, as `<name>-live` or `<name>-<namespace>`.
* **Every workload of a multi-workload app keeps its manifest.** `deployment.yaml` is still the
  first workload; the others (genai-ui, virtualizer-ui, ...) are written as
  `workload.<kind>.<name>.yaml` instead of leaving only an env file. `apps diff --drift` compares
  the `.spec` of each workload present in both captures, matched by what the manifest says it is, so
  an image, replica, resource or probe change shows up.
* **A chart that won't template no longer costs the capture of its live workloads.** When
  `helm template` fails or renders nothing live, the backup captures the classified workloads
  directly and says so. Env-resolution warnings (a ConfigMap or Secret it couldn't read) are shown
  instead of silently producing `<unresolved:...>` values.
* **`apps migrate` says what replacing a patch loses.** Re-running it replaces the whole same-kind
  patch; the paths only the existing patch set (a hand edit, a value the diff can't see) are now
  listed as a warning, which `--yes` stops on unless `--accept-warnings`. So is an obsolete patch.
* **`doctor` checks exclude paths.** Every `spec.values.*` path a chart type excludes must exist in
  its chart's `values.yaml` (non-blocking warning): a path that doesn't exclude nothing. Rocket's
  governance URLs were excluded under `rocketCommon` while the chart defines them under
  `rocketServer`, so the legacy URLs were patched in; the seed is fixed. **A catalog already written
  by `config init` keeps the old paths: change `rocketCommon.settings.governanceIntegration` to
  `rocketServer.settings.governanceIntegration` in it.**
* **Object selection by kind.** When a Kustomization renders several objects with the app's name (a
  storage overlay's Secret next to the HelmRelease), a chart-mode app takes the HelmRelease, and a
  manifest-mode one a custom resource over a core object, rather than whichever comes first.
* **`--all` lets you skip an instance it can't place.** Every question it asks (which declared entry
  an undeclared one migrates into, which of several live objects is the real one, which catalog type
  an object is) now ends with a "skip it" choice. Picking it leaves that component out of the run
  with a warning, and `--all` carries on with the next; it is not counted as a failure. A blank
  answer still means "no answer" and stops the run unless `--continue-on-error`. Asking for a single
  app by name offers no skip.
* **`apps migrate --skip-prepare` patches the tenant file without running any prepare step.** Meant
  for `--all`: nothing is run, shown, asked or backed up for the steps, each skipped step is
  warned about with the command that finishes it, and the run ends with a list of every app that
  still needs its step. Running `apps migrate <app>` later finds the patch up to date and goes
  straight to the step.
* **`doctor` compares the repository checkouts with what the cluster runs.** `apps diff`/`migrate`
  render from the checkouts, so one on another branch or commit than the cluster's GitRepository
  renders templates the cluster doesn't run (a Kustomization named `apps-genai-litellm` where the
  cluster has `apps-litellm`). A mismatch is a warning naming both revisions, and a worktree of the
  same repository already at the cluster's revision, if there is one, to point `repos` at.
* **A patch lands on the tenant entry the app was resolved to, not one named after its
  Kustomization.** Where a template pins a Kustomization to a fixed name (`apps-litellm`) but the
  entry keeps its own (`genai-litellm`), migrate used to fail with "no components.<key> entry named
  litellm"; the `--all` ordering and the dependency check were also looking under the wrong name.
* **Ctrl-C works at a prompt again.** The first Ctrl-C cancels in-flight cluster calls and says so;
  a second one quits immediately, instead of being swallowed while a prompt waits on stdin.
* **`config init --dir ./cfg --force` keeps the repo paths it carries over** (a relative `--dir` no
  longer turns `charts: ../charts` into a path relative to the wrong directory). Two captures of an
  app within one second (a prepare step's backup right after a backup) no longer collide, and the
  error after a failed prepare step names the `--baseline latest` re-run for when the step already
  removed the live workload.
* **Smaller fixes.** A value that merely starts with `<` (XML) is no longer mistaken for a
  placeholder and skipped from the patch; an unquoted number in a chart's env `value:` is compared
  as that number, not `""`; `prepare-genai`'s UPDATE only rewrites rows holding the old name.
* **Secret values never leave the cluster.** A variable read from a Secret is now a
  `<secret:NAME/KEY>` placeholder everywhere: in diffs, warnings, backups and patches. A legacy value
  read from a Secret where the chart now renders a plain value is listed for review instead of
  patched. Backups are readable only by their owner (0700/0600). Backups taken before this change
  still hold plaintext values and should be removed.
* **`prepare-genai` asks before running its SQL.** It shows the SQL and the target pod first, runs
  `psql` with `ON_ERROR_STOP` in a single transaction, then asks again over the real output.
  `--yes` answers neither question.
* **Destructive prepare steps run only after the patch is saved, and only after a backup.** If the
  step then fails, the tenant file already carries the patch; running `apps migrate` again finishes
  the step.
* **Patches no longer change values they meant to keep.** Zero-padded numbers (`0022`) and
  `True`/`TRUE` stay strings. Only `quote`/`squote`/`toString`/`default` pipelines map an env var to
  its `.Values` path. The patched chart is rendered again, and a value the patch doesn't reproduce
  is taken back out and listed for review. Excluded paths are now dropped from inside an op on a
  parent path too. A reordered or shortened list is replaced whole, never merged by index. A
  `valuesFrom` HelmRelease is refused.
* **`apps migrate --all` is stricter.**
  * An object two catalog types select is asked about once, not migrated twice.
  * An app whose dependency failed or was declined is skipped.
  * A declined prompt counts as not migrated.
  * `--yes` stops on an app with warnings unless `--accept-warnings` is given.
* **Reads that fail stop the command instead of reading as "nothing there".**
  * `tenant import` fails on a list it isn't allowed to make, instead of writing placeholder
    entries.
  * Discovery warns about kinds it may not list.
  * A sibling workload that can't be read fails the diff.
* **Smaller fixes.**
  * `envFrom` prefixes are applied on the live side.
  * Multi-line env values survive a backup.
  * A half-written backup is never picked as `latest`.
  * Saving the tenant file keeps its mode and symlink, and refuses if the file changed since it was
    read.
  * Piped answers reach every prompt.
  * Ctrl-C and timeouts stop cluster calls and subprocesses.
  * `prepare-datamarket-agent` scales down what the legacy HelmRelease rendered.
  * `config init --force` backs up what it replaces and keeps repo overrides.
  * `~` and relative paths in the environment file resolve correctly.
  * `tenant import` applies the same tenant-ownership rules as `apps`.

* **Chart-mode patches carry legacy values that the chart's ConfigMaps can't.** Three cases showed up
  in rocket's post-migration drift:
  * **Inline container env.** A variable a container sets in its own `env` (a keos-apps size
    overlay's `controllers.<c>.containers.<k>.env`) overrides the ConfigMap. It was patched through
    the ConfigMap's `.Values` path anyway, so the patch had no effect (rocket's
    `SPARTA_BOOTSTRAP_*` sizing). It's now patched in that env: a list is written whole with the
    legacy values, since a patch replaces lists, and a map gets just its keys. An inline value no
    values env renders is listed for review (`set inline in the container env`).
  * **Shared `.Values` paths.** Patching a path for one variable silently changed another variable
    that already matched through it (`cluster.domain` feeds both `KERBEROS_REALM_NAME`, legacy
    `EOSDEV.INT`, and `PEKKO_DISCOVERY_KUBERNETES_POD_DOMAIN`, legacy `eosdev.int`). The other
    variable is now pinned to its legacy value in the container's env, which the chart's ConfigMaps
    can't override. Where no single container env can carry the pin, it's reported as a conflict.
  * **Large integers.** They're written as strings. helm-controller passes values to Helm as JSON,
    and a number of a million or more rendered in exponent form
    (`SPARTA_PLUGIN_FACADE_CACHE_SIZE=1e+06`).

  The chart's values are now its `values.yaml` defaults under the HelmRelease's values. With a
  flat baseline, differences the patch settles are also dropped from review.
* **`apps migrate` warns about dependencies the tenant file can't satisfy.** An entry whose
  `config.dependencies.<key>.name` names no `components.<key>` entry renders a `dependsOn` on a
  Kustomization that never exists, and Flux holds the app back forever. rocket's hand-written
  `dgAgent: dg-agent` blocked `apps-rocket` on `apps-dg-agent` when the tenant's agent is
  `dg-hdfs-agent`. Migrate now names each such dependency and the entries that are declared.
  Dependency keys that aren't component keys (`governancePostgres`) aren't checked.
* **`apps migrate` stops on its warnings before showing the patch.** Warnings (unmapped
  differences, dropped live variables, unresolved dependencies, a Flux-managed live object) scrolled
  away behind a long patch. Migrate now asks to continue first. `--dry-run` and `--yes` never ask.
* **Review no longer lists differences the patch already settles.** A variable built from several
  templates has no single `.Values` path. rocket's `ROCKET_API_DOCKER_IMAGE` is
  `<registry>/rocket-api:<image tag>`, and it was listed for manual review even though the patch
  sets the image tag (through `ROCKET_VERSION`). The chart is now rendered again with the patch
  applied, and a difference that render already settles is dropped.
* **Chart-mode diffs match env vars per workload.** A chart rendering sibling workloads (genai's
  genai-api/genai-ui/genai-developer-proxy) had every workload's variables merged by name, so a value
  from one sibling could be patched into another's `.Values` path. `apps migrate genai` wrote
  genai-api's Vault role into `genaiUi.general.identity.approlename`, and genai-ui crash-looped.
  (The Python client had the same flat maps; its filesystem-order tie-break just happened to favour
  genai-api where Go's lexical one didn't.) Now:
  * each live workload is compared with its own rendered workload, and each value is patched through
    the `.Values` path in the chart file its rendered ConfigMap was built from;
  * anything that can't be attributed to one path is listed for manual review, never guessed, in
    `apps diff`/`apps migrate`. Those commands also name rendered workloads with nothing live (or
    in the backup) to compare, and count live variables the chart drops. Previously these were
    computed but never shown, and a diff with only unmapped differences reported "no differences";
  * backups also write `env-vars.<kind>.<name>.env` per live workload, which `--baseline` and
    drift checks compare when present. Older backups keep working through `env-vars.env`, with
    shared names reported as ambiguous;
  * the seeded genai type also excludes `genaiApi`'s and `genaiDeveloperProxy`'s
    `general.identity.approlename`, like the other chart components, so every genai workload uses
    the chart's GitOps Vault role.
  * new catalog field `chart.siblings` declares a chart's other workloads that legacy CCT deployed
    as separate apps. They join the anchor's instance, so backups of a CCT install capture them and
    `--baseline`/`--drift` compare each with its own rendered workload. It's seeded for genai
    (`genai-ui`, `genai-developer-proxy`) and virtualizer (`virtualizer-monitor`,
    `virtualizer-ui`), whose siblings `apps backup --all` no longer captures as separate apps.
  * an already-migrated chart app resolves from its HelmRelease, by any workload it renders or by
    the HelmRelease's own name:
    * `apps diff genai-ui` finds genai with its tenant entry inferred, instead of asking for it;
    * its backup and drift captures go through that HelmRelease, found from the workload's Helm
      labels, so every workload it renders is captured, not just the named one;
    * a sibling that joins no instance is only warned about while working on its type, as a legacy
      leftover when its type's HelmRelease doesn't render it.
* **Config split into a typed component catalog and an environment file.** `config init` now
  writes `~/.fluxcd/flux-stratio/catalog.yaml` and `environment.yaml` (`--dir`, `--force`)
  instead of one `config.yaml` whose flat `apps:` list mixed static coordinates with
  per-environment instance values:
  * `catalog.yaml` holds 19 **component types** — static facts only (`component`, `rset`, `chart`,
    `exclude`, `prepare`), `entry`/`object`/`kustomization` name templates, and `match` selectors
    (`kinds` + label/annotation selectors) that recognize a type's live legacy objects. Every
    seeded selector comes from the CCT `application_service`/`application_model` annotations on
    real captured legacy objects. `renamed`/`previousNamespace` are gone: the live object's own
    name and namespace are used.
  * `environment.yaml` holds `base`/`repos`/`cluster`/`tenant` (`--env-config`,
    `$FLUX_STRATIO_ENV`); `--base/--repo/--cluster/--tenant` still override it.
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
  `--dry-run`/`--yes` gate as the migration itself; `prepare-genai` (a Postgres data rewrite no
  Kubernetes API can verify) finds the tenant's PgCluster primary pod by label and runs the SQL
  there itself (via a new `kubeclient.Execer`, a `client-go/tools/remotecommand` wrapper — the Go
  equivalent of `kubectl exec`, replacing a manual `kubectl exec -it <pod> -- psql` session), and
  shows the real captured output — but always asks its own separate confirmation before proceeding,
  never skipped by `--yes`. The step is declared as a `prepare.DBQuery` (namespace, pod selector,
  container, command, SQL), a shape any future component's own manual DB-rewrite step can reuse.
  Fix (found by running it for real for the first time, against eosdev): the SQL targeted the
  wrong database (`psql`, the cluster's own database, instead of genai's own `genai` PgDatabase),
  and its `genai-gateway` DELETE assumed that deprecated (now litellm-superseded) component is
  always present — it's now guarded with `to_regclass(...) IS NOT NULL`, a no-op wherever genai has
  already moved to litellm (or never had genai-gateway).
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
  differ by that patch (the base is rendered without it), so meld shows what it covers; the desired
  side is then labelled `desired state without tenant patch`, and the message reads "nothing to
  migrate: the tenant file's existing patch already covers every difference" instead of the
  misleading "no differences".
* A chart-mode capture (`apps backup`, `apps diff --drift`) that finds none of the chart's workloads
  live now says why: the "declares no live workloads" warning names the workloads it looked for, the
  chart directory it rendered, and the chart version the release runs — the usual cause is a
  charts repository checkout that isn't that version and names its workloads differently. `--drift`
  against a workload (`env-vars.env`) backup then points at that instead of reporting a shape
  change, and every shape-mismatch error lists the files the backup actually has.
* **Every repository's checkout is configurable on its own.** `environment.yaml` gains an optional
  `repos:` map (and a repeatable `--repo <name>=<path>` flag, on every command and on `config
  init`) pointing any of `keos-apps`, `keos-use-cases`, `keos-fleet`, `keos-system-services` and
  `charts` straight at its checkout — a git worktree, or a clone under another name — while the
  rest stay at `<base>/<name>`; `base` is optional when `repos` lists all five. Catalog
  `chart.path` is now relative to the charts repository's root (`litellm`, not `charts/litellm`),
  so it describes the chart rather than the directory layout. **Breaking:** `chartsBase` and
  `config init --charts` are gone — an environment file still setting `chartsBase` fails saying to
  set `repos.charts: <chartsBase>/charts`, and a catalog `chart.path` starting with `charts/` fails
  pointing at `config init --force`.
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
    (the charts repository + `chartPath`) actually exists, instead of only the four GitOps
    repos — a misconfigured charts checkout or `chartPath` is now caught up front, not mid-run.
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
