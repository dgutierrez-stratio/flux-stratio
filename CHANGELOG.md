All notable changes to this project will be documented in this file.

## 0.1.0-SNAPSHOT

* **`tenant import` decides `enabled` for every optional dependency of every component, and asks, instead of
  doing it only for rocket and intelligence.** On eosdev the `genai` entry came out with
  `dependencies.discovery: {name: discovery}` and no `enabled`: the ResourceSet defaults it to false, the chart
  rendered `DISCOVERY_DATABASE: ""` and the GenAI chat could not list data domains, with nothing in the import
  output. `enabled` was written only for the dependencies a hand-written `chart.uses` rule named, seeded for two
  types. Now the keos-use-cases templates say which dependency of which component is switched by `enabled`
  (`catalog.Dependency.Gated`, plus `Adds`: what turning it on also changes, read from the template), so every
  component is covered and a new one or a new gate needs no code. When such a dependency runs on the cluster, the
  command proposes `enabled: true` and asks, saying what it changes; "leave it off" is written as `enabled: false` so
  it is not asked again; a value already in the file is never asked about or changed. `--yes`/`-y` accepts every
  proposal and reports each; with no terminal, or `--dry-run`, a proposal is left undecided and reported; a gated
  dependency that does not run is reported as not running. `flux stratio doctor` warns about a gated dependency no
  catalog type produces, and a new test checks the real templates against the seed.
  **Behaviour change:** `chart.uses` is deprecated and ignored (an old `catalog.yaml` still loads; `tenant import`
  and `doctor` warn), and rocket and intelligence are asked like every other component instead of getting an
  automatic `enabled: true` from their HelmRelease values; the old "the value says off" evidence is gone by design.
  `tenantfile.DependencyRef.Enabled` is now a `*bool` so `false` can be written. The Discovery database id is still
  not scaffolded (the ResourceSets have no input for it): the chart default `1` applies.
* **`prepare-gosec-agent` scales the legacy gosec agents to 0, so they stop interfering with their replacements.** The
  legacy `opensearch1-agent` and the new `opensearch1-gosec-agent` deleted each other's monitoring role every ~14.5 s
  (the legacy image names it `metrics` unless `OPENSEARCH_METRICS_USER` is set, the chart sets `prometheus`; each agent
  deletes every role it doesn't whitelist, and its own metrics role is the only one it does), and the Postgres pair raced
  on the same `REVOKE` sweeps. Component-scoped (`postgres-gosec-agent` and `opensearch-gosec-agent` only, never a
  catalog-wide default), scale only (the Deployment is kept, the plan shows the old replica count for the rollback),
  keos-core agents untouched. Context: `docs/TASK-16-prepare-gosec-agent-scale-legacy-to-zero.md`.
* **Prepare steps delete the legacy Deployment of the apps whose chart changes its immutable selector, and the `[ACTION]`
  that announced it stops misfiring.** On eosdev `dg-datarest-pgi`, `eureka-agent`, `genai-api`, `genai-ui`, `rocket` and
  `intelligence` each printed `[ACTION] 1 workload(s) can't be updated in place` and the operator deleted the Deployment
  by hand (a step existed for two of them, which left it). Chosen app by app (never a catalog-wide default or flag):
  * `prepare-eureka-agent` and `prepare-intelligence` delete the one legacy Deployment (intelligence keeps its legacy
    Ingress); `prepare-rocket` deletes `rocket` and, listed in the plan, the four Deployments its server created
    (derived only from the legacy parent found in this run, so a re-run after the cutover plans nothing;
    `rocket-catalog-standalone`, Ingresses and Services stay); `prepare-datarest` now also deletes the Deployment after
    the Ingress.
  * `prepare-genai` now takes a backup before its SQL (a Query step never got one: `ensurePreparedQuery` was not handed
    `backupFirst`), copies the rows the SQL changes to `<backup>/prepare/genai-<tenant>-{chain,endpoint}.sql` and stops
    before any statement if that fails, then deletes the legacy `genai-api`/`genai-ui`/`genai-developer-proxy`
    Deployments. A read-only `SELECT`, also under `--dry-run`, counts the rows left to rewrite so a re-run asks nothing.
  * The selector `[ACTION]` fires only for a live workload with the same kind, namespace and name as the rendered one: it
    was also printed for `governance-datamarket-agent`, the two gosec agents and `connectors`, where the legacy workload
    has another name or namespace and nothing clashes. Those get a `[NOTE]` that the legacy workload keeps running next to
    the new one; a real clash names the legacy `namespace/name` and says what to do by the app's step. The `connectors`
    catalog notes claimed a same-name clash that doesn't exist.
  * "already satisfied" is now printed at the normal log level.
  Context: `docs/TASK-15-prepare-steps-for-immutable-selector-apps.md`.
* **One null value in a rendered ConfigMap no longer makes `apps diff`/`apps migrate` read the whole ConfigMap as empty.**
  The plugin read rendered ConfigMap `data` with `unstructured.NestedStringMap`, which errors at the first non-string
  value and returns an empty map, and both call sites discarded the error. genai's HelmRelease renders
  `genaiLitellmModel: null` (an empty `${LLM_MODEL_*}` substitution becomes a YAML null even when quoted), so
  `genai-ui-config` came out with null keys and all 119 `genai-ui` variables were reported as "not rendered by the chart",
  while the patch lacked 13 real legacy `genaiUi.*` values (`haProxyHost`, `serviceUrl`, the opensearch and discovery
  settings, ...). Rendered data is now read the way the API server decodes it: a null is `""`, a bool or number is its
  text (`internal/diff/stringmap.go`). A null is reported once per ConfigMap as a warning naming its keys (the usual
  cause is an unquoted, empty `${VAR}`; it never stops `--yes`), and a map or list as a value, which the API server
  rejects, makes the diff fail naming the ConfigMap. The same read replaces `NestedStringMap` for the rendered
  selector and pod-template labels. On eosdev `apps migrate genai --dry-run` goes from 120 to 1 "lost" variable
  (`genai-api/NOT_USED`) and the patch carries the `genaiUi.*` values. Context: `docs/TASK-10-diff-configmap-null-values.md` (removed once done; recover it from git history, commit b879e75).
* **`apps diff`/`apps migrate` say why a legacy variable isn't rendered, and stop calling probe variables lost.** The
  Command Center injected a descriptor's probe settings as `{LIVENESS,READINESS,STARTUP}_PROBE_*` environment
  variables, and a chart sets them on the container, so on eosdev rocket (9 variables) and both gosec agents (8 each)
  were reported as "values are lost after migration" although the chart carries them. A probe variable whose rendered
  workload has the same value in the matching probe field is now listed apart, as `carried natively by the chart` (a
  note, not a warning, so it no longer stops `--yes`); one whose chart probe differs stays in the lost list and its WHY
  names the chart's value (`the chart sets readinessProbe.failureThreshold natively, to 1 (live 3)`). `NOT_USED`, a
  placeholder name shared by unrelated descriptor parameters, gets its own WHY. (`internal/diff/nativeprobe.go`,
  `ChartDiffResult.Native`.) Context: `docs/MIGRATION-HISTORY.md` section 4.
* **`tenant import` scaffolds a legacy tenant file that validates.** On a cluster where the tenant's apps are still the
  CCT deployment, the import found only the components whose name equals a chart's or that have a custom resource, and
  the validation then failed (`apps-psql waits on stratio-datastores/apps-`, ...). Fixed at the source, in four parts:
  * *Discovery goes through the component catalog.* The live workloads of the tenant's namespaces are classified by
    what CCT stamped on them (`internal/components`, the classifier `apps` uses) and each becomes the entry its type's
    `entry` template names, so `connectors-management` is `connectors`, `genai-litellm` stays `genai-litellm`,
    a datamarket agent `governance-datamarket-agent`, and the three legacy governance agents are found. The
    Deployment-name heuristic is gone, as is its namespace-suffix fallback. A new seeded type, `connectors`, covers
    `connectors-management` (20 types); `doctor` now warns when `catalog.yaml` lacks a type the seed has.
  * *`configFromEnv`* (new catalog field, seeded on `dg-agent` and `bdl-datarest`) derives entry config from a legacy workload's env: the
    agent's `type` from `CUSTOM_SERVICE_EXTRACTOR_TYPE` (`HDFS`/`S3`; without it the ResourceSet would render the S3
    overlay under `dg-hdfs-agent`) and its governance pgbouncer/postgres from `DATASTORE_HOST`/`DATASTORE_DATABASE`. An
    agent whose value has no GitOps overlay (the legacy `POSTGRESQL` ones) is left out, with a warning, instead of
    imported as something else; any config field the ResourceSet reads can be derived too (datarest's `postgresDatabase` from
    `JDBC_DATABASE`, which the template defaulted to `governance` while the legacy app is connected to `data`), and an existing
    value that differs from the legacy app's is kept with a warning. Among several instances of a dependency, the one of the dependent's storage type is chosen;
    a guess is warned about with every candidate, also for new entries (it was silent).
  * *Complete resolves dependencies against the file too.* A component added by the run used to be written without
    the dependencies the scan could not resolve, even when the file declared the target (hand-added `connectors`,
    `dgAgent`, ...), so the same `apps-` waits remained. Names now come from the scan and the file together. A
    possible-rename warning no longer fires for several live instances added in one run, and a failed entry no longer
    leaves its lines in the report.
  * *`enabled` and GitOps-only components.* With no HelmRelease to read, a dependency whose component runs on the cluster
    is written `enabled: true`, saying it was assumed (rocket's `virtualizer` decides whether its postrequisites are
    rendered, which `apps-use-case-postrequisites-*` waits on); an operator's `false` is kept. A component only GitOps
    has that something waits on unconditionally (postgres' `pgbackuprepository`) is added with a warning that applying
    it creates it.

* **`tenant import` completes a tenant file whose `components` is empty.** A file declaring `components: []` (or `{}`, or
  a bare `components:`, with or without a trailing comment) failed with `spec.defaultValues.components is not a block
  mapping with entries`, and one with no `components` key at all with `tenant file has no spec.defaultValues.components`.
  The empty value is now rewritten to a bare `components:` (the one line `tenant import` ever changes) and the scanned
  entries are added under it in the same style as a freshly rendered file; a missing key is added at the end of
  `spec.defaultValues`. A value spread over several lines, or a non-empty flow mapping, still asks to be edited by hand.
  Two related bugs fixed on the way: several new entries under the same component key in one run produced a duplicate
  `<key>:` line (invalid YAML), and an addition to an existing entry could land under a new key block recorded before
  it; and a file without a trailing newline got a blank line before every addition at its last line.

* **`tenant import` scaffolds genai's `llmModels` block.** The schema scanner now records the keys a template reads
  off a nested config variable (`$llmModels := get $componentConfig "llmModels"`, then `get $llmModels "chat"`), one
  level deep. A required block with such keys is written with them empty (`chat`, `governance`, `translate`) instead
  of being left out, with a warning to fill them in; a re-run never edits inside the block but keeps warning about
  each key still missing or empty. Optional blocks (`agent`, `backup`) are unchanged, and litellm's required `models`
  list is still only warned about: its item shape isn't in the template.

* **`prepare-litellm` keeps the legacy LiteLLM Vault passwords.** The chart's SecretsBundle declares `db-salt-key`,
  `keos-key-secret` and `master-api-key` (the secrets operator rejects underscores) and deletes every entry it doesn't
  declare, so a migrated release got fresh random values: the provider credentials LiteLLM stored encrypted with the
  legacy salt stopped decrypting ("Did your master_key/salt key change recently?") and every key derived from the
  legacy `keos_key_secret` (genai-api's, admin's) answered 401 `token_not_found_in_db`. Before deleting the legacy
  Deployment, the step now copies each legacy value (`db_salt_key`, `keos_key_secret`, `master_api_key` in
  `userland/passwords/<live name>.<ns>/`) to the chart's name, and keeps a verbatim copy of the original under
  `legacy/` (the bundle never deletes subfolders). It never writes or deletes the originals. Everything is decided
  read-only in the plan: a chart entry that already holds another value (moving would rotate a live secret) or a
  legacy value of another length than the chart declares stops the step before any write; originals already gone
  only warn; `userland/` must be kv v1. It is idempotent like the rest of `apps migrate`: a re-run plans only what
  doesn't hold yet (a run interrupted after its backups is completed from them), each Vault write re-reads its target
  and refuses one that changed since the plan (the chart applied while the prompt waited), and after the cutover (no
  legacy Deployment, the release's SecretsBundle exists) it doesn't reach Vault at all. New package `internal/vault`
  reaches Vault the operator's way (root token from `keos-core/vault-unseal-keys`, vault CLI by pod exec, token on
  stdin, values redacted in any output, no delete), and automated prepare steps now get the pod exec.
* **`doctor` checks Vault access** (`vault access`): for each legacy app whose prepare step moves Vault passwords
  (LiteLLM), it makes the same read-only reads the step starts with (`prepare.InspectPasswords`: kv v1 mount, the
  names in the app's passwords folder and its `legacy/` backups; no value), and says what the step would do. Required
  when such an app is on the cluster; skipped, without touching Vault, when none is.

* **`tenant import` stops warning about config fields that have a default or don't apply.** Two gaps in the schema
  scanner. (1) Only a quoted `| default "x"` counted as a default, so `| default false` (postgres `recoveryEnabled`,
  genai `governanceOpenSearchEnabled`) was reported as "no default" although the template has one: a bare
  `false`/`true`/number is now a typed default and is scaffolded as such, and `| default dict` / `| default list`
  (postgres/opensearch `agent`, opensearch `backup`) marks an optional sub-config that is neither scaffolded nor
  warned about. To keep the warning where the chart needs the content (genai `llmModels`, litellm `models`), the
  catalog type has a new `requiredConfig` list, seeded on both (an older `catalog.yaml` inherits it). (2) A field read
  only inside `if eq $storageType "local"` (pgbackuprepository `storageClassName`) is now skipped for entries of
  another storage type instead of being reported. The first Complete after upgrading adds the explicit defaults
  (`recoveryEnabled: false`, `governanceOpenSearchEnabled: false`) the templates already assumed.
* **`tenant import` validates the tenant file it produces.** New package `internal/tenantcheck`, run after every import
  (Create, Complete and, on the proposed result, `--dry-run`): the file is a `ResourceSetInputProvider` with the
  `tenant-config` label, `tenantName`/`size` are sane, every entry has a unique name, every dependency has a name and
  points at an entry the file declares (the app would otherwise wait on `apps-<name>` forever), `enabled` is a boolean,
  and every `resourceset-apps-*.yaml` of keos-use-cases renders against it with `flux-operator build rset` with no
  tenant `apps-*` Kustomization waited on and not rendered. The file is still written; problems are listed and the
  command exits non-zero. `--no-validate` skips it; without `flux-operator` the render step is skipped with a note.
  An empty or undeclared dependency name is an Error only when the dependency has `enabled: true`; otherwise it is a
  warning, since a switched-off dependency blocks nothing and the render check fails on any real dangling wait (a fresh
  import, which leaves unresolved names empty, therefore exits 0). `uses` evidence ignores string spellings of off
  (`"False"`, `"0"`, `"no"`, `"off"`).
* **`tenant import` writes `dependencies.<dep>.enabled: true` for the integrations an app already uses.** It wrote only
  `{name: ...}`, and the ResourceSets read `enabled` (default false): a migrated rocket or intelligence got
  `genai.enabled: false`, so the prerequisites job skipped the genai role assignment and genai-api answered 403 to
  chain registration every five minutes. The new catalog field `chart.uses` (`{dependency, values}`, seeded on `rocket`
  and `intelligence`; an older `catalog.yaml` inherits the seed's rules) names the live HelmRelease values that prove
  the use. Create and Complete both write `enabled: true` on that evidence and report the value; a hand-written
  `enabled: false` stays, with a warning; an app with no HelmRelease yet is warned about, never guessed. New
  `tenantfile.AddToDependency` adds the line inside an existing block-style dependency. The `add-catalog-component`
  skill now investigates `chart.enabledBy` and `chart.uses` for a new component.
* **`prepare-litellm`: the legacy LiteLLM Deployment is replaced by `apps migrate`.** Same problem and same helper as
  `prepare-discovery` and `prepare-dg-agent` (`replaceLegacyDeployment`): the chart changes the Deployment's immutable
  selector under the legacy name `genai-litellm`. It is found by CCT's app id, never a Flux-managed one, asks like every
  prepare step (no skips only that step) and plans nothing once Flux owns the Deployment. The legacy ingress stays: its
  host (`genai.<domain>`) is not the chart's. A `catalog.yaml` seeded before it needs `config init --force`.
* **The console diff says what it compares and where.** `apps diff`'s default view (and the preview of `apps migrate`
  and `tenant import --dry-run`) was a bare `diff -u`: nothing said which side was which, the `@@ -171,7 +171,7 @@`
  headers were line numbers, three lines of unrelated neighbouring variables surrounded every change, and it was
  plain text. Now: (1) a header names the sides — `--- desired state (tenant file as rendered)` / `+++ live cluster`
  (or `backup`, `backup`/`live now` for `--drift`, `tenant file (current)`/`(with the change)` for a tenant-file
  edit); (2) on a terminal that hasn't opted out (`NO_COLOR`), `-` is red, `+` green and headers cyan, and plain text
  everywhere else, piped or redirected; (3) for a chart a header `@@ virtualizer-monitor @@` names the workload where
  it changes and the context lines are dropped, and for a manifest each hunk is headed with the YAML keys around its
  first change (`@@ -20,8 +20,7 @@ backup.pgbackrest`); (4) within a removed/added pair for the same key, the part of
  the value that differs is highlighted (whole words, `false`/`true`; a lone `_` for `-` stays narrow). Plain text
  carries the same lines as before. `--view patch` and `--view meld` are unchanged.
* **`prepare-discovery`: the legacy Discovery Deployment is replaced by `apps migrate`.** Its chart changes the
  Deployment's immutable selector under the same name, so Flux couldn't update it in place. The step deletes it
  (found by CCT's app id, never a Flux-managed one, nothing planned on a re-run) and asks like every prepare step; no
  skips only that step. Its legacy ingress stays: its host and path differ from the chart's, so nothing collides.
  `prepare-dg-agent` and `prepare-discovery` now share one helper, `replaceLegacyDeployment`, the pattern for the
  next app with only this problem. A `catalog.yaml` seeded before it needs `config init --force` (`doctor` says so).
* **`apps diff --view meld` opens one tab per resource.** A chart-mode app (`dg-hdfs-agent`, `virtualizer`, genai, …)
  used to open a single meld comparison with every workload's env vars and then every workload's spec one after the
  other. It now opens one meld window with a tab for every resource — `env <workload>` and, with `--drift`, `spec
  <Kind>/<name>` — the environment variables first and the specs after, and in each group what differs before the identical
  ones, named `… (identical)`: a resource left out of the window can't be told from a comparison that was lost. Each side's file is named after its label and its resource (`backup-env web.yaml` against
  `live now-env web.yaml`; the labels are `desired state`, `live cluster`, `backup`, `live now`), as the single
  comparison's files always were, so the tab and the panes say which side is which and what they hold. An app of one resource (a
  CR, a single workload) opens as before. The unified diff is unchanged.
* **Prepare steps are laid out like the review.** What a step has to say now uses the same `[ACTION]` sections as an
  app's review — badge, title, hint, aligned table, colour, summary line — instead of free-form `►`/`⚠️` lines: the
  operations a step would perform (`OBJECT · ACTION · KIND · NAMESPACE`), a manual step's instructions and a query
  step's SQL (shown verbatim, never cut to fit), a step that changed the cluster (commit and push), a step that was
  not run (declined or `--skip-prepare`), and the end-of-run table of apps that still need theirs (`APP · STEP ·
  RUN`). The hint says what answering n does, or that the step runs without asking under `--yes`, or that a dry
  run asks nothing. Progress lines (`backed up`, `1/2 delete …`, `complete`) stay narrated like any command's.
* **Prepare steps list their operations and no longer print whole manifests.** `apps migrate` showed, after the
  list of what a step would do, the full YAML of every object it acts on. It now lists only the operations
  (`delete Deployment <ns>/<name>`) and a one-line hint to inspect one with `kubectl get … -o yaml`. Nothing goes to
  stdout from a prepare step any more except the real output of `prepare-genai`'s SQL.
* **Declining a prepare step skips that step, not the migration; and `dg-hdfs-agent` gets one.** Every prepare
  prompt now says first what answering no does, and no (or a blank line) leaves that one step out: the patch is
  still written, a warning says what is left undone and how to finish it (by hand, or `apps migrate <app>` again),
  and the step is listed again in the end-of-run summary — the same path as `--skip-prepare`, which skips every
  step without asking. Before, a no aborted the app (`prepare step … not confirmed; … not migrated`). `--yes`
  still answers the automated steps' question, never `prepare-genai`'s; only that step's second question, asked
  after the SQL ran, remains a failure. The new `prepare-dg-agent` step deletes the legacy governance agent
  Deployment (the chart changes its immutable selector), found by CCT's app id like `prepare-dlc`; with it, the
  selector difference of `dg-hdfs-agent` is a note instead of an action to take by hand. A `catalog.yaml` seeded
  before it needs `config init --force` (`doctor` says so).
* **`apps diff` and `apps migrate` lay their warnings out by what to do about them.** A review used to be a run of
  `⚠️` lines, one per finding, with long values wrapped and different kinds mixed. It is now a few sections of
  one shape — a badge and title with a count, a hint, and one aligned table — ordered so what needs action sits
  last, next to the question that follows, ending in a one-line count: `[NOTE]` (nothing to do: carried
  resources, catalog exclusions, benign settings), `[REVIEW]` (a value the patch leaves as GitOps renders it, a
  variable only live has, Flux already managing the object, a size or dependency mismatch) and `[ACTION]` (an
  immutable selector, a variable asking for more memory than its container's limit). Variables are listed as
  NAME / GITOPS / LIVE, with values cut down to what differs (`…keos-core:5432/postgreskeos…` against
  `…keos-core/postgreskeos…`). Width follows the terminal, and colour is used only on one (never with
  `NO_COLOR`). Notes are one line saying what there is. `-v` adds to the same layout, never changes it: it lists
  the notes and adds a **WHY** column to the tables of variables, saying why each one is there (below). Reviews
  and actions are always shown: they gate `--yes`.
* **A variable "with no .Values path" now says why, with `-v`.** The chart's own line for it is read: **fixed in
  the chart** (written out, nothing sets it), **follows the release's name or namespace** (changes only with the
  HelmRelease's name or namespace), or **built from** the values listed (no single value sets it; the chart's
  helpers are followed, even those of a library chart taken from a local directory). The old hint, "the chart
  has no value that sets them", was wrong for the last kind.
* **`prepare-virtualizer`: the virtualizer's legacy Deployments are replaced by `apps migrate`, not by hand.** The chart
  renders the server, `virtualizer-monitor` and `virtualizer-ui` as one release, each under a new immutable
  selector and the legacy Deployments' names: an upgrade meeting one fails, and Flux's rollback then fails too
  (`original object Job … prerequisites-N not found`; the chart names that Job after the release revision), which
  hides the real error. The new step deletes the family's legacy Deployments, found by the type's own `match` and
  `chart.siblings` selectors (new `config.App.Family`, `prepare.Options.IsLegacy`) rather than by one CCT app id, so
  it still finds the monitor and ui after the server was migrated; re-running it after the cutover plans nothing.
  Ingresses and Services are left alone. It sets `ReplacesWorkloads`, so the selector warnings become notes.
  `tenant import` warns, when it switches the siblings on (`uiEnabled: true`), to run `apps migrate` before
  pushing. `doctor` warns when `catalog.yaml` lacks a prepare step the seed declares: re-run `config init --force`
  to pick this one up.
* **`tenant import` completes an existing tenant file instead of refusing it.** With `--output` naming a file that
  exists, it adds only what the file lacks (config fields the component templates read, dependencies, the storage
  type, the top-level fields) and never removes, reorders, uncomments or overwrites anything: your values win (a
  disagreement with the cluster is a warning), commented-out component blocks stay commented out, and the lines
  added are the only difference in `git diff`. Additions are inserted into the file's text, not re-encoded, so
  indentation and comments survive byte for byte. Running it twice changes nothing. Components live on the cluster
  that the file doesn't declare are added as new entries, except under a commented-out block, which stays as it
  is (an entry makes Flux deploy the component, so read the result or use the new `--dry-run`, which prints the
  additions as a diff and writes nothing). `--force` still regenerates the file. Without a file, or with a new
  `--output`, it creates the skeleton as before.
* **`tenant import` no longer drops config fields silently.** A field a template reads that has no default
  (genai's `llmModels`, litellm's `models`, ...) is reported as a warning in both modes. The `hasKey ... else
  <literal>` form is recognised as a default, so virtualizer's `uiEnabled` is scaffolded (a bool, not a string),
  and it is `true` when the cluster runs the virtualizer's monitor or ui, legacy or already Helm-managed: left at
  its default next to them, the next release removed them. The knowledge of which key switches a chart's siblings
  on is the new catalog field `chart.enabledBy` (seeded on the virtualizer type; see `docs/config-reference.md`);
  a `catalog.yaml` seeded before the field existed inherits the seed's value for the same type, so it needs no
  re-seeding.
* **`--help` rewritten for every command.** Each command now says what it does and whether it writes anything,
  lists every mode and modifier (`apps diff`'s `--baseline`/`--drift`/`--view`, `apps migrate`'s
  `--dry-run`/`--yes`/`--accept-warnings`/`--skip-prepare`/`--all`/`--continue-on-error`, ...), and ends with
  examples; the root help gives the order of work. Flag descriptions are one line, usage lines read
  `flux stratio ...`, and a test keeps every command documented and every flag brief.
* **`apps diff` compares the tenant file as it is.** The desired side includes the app's patch when the file has
  one (as Flux applies it), so a migrated app reports "no differences" and a difference means something the patch
  leaves different from live; with no patch the output is what it always was. To compare against plain GitOps,
  comment the patch out; `apps migrate --dry-run` shows the whole patch it would write. `apps migrate` itself is
  unchanged: it still renders the pristine base so the patch it writes is always the whole one. There is no flag
  for any of this. An app that already has a patch no longer carries live resources into a diff's patch (that
  would hide what the patch leaves different), so they are reported as differences.
* **Chart-mode `apps diff` / `apps migrate` compare more than env vars.** Each live workload's container
  resources (requests/limits, compared as quantities), image and replicas are compared with the rendered
  workload, so a migration that changes them is no longer reported as "nothing to migrate". Risky
  differences (less memory than live had, a limit where live had none, a different image tag, replicas
  0 to N) are warnings that stop `migrate --yes` unless `--accept-warnings`; the rest are printed as
  information. `--baseline` reads the live side from the backup's workload manifests.
* **The patch carries the legacy container resources.** When the live resources differ from the GitOps size
  overlay's, they are written to `controllers.<controller>.containers.<container>.resources` (requests and
  limits together; a limit live never had is removed with `null`), and the chart is rendered again with
  the patch to check it reproduces them. A value the render doesn't reproduce is taken back out of the
  patch and stays a warning. Image tags, replicas, probes and selectors are never patched.
* **`apps diff` / `apps migrate` warn when an env var outgrows its container's memory limit.** `-Xmx` in
  any env value, and the type's `memoryEnv` variables (new catalog field, seeded for the virtualizer,
  bdl-datarest, eureka-agent and discovery types; see `docs/config-reference.md`), are checked against
  the limit of the chart as patched.
* **A selector that differs from the live workload's is a warning.** Deployment and StatefulSet selectors are
  immutable, so a chart selecting on `app.kubernetes.io/*` can't be applied over a legacy workload selecting on
  `cct.stratio.com/application_id`: the legacy workload has to be deleted first. Information only for an app whose
  prepare step does that (`prepare-dlc`).
* **They warn when the tenant `size` isn't the size the live app reports** (`CUSTOMER_SIZE`), and name the
  chart default env var an overlay's list hides (`chart default env X is shadowed by the list at
  controllers.<c>.containers.<c>.env`).
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
