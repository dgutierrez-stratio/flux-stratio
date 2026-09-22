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

It never runs `git` itself: it edits the tenant file in place; committing and pushing is up to you.

## Quick start

```shell
# Point at your app catalog (see docs/config-reference.md for the full format)
cat > flux-stratio.yaml <<'YAML'
base: /path/to/gitops        # parent of keos-apps, keos-use-cases, keos-fleet, keos-system-services
cluster: eosdev
tenant: stratio
apps:
  - id: psql
    name: Postgres psql
    rset: apps/components/resourceset-apps-datastores.yaml
    kustomization: apps-psql
    object: psql
YAML

# Confirm binaries, config, repo layout, cluster access and the tenant file are all in order
flux stratio doctor

# No tenant file yet? Scan the live, not-yet-migrated cluster for one
flux stratio tenant import --output /path/to/keos-fleet/clusters/eosdev/tenants/config/stratio.yaml

# Capture live state before touching anything, for the record and for --baseline diffing later
flux stratio apps backup --all

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
3. `~/.config/flux-stratio/config.yaml`, if it exists
4. `./flux-stratio.yaml`, if it exists

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
| `flux stratio tenant import` | `--size`, `--output`, `--force` | Scan a live, not-yet-migrated cluster and render a tenant `ResourceSetInputProvider` skeleton |
| `flux stratio apps diff <id>` | `--patch`, `--baseline` | Compare an app's rendered GitOps desired state against its live legacy state (or a backup, with `--baseline`) |
| `flux stratio apps backup <id> \| --all` | `--dir` | Capture an app's live legacy state to disk |
| `flux stratio apps migrate <id> \| --all` | `--dry-run`, `-y`/`--yes`, `--continue-on-error` | Diff an app (running its declared prepare step first, if any) and splice the resulting patch into the tenant file |

Persistent flags on every command: `--config`, `--base`, `--cluster`, `--tenant`, `--kubeconfig`,
`--kube-context`, `-v`/`--verbose`.

`tenant import` is named to avoid confusion with flux-keos's own `tenant create`, which scaffolds a
brand-new tenant from a template; this command instead reads a live, not-yet-migrated cluster and
imports what it finds there. It refuses to overwrite an existing `--output` file without `--force`
— regenerating drops any hand-authored fields, comments or patches (with no `--output` at all, it
prints to stdout instead, so it can never clobber anything by default).

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
