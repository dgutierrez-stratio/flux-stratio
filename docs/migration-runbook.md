# Migration runbook

This is the practical, end-to-end sequence for migrating a tenant's applications from an
Ansible-based cluster onto Flux/GitOps with flux-stratio. It assumes:

- The cluster's system services (namespace/CRD bootstrap, legacy Flux/Capsule/Kyverno/VPA cleanup)
  have already been migrated with [flux-keos](https://github.com/Stratio/flux-keos)'s
  `cluster migrate` — flux-stratio only ever touches applications, never that layer.
- You have a local checkout of `keos-apps`, `keos-use-cases`, `keos-fleet` and
  `keos-system-services` as sibling directories (`--base`/the config file's `base` points at their
  parent).
- `flux-operator`, `flux` and `helm` are on `PATH` (see the [README](../README.md#install)).

## 1. Get a config file

If this is the first run against a given `base`/`cluster`/`tenant`, seed one from the known Stratio
application catalog instead of authoring `apps:` from scratch:

```shell
flux stratio config init --base /path/to/gitops --cluster eosdev --tenant stratio
```

Review the result — an environment may run a subset of the seeded applications, or ones the catalog
doesn't know about yet — then continue below.

## 2. Preflight

```shell
flux stratio doctor
```

This checks binaries, the config file, the `--base` repo layout, cluster access, and that the
tenant's `ResourceSetInputProvider` file exists — in that order, reporting every problem it finds
rather than stopping at the first one. Fix everything `doctor` reports before continuing; a
migration command surfacing the same class of problem mid-run is a worse time to discover it.

## 3. Get (or generate) the tenant file

If the tenant already has a `ResourceSetInputProvider` in `keos-fleet` (the common case for a
tenant that's partway through migration already), `doctor` above confirms it exists and you can
skip to step 4.

For a tenant with no GitOps presence yet, scan the live, not-yet-migrated cluster for one:

```shell
flux stratio tenant import \
  --output keos-fleet/clusters/<cluster>/tenants/config/<tenant>.yaml
```

This produces a *skeleton*: every component the scan could discover, with dependencies wired
between them where the scan could resolve them. It's a starting point, not a finished tenant file —
review it before committing:

- Fields the scan cannot know at all (secrets, model configuration, storage sizing beyond what's
  inferred from the live cluster) are left at empty defaults; `internalS3BucketName`, `userEmail`,
  `userId` and `userName` in particular need filling in by hand.
- Any dependency the scan couldn't resolve is still emitted, with an empty `name:` and a warning
  logged to stderr explaining why — search the file for empty `name: ""` dependency entries and
  fill them in.
- The scan never writes to the file directly (`--output` is required, or it prints to stdout), and
  refuses to overwrite an existing `--output` without `--force` — regenerating a tenant file that
  already has hand-authored fields or patches would drop them.

Commit the reviewed file to `keos-fleet` before continuing.

## 4. Survey what needs migrating

The config file's `apps:` list (see [`config-reference.md`](config-reference.md)) is the catalog of
migratable applications — there is no live discovery of "what apps exist" the way `tenant import`
discovers components; each app is a deliberate entry an operator added once, reviewed, and expects
to keep migrating the same way every time.

For an app with a declared `prepare` step, read what that step actually does before your first run
against production — `flux stratio apps migrate` runs it automatically, but a step like
`prepare-datamarket-agent` suspends and scales down a live workload, which is disruptive by nature
even though it's exactly what needs to happen before cutover. See
[`config-reference.md`](config-reference.md#prepare) for what each of the four steps does.

## 5. Back up before touching anything

```shell
flux stratio apps backup --catalog
```

Captures every app's live state to `backups/<app-id>/<UTC-timestamp>/` next to the config file (or
wherever `--dir` points). This has two purposes: a record of exactly what was live before cutover,
and a fixed comparison point for `apps diff --baseline` — useful when the live object itself is
about to change (e.g. right before running a `prepare` step) and you want to diff against what it
looked like a moment ago, not whatever it looks like when you happen to run the diff.

`--catalog` only backs up what's in the config file — the apps this run is actually going to
migrate. Pass `--all` instead for a broader, unscoped capture of everything the cluster scan finds
(system services, CCT, anything else still live) — useful as a one-off safety net before a bigger
cutover, but not something `apps diff --baseline` needs for the apps this plugin migrates.

Unlike `apps diff`/`apps migrate`, backup finds the live object by scanning the cluster directly —
it never needs the tenant file to already declare the app's component. It's safe (and useful) to
run this step well before step 3 gets a tenant file in place at all.

## 6. Diff, review, then migrate — one app at a time first

For at least the first app of each *kind* (a manifest/CRD app like `psql`, and a chart-mode app
like a gosec agent), go through the full cycle by hand before trusting `--all`:

```shell
flux stratio apps diff psql                    # see what would change
flux stratio apps diff psql --view patch       # see the exact patch YAML, if you want it
flux stratio apps migrate psql --dry-run       # preview the tenant-file edit itself
flux stratio apps migrate psql                 # apply it (prompts for confirmation)
```

`apps migrate` is idempotent: running it again against an already-migrated app recomputes the diff
fresh from live state and finds nothing to change, rather than trusting a prior result. Re-running
it after live state drifts (say, someone hand-edited a value on the old cluster) picks up the new
difference and re-patches — it never silently skips based on "already done."

## 7. Migrate the rest

```shell
flux stratio apps migrate --all
```

Apps are ordered by their dependencies as declared in the tenant file itself (a dependency migrates
before its dependent), not by the order they appear in the config. By default, the run stops at the
first app that fails, so a real problem doesn't get masked by nine "successful" migrations after
it; pass `--continue-on-error` once you're confident enough failures are isolated per-app to be
worth pushing through.

## 8. Verify

After migrating an app, confirm Flux actually reconciles the patch you just wrote — flux-stratio
edits the tenant file; it doesn't reconcile it:

```shell
flux-operator build rset \
  -f keos-use-cases/<app's rset> \
  --inputs-from-provider keos-fleet/clusters/<cluster>/tenants/config/<tenant>.yaml
```

confirms the patch renders into the Kustomization as expected, and a `git diff` on the tenant file
shows only the intended `patches:` block changed — every comment and commented-out component stays
untouched (see `internal/tenantfile`'s design if you're curious why that matters here specifically:
a whole-file YAML re-dump would silently destroy them).

Commit the tenant file, let Flux reconcile it against the real cluster, and confirm the live object
now matches — at which point the legacy, pre-migration copy can be decommissioned per your own
cutover process (flux-stratio doesn't do that part; it only migrates the GitOps side into existence
next to what's still live).

## 9. Check for drift, after the fact

Once Flux is reconciling an app, `apps diff psql`'s default comparison stops being useful for
day-to-day checks — it renders the desired state and compares it against live every time, the
question `apps migrate` needed, not "has anything changed since I last looked." `apps diff --drift`
answers that instead: the live cluster right now, compared directly against a stored backup, with
no GitOps rendering at all.

```shell
flux stratio apps backup psql              # capture a fresh reference point, any time
flux stratio apps diff psql --drift latest # has anything changed live since that capture?
```

`--drift` (like `--baseline`) resolves `latest`, an app's own backup directory, or the overall
`backups/` root automatically — see [`README.md`](../README.md#commands). Pass `--view meld` on
either comparison to open it in [meld](https://meldmerge.org/) instead of a terminal diff.

`--baseline` and `--drift` answer different questions and are never interchangeable: `--baseline`
still needs a working GitOps render (the tenant file must declare the component), because it's
comparing against the *desired* state; `--drift` never renders anything, so it works before or
after migration, any time you have a backup to compare against.
