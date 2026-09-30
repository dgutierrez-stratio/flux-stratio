# Investigation: `tenant import` can't scaffold nested/structured component config

## Summary

`tenant import` (`internal/tenantimport`) scaffolds a tenant-file skeleton from a live cluster
scan — components, their dependencies, and (via `catalog.Schema.ExtraConfig`) any other scalar
config field a component's ResourceSet template reads. That last mechanism only understands a
**flat scalar** read directly off `$componentConfig` with an optional **quoted-string** default.
It has no notion of a **nested** sub-config — a variable assigned from `$componentConfig`, itself
read for several leaf keys further down the same block. `genai`'s own `llmModels` (`chat`/
`governance`/`translate`) is exactly that shape, and the investigation below shows it's not just
under-scaffolded — it's silently dropped with no warning at all, unlike every other kind of gap
`tenant import` already knows how to flag.

This is the same underlying scenario as `docs/TASK-multi-workload-env-var-collision.md` (a
component whose full configuration can't be inferred from what's discoverable), but a different
mechanism and a different fix — that task is about `apps diff`/`apps migrate`'s chart-mode env-var
diffing; this one is about `tenant import`'s own scaffolding completeness. Track them separately.

## The existing (scalar-only) mechanism

- `internal/catalog/extract.go`'s `extraConfigRe`:
  ```go
  extraConfigRe = regexp.MustCompile(`get\s+\$componentConfig\s+"([^"]+)"(?:\s*\|\s*default\s+"([^"]*)")?`)
  ```
  Matches `get $componentConfig "<key>"`, and — if immediately followed by
  `| default "<quoted string>"` — captures that as the field's default.
- `extractSchema` records every match as an `ExtraConfigField{Key, Default, HasDefault}` (one
  scalar per key), skipping `size`/`dependencies`/`type` (`extraConfigIgnore`).
- `internal/tenantimport/enrich.go`'s `enrichEntry`:
  ```go
  for _, field := range schema.ExtraConfig {
      if _, already := entry.ExtraConfig[field.Key]; already {
          continue
      }
      if field.HasDefault {
          entry.ExtraConfig[field.Key] = field.Default
      }
  }
  ```
  **Only scaffolds a field when `HasDefault` is true.** A field recognized by `extraConfigRe` but
  without a quoted-string default is silently skipped — no entry in the generated YAML, and no
  `warn(...)` call either (contrast with the dependency-resolution loop just above it in the same
  function, which always emits a warning for anything it can't resolve).

## The gap, traced through `genai`'s own template

`keos-use-cases/apps/components/resourceset-apps-genai.yaml`:
```
242:    <<- $llmModels := get $componentConfig "llmModels" | default dict >>
...
318:          LLM_MODEL_CHAT: "<< get $llmModels "chat" | default "" >>"
319:          LLM_MODEL_GOVERNANCE: "<< get $llmModels "governance" | default "" >>"
320:          LLM_MODEL_TRANSLATE: "<< get $llmModels "translate" | default "" >>"
```

Two separate reasons this is invisible to scaffolding today (verify both with a unit test against
this exact block before relying on either — this is reasoned from reading the regex and code, not
yet confirmed by running it):

1. **Line 242's own default breaks `HasDefault` capture.** `extraConfigRe`'s optional default
   group requires a quoted string (`default\s+"([^"]*)"`); `| default dict` is a bareword Sprig
   call, not a quoted string, so the optional group simply fails to match at that position and
   the engine backs off to zero repetitions. The mandatory part still matches, so `llmModels` *is*
   captured as a key — but with `HasDefault: false`. Per `enrichEntry` above, that means it's
   **dropped entirely**, not scaffolded even as an empty/wrong-shaped placeholder.
2. **Even if `HasDefault` were somehow true, the shape would be wrong.** `ExtraConfigField.Default`
   is a plain `string`. `llmModels` is a map with three named leaves (`chat`/`governance`/
   `translate`), each read via a *second* indirection (`get $llmModels "<leaf>"`, not
   `get $componentConfig "..."` directly) that `extraConfigRe` doesn't match at all — it only looks
   for reads directly off `$componentConfig`. So the three leaf keys aren't independently visible
   to extraction under any circumstance today, regardless of point 1.

Net effect: a tenant file scaffolded by `tenant import` for a fresh `genai` instance would emit no
`llmModels` key at all — not a placeholder, not a warning — even though the chart hard-requires it
(no usable chart default; see the sibling investigation for how that surfaces as a crash loop).

## Precedent already in the codebase for recognizing nested config

`internal/catalog/extract.go` already has `agentAssignRe`:
```go
// agentAssignRe finds a nested-config variable assignment shaped like
// `$agentConfig := get $componentConfig "agent"` — the one nested
// sub-config shape currently in use (see the package doc).
agentAssignRe = regexp.MustCompile(`\$(\w+)\s*:=\s*get\s+\$componentConfig\s+"(\w+)"`)
```
— but it's used only by `extractKustomizations`, to tell a component's own nested
Kustomization-patches sub-resource (e.g. a postgres/opensearch gosec agent) apart from a
`dependsOn` cross-reference. It's the right *shape* of recognition (a variable assigned from
`$componentConfig`, then referenced elsewhere in the block) but it isn't reused for ExtraConfig
scaffolding, and `llmModels` isn't tied to a Kustomization/patches block at all — it only feeds a
`postBuild.substitute` variable. A real fix likely means generalizing recognition of "a variable
assigned from `$componentConfig`, then read for N leaf keys elsewhere in the block" into its own
extraction pass feeding `Schema.ExtraConfig` (or a new nested sibling of it), independent of the
Kustomization-anchor-specific use `agentAssignRe` currently serves.

## What a fix needs to answer

1. **Extend `Schema` to represent a nested config group**, not just flat scalars — e.g. a new
   `NestedConfig` (or generalize `ExtraConfigField`) carrying the group key (`llmModels`) and its
   leaf keys (`chat`, `governance`, `translate`), discovered by generalizing `agentAssignRe`'s
   pattern to scan the *whole* block for `get $<assignedVar> "<leaf>"` reads, not just the ones
   feeding a `patches:` line.
2. **Scaffold nested groups as a proper map in the generated YAML**, not a scalar — e.g.
   `llmModels: {chat: "", governance: "", translate: ""}` (or a clearer placeholder like
   `"<fill in>"`), so an operator opening the generated tenant file sees exactly what shape is
   expected instead of nothing at all.
3. **Warn, always, when a group (or a plain ExtraConfig field) has no usable default** — mirroring
   the dependency-resolution loop's own `warn(...)` calls. This is the single highest-value,
   lowest-effort piece: even without solving nested scaffolding shape, making `enrichEntry` warn
   whenever it drops a field for lacking `HasDefault` (instead of silently skipping) would have
   caught this at import time, for `llmModels` and for anything else this affects.
4. **Check for other affected fields.** `models` (litellm's own tenant-entry field, a list of
   `{provider, model, auth...}` — see `resourceset-apps-genai.yaml`'s litellm block,
   `LLM_MODELS_JSON: << get $componentConfig "models" | default list | toJson | quote >>`) has the
   same "default isn't a quoted string" shape (`default list`, not `default "..."`) — grep every
   ResourceSet template for `| default (dict|list)` (non-string defaults) to find the full set of
   fields silently invisible to scaffolding today, not just genai's `llmModels`.

## Where to start reading

- `internal/catalog/extract.go` — `extraConfigRe`, `extractSchema`, `agentAssignRe`,
  `extractKustomizations`
- `internal/catalog/catalog.go` — `Schema`, `ExtraConfigField`
- `internal/tenantimport/enrich.go` — `enrichEntry`
- `internal/tenantimport/render.go` — `renderEntries` (how `ExtraConfig` becomes YAML)
- Existing tests to extend, matching this repo's conventions (table-driven, `testdata/` fixtures):
  `internal/catalog/extract_test.go`, `internal/tenantimport/enrich_test.go`,
  `internal/tenantimport/render_test.go` — a first, cheap step before any design work is a table
  test asserting today's actual (not just reasoned-through) output for a `$componentConfig` read
  with a non-string default, and for a two-level nested read like `llmModels`, to turn the
  hypothesis above into a confirmed baseline.

## Non-goals for this task

- Not about the env-var diffing collision (`docs/TASK-multi-workload-env-var-collision.md`) —
  that's a different mechanism (`internal/diff`/`internal/appdiff` chart-mode diffing), already
  under separate investigation.
- Don't assume `llmModels` is the only affected field — measure via the grep in point 4 above
  before deciding scope.
- Don't design nested-config scaffolding as a fully general, arbitrarily-deep mechanism unless the
  measurement in point 4 shows it's needed — `agentAssignRe`'s existing one-level-deep pattern
  covers everything seen so far.
