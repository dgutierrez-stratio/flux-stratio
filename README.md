# flux-stratio

A [Flux CLI plugin](https://github.com/fluxcd/flux2/blob/main/rfcs/0013-cli-plugin-system/README.md)
(`flux stratio ...`) that migrates Stratio applications from an Ansible-based deployment onto a
Flux/GitOps tenant: importing a tenant's live state into a `ResourceSetInputProvider` skeleton,
backing up and diffing each application against its rendered GitOps desired state, and splicing
the resulting patch into that tenant's file — safely and repeatably enough to run against a
production cluster.

This plugin covers **application** migration only. Generic, cluster-wide system-services migration
(namespace/CRD bootstrap, legacy Flux/Capsule/Kyverno/VPA cleanup) is
[flux-keos](https://github.com/Stratio/flux-keos)'s job; chart/CCT descriptor maintenance stays in
the Python `migrate.py charts` tooling. See [`docs/migration-runbook.md`](docs/migration-runbook.md)
for how the two plugins fit together in a full cluster migration.

Unlike flux-keos, flux-stratio is **not** a self-contained binary: it shells out to `flux-operator`
and `flux` to render exactly what Flux would reconcile (byte-identical fidelity beats
reimplementing their template/build logic), and to `helm` to render a chart's env vars for
comparison. See [Install](#install) for what needs to be on `PATH`.

## Install

Download a release archive for your platform, then install it as a Flux CLI plugin:

```shell
tar xzf flux-stratio_<os>_<arch>.tar.gz
chmod +x flux-stratio
mkdir -p ~/.fluxcd/plugins
mv flux-stratio ~/.fluxcd/plugins/flux-stratio
```

`flux stratio --help` should now work (requires the [Flux CLI](https://fluxcd.io/flux/cmd/) 2.9+).

flux-stratio also needs these on `PATH`, and checks for all of them up front via `flux stratio
doctor`:

| Binary | Used for | Minimum version |
|---|---|---|
| [`flux-operator`](https://github.com/controlplaneio-fluxcd/flux-operator) | rendering a tenant's ResourceSet | 0.45.1 |
| [`flux`](https://fluxcd.io/flux/cmd/) | rendering a Kustomization's objects | 2.9.0 |
| [`helm`](https://helm.sh/) | rendering a chart's env vars (chart-mode apps only) | any |
| `kubectl` | client-version check only — flux-stratio talks to the cluster directly via its own Kubernetes client, never by shelling out to `kubectl` | any |

[`meld`](https://meldmerge.org/) is optional — only `apps diff --view meld` needs it, checked
separately by `doctor` as a non-blocking, informational check.

It never runs `git` itself: it edits the tenant file in place; committing and pushing is up to you.

## Quick start

```shell
# Write ~/.fluxcd/flux-stratio/catalog.yaml (the known Stratio component types, with the
# selectors that recognize their live legacy objects) and environment.yaml (--base/--cluster/
# --tenant). See docs/config-reference.md. Add --charts /path/to/charts too if chart-mode
# components' Helm chart sources aren't checked out as a sibling of --base's keos-* repos.
flux stratio config init --base /path/to/gitops --cluster eosdev --tenant stratio

# Confirm binaries, catalog, environment, repo layout, cluster access and the tenant file are all in order
flux stratio doctor

# No tenant file yet? Scan the live, not-yet-migrated cluster for one
flux stratio tenant import --output /path/to/keos-fleet/clusters/eosdev/tenants/config/stratio.yaml

# Capture live state before touching anything, for the record and for --baseline diffing later
flux stratio apps backup --catalog

# See what migrating one app would change — by its live name (psql, psql-agent, kafka1, ...)
# or its GitOps object name (psql-gosec-agent, ...); the type is detected from the live object
flux stratio apps diff psql

# Preview the tenant-file edit without writing it
flux stratio apps migrate psql --dry-run

# Migrate it for real (prompts for confirmation unless --yes)
flux stratio apps migrate psql

# Or migrate every app, in dependency order, stopping on the first failure
flux stratio apps migrate --all
```

## Which files am I using?

flux-stratio reads two files (see [`docs/config-reference.md`](docs/config-reference.md)):

| | Catalog (component types — static) | Environment (repos, cluster, tenant) |
|---|---|---|
| 1. flag | `--config PATH` | `--env-config PATH` |
| 2. env var | `$FLUX_STRATIO_CONFIG` | `$FLUX_STRATIO_ENV` |
| 3. if it exists | `~/.fluxcd/flux-stratio/catalog.yaml` | `~/.fluxcd/flux-stratio/environment.yaml` |
| 4. if it exists | `./flux-stratio.yaml` | `./flux-stratio-env.yaml` |

`~/.fluxcd/flux-stratio/` is a sibling of `~/.fluxcd/plugins/` (where the plugin binary itself
lives), not a subdirectory of it — keeping both files there means `apps backup`'s default `--dir`
(always "next to the resolved catalog file") lands at `~/.fluxcd/flux-stratio/backups/` too:
permanently outside any source checkout, so nothing about building or cleaning this repo can ever
touch any of them.

`--base`, `--cluster` and `--tenant` override the environment file's fields for a single
invocation; with all three given, no environment file is needed at all.

The catalog holds no environment- or instance-specific value: which live object is which
component instance, under what name and namespace, and which tenant-file entry it migrates into,
is derived from the live cluster and the tenant file on every run. When that can't be inferred,
the command asks (or takes `--as <type>/<entry>`); the answer is never stored.

## Cluster access

flux-stratio talks to the cluster directly through its own Kubernetes client (never by shelling
out to `kubectl`), configured the same way `kubectl` itself is: `--kubeconfig` and `--kube-context`
flags, falling back to the usual `KUBECONFIG` environment variable and current context.

## Logging

Progress narration goes to stderr, one line per step, with the same symbol vocabulary as the
`flux` CLI itself (and flux-keos):

| Symbol | Meaning |
|---|---|
| `►` | starting a unit of work |
| `✚` | something was created or written |
| `◎` | waiting on external state |
| `✔` | a unit of work finished |
| `⚠️` | noteworthy but non-fatal |
| `✗` | a unit of work failed |

Every `apps diff`/`apps migrate` run says up front which two sides it compares — the rendered
GitOps *desired state*, the *live cluster* (now), or a *backup* (named "your last backup" when
`latest` located it):

```
► diffing "Postgres psql": desired state vs. live cluster
► diffing "Postgres pgbouncer pool-psql": desired state vs. your last backup (~/.fluxcd/flux-stratio/backups/pool-psql/2026-09-28T09-47-24Z)
► checking "Postgres psql" for drift: live now vs. your last backup (~/.fluxcd/flux-stratio/backups/psql/2026-09-28T09-47-24Z)
► migrating "Postgres pgbouncer pool-psql": patch from desired state vs. your last backup (...)
```

Actual result data — a diff, a patch, a generated tenant file — always goes to stdout instead, so
piping or redirecting a command's output never captures progress noise along with it. Pass
`-v`/`--verbose` for additional diagnostic detail (e.g. what a shelled-out command printed).

## Commands

| Command | Flags | Description |
|---|---|---|
| `flux stratio version` | | Print the flux-stratio version |
| `flux stratio doctor` | | Check binaries, catalog, environment, repo layout, chart paths, catalog types, cluster access and the tenant file are all in order |
| `flux stratio config init` | `--dir`, `--force`, `--charts` | Write `catalog.yaml` (seeded with the known Stratio component types) and `environment.yaml` |
| `flux stratio tenant import` | `--size`, `--output`, `--force` | Scan a live, not-yet-migrated cluster and render a tenant `ResourceSetInputProvider` skeleton |
| `flux stratio apps diff <name>` | `--baseline`, `--drift`, `--view`, `--as` | Pre-migration: compare desired state against live (or a backup, with `--baseline`). Post-migration: `--drift` compares live right now directly against a backup, no GitOps rendering. `--view unified\|patch\|meld` picks how it's shown |
| `flux stratio apps backup <name> \| --catalog \| --all` | `--dir`, `--as` | Capture an app's live legacy state to disk (`--catalog`: every live object a catalog type selects; `--all`: that plus every other live object the cluster scan finds, unfiltered) |
| `flux stratio apps migrate <name> \| --all` | `--dry-run`, `-y`/`--yes`, `--continue-on-error`, `--as`, `--baseline`, `--dir` | Diff an app (running its declared prepare step first, if any) and splice the resulting patch into the tenant file. `--baseline` computes the patch from a backup instead of live — for a component Flux already reconciled unpatched |

Persistent flags on every command: `--config`, `--env-config`, `--base`, `--cluster`, `--tenant`, `--kubeconfig`,
`--kube-context`, `-v`/`--verbose`.

`config init` requires `--base`, `--cluster` and `--tenant` on the command line, since by
definition there's no environment file yet to read them from. It writes a 19-type component
catalog covering the application set the legacy Python migration client shipped, with every
selector derived from real captured legacy objects — and refuses to overwrite either file without
`--force`. Review the catalog, then edit it by hand from then on: it's the same for every
environment, so one reviewed copy can be shared across a team.

`tenant import` is named to avoid confusion with flux-keos's own `tenant create`, which scaffolds a
brand-new tenant from a template; this command instead reads a live, not-yet-migrated cluster and
imports what it finds there. It refuses to overwrite an existing `--output` file without `--force`
— regenerating drops any hand-authored fields, comments or patches (with no `--output` at all, it
prints to stdout instead, so it can never clobber anything by default).

`apps backup` finds live objects by scanning the cluster directly (never by rendering the app, and
never through the tenant file). `--catalog` captures every instance a catalog type's selectors
classify, each from the exact live object it was classified from; `--all` also captures every
other live Kustomization/HelmRelease/workload/known-CRD identity the scan found (system services,
CCT, or anything else still live on a not-yet-migrated cluster). A catalog instance is captured
identically either way — same directory, same shape `apps diff --baseline` expects.

`apps diff` answers two different questions, each its own flag (`flux stratio apps diff --help`
covers this in detail): the default (and `--baseline`) compares the *rendered GitOps desired
state* against something live-ish — the live cluster, or a backup standing in for it — the
pre-migration question `apps migrate` needs answered to compute a patch. `--drift` compares the
live cluster *right now* directly against a backup, with no GitOps rendering at all — the
post-migration question, worth asking once Flux already reconciles the desired side and what you
actually want to know is whether anything has changed since a known-good snapshot. `--baseline`/
`--drift` don't require the full backup path: `--baseline latest` (or `--drift latest`)
auto-locates the app's most recent capture, and a partial path — an app's own backup directory, or
the overall `backups/` root — resolves the same way. `--view` picks how the result is shown —
`unified` (default, a terminal diff), `patch` (the raw patch YAML `apps migrate` would write;
invalid with `--drift`), or `meld` (opens [meld](https://meldmerge.org/) instead, even when there are no differences, so
you can inspect both sides) — independently of which comparison ran.

### How `apps migrate` computes a patch — and why order matters

A patch is the set of legacy values the GitOps render doesn't already produce. `apps migrate`
(and `apps diff --view patch`, which shows exactly what it would write) computes it like this:

1. **Desired side:** the component is rendered from the tenant file *without* its existing patch
   for the object's kind — the one `apps migrate` replaces (patches are replaced by
   `target.kind`). So the result is always the whole patch, never a leftover delta that would drop
   what the existing patch already set.
2. **Legacy side:** the live cluster, or a backup with `--baseline`.
3. **Result:** if the tenant file already carries exactly that patch, there is nothing to do — so
   running `apps migrate` twice converges. Otherwise the patch is written in full, replacing the old
   one, with every comment in the tenant file preserved.

**Back up, then migrate each component *before* pushing it to the tenant file unpatched.** Once a
component is pushed without its patch and Flux reconciles it, the live object is reset to the GitOps
defaults, and a patch computed from live can no longer see the legacy values Flux overwrote — they
survive only in the backup. `apps diff` and `apps migrate` warn when this has happened (the live
object carries Flux's `kustomize.toolkit.fluxcd.io/name` or `helm.toolkit.fluxcd.io/name` label):

```
⚠️ live stratio-datastores/pool-psql is already managed by Flux (Kustomization apps-pool-psql): it may no
   longer hold the legacy values — compare against a pre-cutover backup instead: --baseline latest
```

To recover, compute the patch from the backup instead of live, then commit and push the tenant
file — Flux then restores the legacy values:

```shell
flux stratio apps diff pool-psql --baseline latest --view patch   # the whole patch, from the backup
flux stratio apps migrate pool-psql --baseline latest --dry-run   # preview the tenant-file edit
flux stratio apps migrate pool-psql --baseline latest             # write it
```

`--baseline latest` picks the component's most recent backup; pass `--dir` if `apps backup` used a
non-default one. See [`docs/migration-runbook.md`](docs/migration-runbook.md) for the full sequence.

An app whose config declares a `prepare` step (see
[`docs/config-reference.md`](docs/config-reference.md)) has that precondition checked — and, for an
automated step, satisfied — as the first stage of `apps migrate`, before any diff or patch. An
automated step first lists every operation it would perform (on stderr) and the live manifest of each
object it acts on (on stdout), then asks before running exactly those; `--dry-run` stops after the
list. A step requiring a manual action (`prepare-genai`'s Postgres data rewrite) prints what to do
and always asks its own separate confirmation, never skipped by `--yes`.

## Build from source

```shell
git clone https://github.com/Stratio/flux-stratio
cd flux-stratio
make build      # -> bin/flux-stratio
make install    # -> ~/.fluxcd/plugins/flux-stratio
```

## Development

```shell
make fmt-check   # gofmt
make vet         # go vet
make lint        # golangci-lint (not wired into CI; a local check)
make test        # go test ./...
```

`golangci-lint` isn't installed by `make`; see the
[installation instructions](https://golangci-lint.run/welcome/install/) if `make lint` reports it
missing.
