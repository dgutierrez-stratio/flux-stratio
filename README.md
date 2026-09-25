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
# Seed a starting config file from the known Stratio application catalog
# (see docs/config-reference.md for the full format, and to review what got written)
# Add --charts /path/to/charts too if chart-mode apps' Helm chart sources aren't
# checked out as a sibling of --base's keos-apps/keos-use-cases/keos-fleet/keos-system-services.
flux stratio config init --base /path/to/gitops --cluster eosdev --tenant stratio

# Confirm binaries, config, repo layout, cluster access and the tenant file are all in order
flux stratio doctor

# No tenant file yet? Scan the live, not-yet-migrated cluster for one
flux stratio tenant import --output /path/to/keos-fleet/clusters/eosdev/tenants/config/stratio.yaml

# Capture live state before touching anything, for the record and for --baseline diffing later
flux stratio apps backup --catalog

# See what migrating one app would change
flux stratio apps diff psql

# Preview the tenant-file edit without writing it
flux stratio apps migrate psql --dry-run

# Migrate it for real (prompts for confirmation unless --yes)
flux stratio apps migrate psql

# Or migrate every app, in dependency order, stopping on the first failure
flux stratio apps migrate --all
```

## Which config am I using?

Every command that needs the app catalog resolves it in this order:

1. `--config PATH`
2. `$FLUX_STRATIO_CONFIG`
3. `~/.fluxcd/flux-stratio/config.yaml`, if it exists
4. `./flux-stratio.yaml`, if it exists

`~/.fluxcd/flux-stratio/` is a sibling of `~/.fluxcd/plugins/` (where the plugin binary itself
lives), not a subdirectory of it — keeping your config there, rather than a `flux-stratio.yaml` in
whatever directory you happen to be working in, means `apps backup`'s default `--dir` (always "next
to the resolved config file") lands at `~/.fluxcd/flux-stratio/backups/` too: permanently outside
any source checkout, so nothing about building or cleaning this repo can ever touch either one.

`--base`, `--cluster` and `--tenant` override the config file's own `base`/`cluster`/`tenant`
fields for a single invocation, without editing the file. See
[`docs/config-reference.md`](docs/config-reference.md) for the full schema.

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

Actual result data — a diff, a patch, a generated tenant file — always goes to stdout instead, so
piping or redirecting a command's output never captures progress noise along with it. Pass
`-v`/`--verbose` for additional diagnostic detail (e.g. what a shelled-out command printed).

## Commands

| Command | Flags | Description |
|---|---|---|
| `flux stratio version` | | Print the flux-stratio version |
| `flux stratio doctor` | | Check binaries, config, repo layout, cluster access and the tenant file are all in order |
| `flux stratio config init` | `--output`, `--force` | Write a starting config file, seeded with the known Stratio application catalog |
| `flux stratio tenant import` | `--size`, `--output`, `--force` | Scan a live, not-yet-migrated cluster and render a tenant `ResourceSetInputProvider` skeleton |
| `flux stratio apps diff <id>` | `--baseline`, `--drift`, `--view` | Pre-migration: compare desired state against live (or a backup, with `--baseline`). Post-migration: `--drift` compares live right now directly against a backup, no GitOps rendering. `--view unified\|patch\|meld` picks how it's shown |
| `flux stratio apps backup <id> \| --catalog \| --all` | `--dir` | Capture an app's live legacy state to disk (`--catalog`: every app in the config catalog; `--all`: every live object the cluster scan finds, unfiltered) |
| `flux stratio apps migrate <id> \| --all` | `--dry-run`, `-y`/`--yes`, `--continue-on-error` | Diff an app (running its declared prepare step first, if any) and splice the resulting patch into the tenant file |

Persistent flags on every command: `--config`, `--base`, `--cluster`, `--tenant`, `--kubeconfig`,
`--kube-context`, `-v`/`--verbose`.

`config init` requires `--base`, `--cluster` and `--tenant` on the command line, since by
definition there's no config file yet to read them from. It writes the same 16-application catalog
the legacy Python migration client shipped — a real starting point, not a placeholder — and refuses
to overwrite an existing `--output` file without `--force`. It's a one-time scaffold: review the
result (an environment may run a subset of these applications, or ones this catalog doesn't know
about) and edit it by hand from then on, the same as any other config file.

`tenant import` is named to avoid confusion with flux-keos's own `tenant create`, which scaffolds a
brand-new tenant from a template; this command instead reads a live, not-yet-migrated cluster and
imports what it finds there. It refuses to overwrite an existing `--output` file without `--force`
— regenerating drops any hand-authored fields, comments or patches (with no `--output` at all, it
prints to stdout instead, so it can never clobber anything by default).

`apps backup` finds a live object by scanning the cluster directly (never by rendering the app, and
never through the config catalog) — `--catalog` and `--all` only decide *which* names it looks for,
not *how*: `--catalog` looks up every app the config file declares; `--all` looks up every distinct
live Kustomization/HelmRelease/workload/known-CRD identity the scan found, catalog or not. An app
that's in the catalog is captured identically either way — same directory, same shape `apps diff
--baseline` expects — `--all` just also picks up everything the catalog doesn't mention (system
services, CCT, or anything else still live on a not-yet-migrated cluster).

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
invalid with `--drift`), or `meld` (opens [meld](https://meldmerge.org/) instead) — independently
of which comparison ran.

An app whose config declares a `prepare` step (see
[`docs/config-reference.md`](docs/config-reference.md)) has that precondition checked — and, for an
automated step, satisfied — as the first stage of `apps migrate`, before any diff or patch. A step
requiring a manual action (`prepare-genai`'s Postgres data rewrite) prints what to do and always
asks its own separate confirmation, never skipped by `--yes`.

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
