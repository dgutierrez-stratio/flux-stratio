package cli

// The text `flux stratio ... --help` prints. Kept together so the whole CLI
// reads in one voice: say what the command does and whether it writes
// anything, then every choice the user has, then examples.

const rootLong = `flux stratio migrates the applications that Stratio Command Center (CCT)
installed on a cluster into GitOps. For each app it works out which values
the GitOps render does not reproduce and writes them as a patch into the
tenant file (keos-fleet: clusters/<cluster>/tenants/config/<tenant>.yaml), so
that once Flux reconciles the app it keeps its legacy configuration.

Order of work:

  1. config init     once per workstation: write the catalog and environment files
  2. doctor          check that everything is in place
  3. tenant import   first time only: scaffold the tenant file from the live cluster
  4. apps backup     save each app's legacy state BEFORE Flux reconciles it
  5. apps diff       preview what migrating an app would change
  6. apps migrate    write the app's patch into the tenant file
  7. you commit and push the tenant file; Flux does the rest

flux stratio never commits or pushes. It reads the cluster with your
kubeconfig and changes it only in "apps migrate"'s prepare steps (see
"apps migrate --help").

Two files configure it (written by "config init", see "config --help"):
the catalog ($FLUX_STRATIO_CONFIG) and the environment ($FLUX_STRATIO_ENV).
The global flags below override the environment for one run.`

const rootExample = `  flux stratio config init --base /stratio/gitops --cluster eosdev --tenant stratio
  flux stratio doctor
  flux stratio apps backup --catalog
  flux stratio apps diff psql
  flux stratio apps migrate psql --dry-run`

const configLong = `The two files flux-stratio reads on every run, in ~/.fluxcd/flux-stratio unless
--config / --env-config (or $FLUX_STRATIO_CONFIG / $FLUX_STRATIO_ENV) say
otherwise:

  catalog.yaml      what can be migrated: each supported component type and the
                    selectors that recognise its live CCT objects
  environment.yaml  where this workstation's checkouts are and which cluster and
                    tenant to operate on

Both are documented in docs/config-reference.md. Only "init" exists today.`

const configInitLong = `Writes catalog.yaml and environment.yaml into --dir (default
~/.fluxcd/flux-stratio):

  catalog.yaml      every supported component type with the selectors that
                    recognise its live CCT objects: the same for every
                    environment. Review it before use.
  environment.yaml  the base directory of the repositories, any per-repository
                    path, the cluster and the tenant: this workstation's target

The content of environment.yaml comes from the global flags: --base (or every
--repo), --cluster and --tenant are required, because there is no environment
file yet to read them from. --repo name=path is repeatable and may name
keos-apps, keos-use-cases, keos-fleet, keos-system-services or charts;
relative paths are stored absolute.

Existing files are never overwritten unless you pass --force, which first
copies each to <file>.bak-<timestamp> and keeps the old environment file's
--repo entries that this run does not replace.`

const configInitExample = `  flux stratio config init --base /stratio/gitops --cluster eosdev --tenant stratio
  flux stratio config init --force --base /stratio/gitops --cluster eosdev --tenant stratio \
      --repo charts=/path/to/charts-worktree --repo keos-apps=/path/to/keos-apps-worktree`

const doctorLong = `Checks that flux-stratio is ready to run and says what to fix. Read-only.

  binaries         flux-operator, flux, helm and kubectl (required); meld (optional)
  catalog          loads and validates; every exclude path exists in its chart
  environment      loads; repositories are where it says; chart and
                   kustomization paths exist
  cluster          reachable with your kubeconfig; the revision each repository
                   is checked out at against the one the cluster runs
  tenant file      <keos-fleet>/clusters/<cluster>/tenants/config/<tenant>.yaml exists

It exits with an error when a required check fails. Warnings (an exclude path
that excludes nothing, a checkout on another revision than the cluster's) are
printed but do not fail it. Use the global flags to check another cluster,
tenant or checkout.`

const doctorExample = `  flux stratio doctor
  flux stratio doctor --cluster eosdev --repo charts=/path/to/charts-worktree`

const tenantLong = `Commands that create tenant artifacts. Only "import" exists today.`

const tenantImportLong = `Scans the live, not-yet-migrated cluster and renders a skeleton of the tenant's
ResourceSetInputProvider (the tenant file): one components.<key> entry for
each app it recognises. Read-only on the cluster. Use it once, to create the
tenant file; afterwards "apps migrate" adds patches to it.

Prints to stdout, or writes --output. It will not overwrite an existing
--output unless you pass --force, and regenerating drops anything written by
hand (fields, comments, patches). --size sets the default size (S, M or L)
every component is rendered with (the keos-apps size overlays); it does not
change what the legacy apps were given.

The tenant is the global --tenant (or the environment file's).`

const tenantImportExample = `  flux stratio tenant import                                  # print the skeleton
  flux stratio tenant import --output stratio.yaml --size M   # write it, default size M
  flux stratio tenant import --output stratio.yaml --force    # replace an existing file`

const appsLong = `Back up, compare and migrate the apps CCT installed on the cluster.
<name> in these commands is the live object's name (psql, kafka1) or its
GitOps name (psql-gosec-agent); the type is detected from the catalog, and
--as <type>[/<entry>] answers the "which one?" question up front.

  backup    save an app's legacy state to disk (read-only on the cluster)
  diff      what would migrating change? / has a migrated app drifted? (read-only)
  migrate   write the app's patch into the tenant file (and run its prepare step)

Order: backup, then diff, then "migrate --dry-run", then migrate.`

const appsDiffLong = `Shows what migrating an app would change, or whether it has drifted.
Read-only: it never writes to the cluster or to the tenant file.

<name> is the live object's name (psql, kafka1) or its GitOps name
(psql-gosec-agent). If the type or tenant entry is ambiguous you are asked,
or answer up front with --as <type>[/<entry>].

What is compared (pick one mode):

  (default)      the GitOps desired state, rendered from the tenant file
                 WITHOUT its current patch for this app, against the live
                 cluster. The difference is the patch "apps migrate" writes.
  --baseline X   the same, against a backup instead of live. Use it when Flux
                 has already reset the live app (a warning tells you).
  --patched      the desired state WITH the tenant patch applied, as Flux
                 would apply it, against live: what does the migrated app
                 still differ in? (An app that already has a patch looks like
                 a big difference in the default view: closing it is the
                 patch's job.)
  --drift X      no GitOps rendering: live now against a backup. Has the app
                 changed since it was backed up?

X, for --baseline and --drift, is one of:
  latest                  the app's most recent backup
  <dir>/<app>             that app's backup directory (its latest capture)
  <dir>                   the backups root (finds <dir>/<app>)
  <dir>/<app>/<timestamp> one exact capture
("latest" is a keyword; --dir says where the backups root is.)

For a chart-based app (a HelmRelease) every workload is compared on its
environment variables and on its container resources, image, replicas and
selector. The resources the legacy workload had are carried into the patch
(and checked by rendering the chart again); everything else is reported:

  warning  can break the migrated app, so "apps migrate --yes" stops on it:
           less memory than live had, a different image tag, replicas going
           from 0 to N, an -Xmx above the memory limit, a selector Helm cannot
           change in place, a difference the patch cannot carry
  info     the rest: what the patch carries from live, and differences the
           catalog's "exclude" list keeps out on purpose

--view picks how it is shown:
  unified  a diff in the terminal (default)
  patch    the patch "apps migrate" would write (not with --drift or --patched)
  meld     open meld, even when nothing differs, to look at both sides`

const appsDiffExample = `  flux stratio apps diff psql                          # what would migrating change?
  flux stratio apps diff psql --view patch             # print the patch migrate would write
  flux stratio apps diff psql --patched --view meld    # after migrating: what still differs from live?
  flux stratio apps diff pool-psql --baseline latest   # Flux already reset live: use the backup
  flux stratio apps diff psql --drift latest           # has psql changed since the last backup?
  flux stratio apps diff kafka1 --as kafka/kafka       # live kafka1 belongs to tenant entry "kafka"`

const appsBackupLong = `Saves the legacy state of live apps to disk, so it can be used as the source of
a patch after Flux has reset the live app. Reads only the cluster (never the
tenant file), so it works before an app is in the tenant file. Writes only
local files.

Take the backup BEFORE you push an app to the tenant file: once Flux
reconciles an app without its patch it overwrites the legacy values, and the
backup is then the only record of them (see "apps diff/migrate --baseline").

Choose what to back up (exactly one):
  <name>     one app
  --catalog  every live object that a catalog type selects
  --all      --catalog plus every other live object found in the cluster

Each capture is <dir>/<app-id>/<UTC timestamp>/ and holds:
  cr.yaml                           an operator-managed app (a custom resource)
  deployment.yaml                   a chart-based app's workload (and
  workload.<kind>.<name>.yaml       each sibling workload)
  env-vars.env                      its resolved environment, merged, and
  env-vars.<kind>.<name>.env        one file per workload (same-named
                                    variables of siblings stay apart)
  helmrelease.yaml + values.yaml    when only a HelmRelease is live

--dir sets the backups root (default: a backups/ directory next to the
catalog file). "apps diff/migrate" find it again with the same --dir.`

const appsBackupExample = `  flux stratio apps backup --catalog               # before touching anything
  flux stratio apps backup pool-psql               # a fresh reference point for one app
  flux stratio apps backup --all --dir ~/backups   # everything, into another directory`

const appsMigrateLong = `Writes an app's patch into the tenant file: the legacy values the GitOps render
does not already give it, so that once Flux reconciles the app it keeps its
legacy configuration. The patch goes into the app's entry
(components.<key>[name=<entry>].patches, or config.agent.patches for a gosec
agent); every comment in the file is kept. It never commits or pushes: that
is yours. Running it twice changes nothing the second time. Start with
--dry-run.

For each app it (1) compares desired state and live exactly like "apps diff",
(2) shows the tenant-file change and asks you to confirm, (3) saves it,
(4) runs the app's prepare step, if it has one.

Prepare steps change the live cluster (a backup is taken first). Today:
  datamarket-agent  suspend and scale down the legacy agent
  bdl-datarest      remove the legacy ingress that collides with the new one
  dlc-entity        remove the legacy ingress and Deployment
  genai             rewrite stored component references (SQL; asks its own
                    confirmation, which --yes never answers)
--skip-prepare leaves them out and lists them at the end; run
"apps migrate <app>" later to do just the step.

Which app (exactly one):
  <name>   a live name or GitOps name. If its tenant entry is not obvious you
           are asked, or answer up front with --as <type>[/<entry>]
  --all    every classified app whose component the tenant file declares, in
           dependency order; undeclared (absent or commented-out) ones are
           skipped with a warning

Where the legacy values come from: the live cluster, which is right until
Flux has reconciled the app. If it already has (an "already managed by Flux"
warning), use --baseline latest to take them from the backup instead.

Warnings (values that would be lost, differences to review, unresolved
dependencies, an existing patch that would lose paths, a risky resource,
image, replicas or selector difference) stop --yes unless you also pass
--accept-warnings.`

const appsMigrateExample = `  flux stratio apps migrate psql --dry-run                # preview the tenant-file edit
  flux stratio apps migrate psql                          # apply it (asks to confirm)
  flux stratio apps migrate rocket --skip-prepare         # patch only, no prepare step
  flux stratio apps migrate pool-psql --baseline latest   # Flux already reset live: use the backup
  flux stratio apps migrate kafka1 --as kafka/kafka       # pin the tenant entry
  flux stratio apps migrate --all --dry-run               # survey everything first
  flux stratio apps migrate --all --continue-on-error     # then do what resolves`
