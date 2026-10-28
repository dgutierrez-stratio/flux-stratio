# TASK-17 — the plugin never looks at Services: a legacy LoadBalancer loses its endpoints at cutover without a word (eosdev I33), and legacy Service leftovers are only found by hand

**Status: implemented 2026-10-28 on `feat/application-migration` (`internal/appdiff/services.go`, `internal/diff/servicepatch.go`, the review sections in `internal/cli/review_sections.go`, `writeServiceManifests` in `internal/backup`); unit and wiring tests pass. Verified on eosdev 2026-10-28 (see "Result on eosdev" below). Analysed the same day at `67b20f1`. No cluster change made for this finding.**

Implementation notes (differences from the design below): the carried case is a `[NOTE]`, like carried resources, not a `[REVIEW]`; a target's legacy Services are looked for in the namespace the diff found its live workload in and in the app's live namespace as well as the rendered one (`governance-datamarket-agent` renders in `stratio-apps`, its legacy `datamarket-agent` Service is in `stratio-datastores`).

Found in the eosdev legacy->GitOps run of 2026-10-26/27 (migration report finding **I33**, same class as **I26**, **I30**, **I55**; run folder `~/.fluxcd/flux-stratio/runs/eosdev-stratio/`).
Plugin to change: `flux-stratio` (this repo). Branch to work on: `feat/application-migration`.
Related, do not duplicate: `TASK-18` (the `virtualizer` chart gets a proper `service.loadbalancer` entry; this task does not depend on it, see "Interaction with TASK-18"), `TASK-15` (component-scoped prepare steps; `prepare-virtualizer` stays Deployments-only), `TASK-01` (backup completeness).

## Requirement for this task (user constraints, read first)

- **End state:** after a migration nothing the app depends on keeps a Command Center (CCT) reference, and every object the app needs is owned by Git (Flux/Helm). An object the GitOps render does not declare must not be "fixed" by patching it live.
- **Nothing is deleted by the plugin.** The cutover of legacy objects stays the operator's (memory rule `legacy-cutover-stays-manual`, `CONTEXT.md` section 2). This task adds no delete or patch operation on live objects; it changes what `apps diff` shows and what the migration **patch** carries.
- **Preserve legacy:** the patch exists to keep the legacy configuration (memory rule `migration-preserves-legacy`). An externally exposed legacy Service is legacy configuration: carry it.
- **Scope decided by the user (2026-10-28):**
  - **carry** in the patch every legacy **LoadBalancer or NodePort** Service that selects the app's legacy pods and has no chart counterpart. Match by **selector**, not by name: CCT lets the operator choose the name (`loadBalancerK8sServiceName`);
  - **only list** the legacy **ClusterIP** Services that lose their endpoints (the metrics Services), with their chart replacement and the commands to save and remove them, for the user's by-name removal (decision D10 of the run). No automatic removal.

## What happened

After `virtualizer` migrated (unit 11, 2026-10-26 11:19Z), the legacy Service `stratio-apps/virtualizer-loadbalancer` (type LoadBalancer, external address **10.202.18.2:13422**, used by SQL clients) kept its selector `cct.stratio.com/application_id=virtualizer.stratio-apps`. `prepare-virtualizer` had deleted the three legacy Deployments and the chart's pods carry `app.kubernetes.io/{name,instance,component}` and no CCT label, so the Service has had **no endpoints** since then. The orchestrator found it by hand after the cutover; the user left it (decision U15) because the tool had nothing to fix it with.

The same check found five ClusterIP Services in the same state: `stratio-datastores/dg-hdfs-agent` (I26), `eureka-agent` (I30), `datamarket-agent` (I55), `psql-agent` and `opensearch1-agent` (listed in the report's "legacy objects left behind"). These are harmless: each chart renders its own `<release>-metrics` Service, and at least `dg-agent` scrapes it through a `ServiceMonitor` (`charts/dg-agent/values.yaml:282-301`), so nothing depends on the legacy `governance-prometheus-selector` label any more (checked for `dg-agent` only, see "Open points").

Nothing in the plugin announced any of them, before or after the push:

| What | Where | Effect |
|---|---|---|
| `apps diff` / `apps migrate` compare **workloads only** (env, spec, selector) | `internal/appdiff/chart.go` (`workloadTargets` `:452`, `specDiffs` `:264`), `internal/cli/review_sections.go` (`specSections` `:241-290`) | A Service the chart does not render is never read |
| `prepare-virtualizer` leaves the LB on purpose | `internal/prepare/step_virtualizer.go:30-34` | Correct (no deletion), but the only trace is a code comment |
| The runbook says "decide what replaces it" | `docs/migration-runbook.md:189-193` | One sentence, nobody acts on it |
| `apps backup` saves workloads and env only | `internal/backup/backup.go` (`captureChartMode` `:442`, `writeWorkloadManifests` `:556`); `backups/virtualizer/2026-10-26T11-19-17Z/` holds `deployment.yaml`, `workload.deployment.*.yaml`, `env-vars*.env` | No copy of the Service to undo from, and `apps diff --baseline` cannot see Services |

## Why the LoadBalancer exists, and why nothing replaced it

It is a **supported CCT feature**, not a client's hand-made object. `kubernetes-universe/packages/virtualizer/virtualizer-default.json` (branch `branch-15.1`, commit `f14a1355`), section "Public access":

- `createLoadBalancer` (boolean, default `false`), around line 1471;
- `loadBalancerK8sServiceName` (default `${Application.name}-loadbalancer`), around line 1482;
- `loadBalancerExtraSANToAddToCertificate` (default `${loadBalancerK8sServiceName}.${Application.domain}.${external_domain}`), around line 1499, added to the server certificate's `altNames` (task `applicationCertificate`, around line 4988).

The install, update and upgrade lifecycles (tasks "Creating Load Balancer Service", around lines 5451, 5896, 6367) create it as a generic `kubernetes/customresource/update` object, which is why it carries no CCT label of its own:

```
ports: [{name: lb-port, protocol: TCP, port: 13422, targetPort: 13422}]
selector: {cct.stratio.com/application_id: <Application.id>}
sessionAffinity: None
type: LoadBalancer
```

The `virtualizer` chart (`charts/virtualizer/values.yaml:560`) renders only the ClusterIP `virtualizer` and the headless `dns` Services, and Flux manages only what the chart renders. So nothing in Git declares the LB (fixed in the chart by `TASK-18`).

## Why carrying it in the patch works (evidence)

1. **The chart can render it today, through values only.** The `common` library renders every `.Values.service.<id>`:
   - name: `<fullname>-<nameOverride>` (`charts/common/templates/lib/service/_valuesToObject.tpl:12-15`); the virtualizer chart sets `global.nameOverride` to the release name, so `nameOverride: loadbalancer` gives `virtualizer-loadbalancer`;
   - `type: LoadBalancer` with `loadBalancerIP`, `loadBalancerSourceRanges`, `loadBalancerClass`, plus `externalTrafficPolicy`, `sessionAffinity` and annotations (`charts/common/templates/classes/_service.tpl:16-75`);
   - `controller: <key>` gives the selector of that controller's pods.
2. **Tenant entries already carry HelmRelease `values` patches** (`keos-fleet/clusters/eosdev/tenants/config/stratio.yaml`, `patches:` under each component). The plugin already writes values there (env, resources: `internal/diff/resourcepatch.go`).
3. **Helm adopts the existing object instead of refusing it.** The Flux helm-controller's `install.disableTakeOwnership` and `upgrade.disableTakeOwnership` default to `false` (Flux HelmRelease API docs), so a rendered object with the name of an existing one is taken over in place. On eosdev Flux took over psql, the pooler, hdfs, kafka, opensearch and dashboards in place (report units 1-6), and the same-named legacy Services the charts render ended up serving the new pods (e.g. `discovery`, report unit 12: the legacy and chart Ingresses all route to the Service `discovery`, and the app works; the selector of the taken-over Service was not read directly).

So on push Helm takes over `virtualizer-loadbalancer`: same object, same external address, the chart's selector, owned by Git. Nothing is deleted and `prepare-virtualizer` does not change.

## Design

### 1. Detection: a "Services" analysis in `apps diff` and the `apps migrate` review

New file `internal/appdiff/services.go`. Inputs: the rendered docs (`renderChart`), the live workload pairs (`fetchWorkloadPairs` / `workloadTargets`, `chart.go:410-470`), the app family (`components.MatchesAny(app.Family, obj)`), and the Services listed in the app's live namespace and the release namespace (in baseline mode, the `service.*.yaml` files of the backup, see 3).

A live Service is **considered** when all hold:
- not Flux/Helm-managed (the `helm.toolkit.fluxcd.io/*` labels, as `fluxManaged` in `internal/prepare/prepare.go:222`), no `ownerReferences`;
- it has a non-empty `spec.selector`;
- its selector matches **no rendered pod template** (in the same namespace);
- its selector matches the pod template labels of **a legacy workload of the app's family** (before cutover), **or** matches no live pod at all while the app is migrated (after cutover: the case of eosdev today). For the "after" case, take the legacy pod labels from the baseline backup when there is one, and otherwise accept a selector made only of `cct.stratio.com/application_id=<id>` where `<id>` is the app's or a sibling's `LiveName().LiveNamespace()`.

Each considered Service becomes a `diff.ServiceDiff` (new type next to `SpecDiff` in `internal/diff/workloadspec.go`): `Namespace, Name, Type, ExternalAddress (status.loadBalancer.ingress[].ip|hostname, or nodePorts), Ports, Selector, Carried bool, Reason string, Replacement string, Monitored bool`.

Review sections (`internal/cli/review_sections.go`, same pattern as `specSections`):

| Case | Level | Text (shape) |
|---|---|---|
| LoadBalancer/NodePort, carried (see 2) | `[NOTE]` | `service <ns>/<name> (<type>, <address>) is carried in the patch as service.<key>: Helm takes it over on push, same object and address` |
| LoadBalancer/NodePort, not carriable | `[ACTION]` | `service <ns>/<name> (<type>, <address>) will lose its endpoints at cutover: the tool cannot carry it because <reason>` |
| ClusterIP with a rendered Service on the same ports for the same controller | `[REVIEW]` | `legacy leftover <ns>/<name>, replaced by <Replacement> (ServiceMonitor: yes/no); remove it at D10 with your yes` + two commands: `kubectl -n <ns> get service <name> -o yaml > <name>.yaml` and `kubectl -n <ns> delete service <name>` |
| ClusterIP without a replacement | `[ACTION]` | `legacy <ns>/<name> will lose its endpoints at cutover and the chart renders nothing on port(s) <p>` |

The `[ACTION]` rows make `--yes` stop (as the other warnings do: `review.hasWarnings()`, `apps_migrate.go`).

### 2. Carry: `serviceEdits` beside `resourceEdits`

New file `internal/diff/servicepatch.go`, called in `ChartDiff` right after the resource carry (`internal/diff/chartdiff.go:400-410`); its values are merged with `MergeValues` the same way. New input `ChartDiffInput.LiveServices []LiveService` (the considered exposed Services with the legacy workload they select), new result `ChartDiffResult.ServiceEdits []ServiceEdit` (like `ResourceEdits`), surfaced through `appdiff.Result` like `CarriedResources` (`internal/appdiff/appdiff.go:122-130`).

For a considered **LoadBalancer or NodePort** Service, write under `spec.values.service.<key>`:

| Value | From |
|---|---|
| `<key>` and `nameOverride` | the name minus the `<fullname>-` prefix (`virtualizer-loadbalancer` -> `loadbalancer`); if the name does not start with `<fullname>-`, it cannot be rendered under that name: **not carriable**, reason "name not expressible by the chart" |
| `enabled: true`, `controller` | the controller key of the rendered workload paired with the legacy workload the selector matched (`controllerKeyFor`, `internal/diff/resourcepatch.go:109`, or the pairing in `workloadTargets`); none found: **not carriable** |
| `type` | live |
| `ports.<name>` | for each live port: `port`, `protocol`, `targetPort` only when it differs from `port`, `nodePort` for NodePort/LoadBalancer when the live one was set explicitly; a **named** `targetPort` the rendered pod does not expose: **not carriable** |
| `loadBalancerIP` | live `spec.loadBalancerIP`, else the IP in `status.loadBalancer.ingress`, so the address is pinned in Git (the object keeps it on takeover anyway; the pin protects a later recreation) |
| `loadBalancerSourceRanges`, `loadBalancerClass`, `externalTrafficPolicy`, `sessionAffinity` | live, only when set and different from the Kubernetes default |
| `annotations` | live, minus `cct.stratio.com/*` and `kubectl.kubernetes.io/last-applied-configuration` |

Rules:
- When the chart **already renders** a Service with that name (after `TASK-18`, if the tenant enabled it), carry only the fields whose rendered value differs from live; nothing when all are equal.
- Respect the catalog `Exclude` paths (`isExcluded`).
- Verify after render like `VerifyResources` (`resourcepatch.go:140`): render again with the patch and check the Service comes out with the expected name, type, ports and the controller's selector; an edit that does not hold is dropped and reported as not carriable.
- ClusterIP Services are never carried.

### 3. Backup: save the Services the app's legacy pods are reached through

`apps backup` (`backup.Run`) and `backupBeforePrepare` (`internal/cli/apps_migrate.go:324`) also write every Service of the live namespace whose selector matches the captured workloads' pod labels, as `service.<name>.yaml` (no conflict with `workload.<kind>.<name>.yaml`, `backup.go:534-552`). Baseline mode (`baselineWorkloadEnvs`, `chart.go:130`) reads them for the analysis in 1. This also gives the undo copy that I33 lacked.

### 4. Docs

- `docs/migration-runbook.md:189-193`: the LB is carried in the patch and taken over by Helm; metrics leftovers are listed for D10.
- `internal/prepare/step_virtualizer.go:30-34`: comment updated (the LB is not this step's: the patch carries it).
- `CONTEXT.md`, the "Diff, migrate, backup, drift" section: the Services analysis and carry.
- `CHANGELOG.md`.
- No catalog field is added, so `config-reference.md` and the seed are unchanged.

### 5. Outside this repo (record only, do not implement here)

The `migrate` / `migrate-validate` skills live in `platform-claude-plugins` (worktree `/stratio/IA/.worktrees/platform-claude-plugins-gitops-migration`, installed as `~/.claude/plugins/cache/stratio-platform/stratio/<ver>/skills/`):
- the unit captures `before/net.<ns>.yaml` and `after/net.<ns>.yaml` contain only the kubectl error `arguments in resource/name form may not have more than one slash` (eosdev, `units/11-virtualizer/`), so no Service or Ingress snapshot exists for any unit. Fix the capture command;
- `migrate-validate` gets a "Services with no endpoints, LoadBalancer first" leftovers check that cites this plugin's new section.

## Interaction with TASK-18

`TASK-18` adds `service.loadbalancer` (default off) and the LB host SAN to the `virtualizer` chart (PR `Stratio/charts#258`). Nothing here waits for it:
- chart without the entry (eosdev today, any older release): the patch carries the whole entry;
- chart with the entry: the patch carries only `enabled: true`, `loadBalancerIP` and whatever else differs.

The certificate SAN is only in the chart (TASK-18): the plugin does not carry `altNames`.

## Tests to add

`internal/diff/servicepatch_test.go`, `internal/appdiff/services_test.go` (fake client, like `selector_test.go`), `internal/cli/review_test.go`, `internal/backup/backup_test.go`:
- LoadBalancer carried: key and `nameOverride` from the name, controller, ports (numeric `targetPort` equal to `port` omitted), `loadBalancerIP` from `status` when `spec` has none, CCT and `last-applied` annotations dropped, `Exclude` respected;
- NodePort carried with its `nodePort`;
- chart already renders the same name: only differing fields; all equal: nothing;
- not carriable: name without the `<fullname>-` prefix, no controller, named `targetPort` missing on the rendered pod; each gives the `[ACTION]` with its reason;
- ClusterIP leftover with a replacement (and `Monitored`), and without one;
- ignored: Flux-managed, owner-referenced, selector matching a rendered pod, empty selector, a Service of another app;
- after cutover (no legacy workload live): detected from the baseline backup and from a pure `cct.stratio.com/application_id` selector;
- backup writes `service.<name>.yaml` for the matching Services only.

## Acceptance

1. `go test ./...` passes with the cases above.
2. On eosdev, read-only: `flux stratio apps diff virtualizer` shows the LB as carried (`service.loadbalancer`, 10.202.18.2:13422) and the patch diff contains the block; `apps diff dg-hdfs-agent`, `eureka-agent`, `governance-datamarket-agent`, `psql-gosec-agent`, `opensearch1-gosec-agent` list their legacy metrics Service as a leftover with its replacement.
3. With the user's yes and the live LB YAML saved first: `apps migrate virtualizer`, push, force the Flux reconcile. Then:
   - the Service `stratio-apps/virtualizer-loadbalancer` keeps 10.202.18.2;
   - it has endpoints (the virtualizer server pod);
   - its selector has no `cct.stratio.com` key and it carries Helm ownership metadata (`meta.helm.sh/release-name: virtualizer`);
   - a SQL client connects to 10.202.18.2:13422.
4. Undo: remove the `service` block from the tenant patch and re-apply the saved YAML.

## Open points (to verify while implementing)

- **The takeover result on eosdev.** The helm-controller version on eosdev is not known here. If the takeover leaves the old selector key, or CCT metadata, on the Service (possible with server-side apply when another field manager owns the key), record it here and decide; do not patch the live object to hide it.
- **TLS:** whether the SQL clients use the IP or the LB host name. The host name needs the SAN that only `TASK-18` adds.
- **Metrics scraping:** `ServiceMonitor` coverage of the other charts' `-metrics` Services (`eureka-agent`, `governance-datamarket-agent`, `gosec-agent`): only `dg-agent` was checked.
- **Other components:** grep `kubernetes-universe/packages` for other descriptors that create `LoadBalancer` or `NodePort` Services the same way; the detection covers them generically, but list them here.

## Result on eosdev (2026-10-28)

Run with the branch at `161b97d`, chart `15.1.0-PR258-SNAPSHOT` from Jenkins build PR-258 #11, Flux helm-controller v1.5.5.

- **Acceptance 2 (read-only diff):** `apps diff virtualizer --as virtualizer/virtualizer` listed `stratio-apps/virtualizer-loadbalancer` (LoadBalancer, 10.202.18.2, 13422/TCP) as carried, with the `service.loadbalancer` block (`enabled`, `loadBalancerIP`, the `cluster-autoscaler.kubernetes.io/safe-to-evict` annotation). The installed older plugin printed "no differences". `dg-hdfs-agent`, `eureka-agent`, `governance-datamarket-agent`, `psql-gosec-agent` and `opensearch1-gosec-agent` each listed their legacy Service as a `[REVIEW]` leftover with its chart replacement, all scraped by a ServiceMonitor. Every other LoadBalancer or NodePort Service of the cluster (`ingress-nginx-controller`, `k8s-gateway`) is Helm-managed and was correctly not flagged.
- **Acceptance 3 (applied):** the block was added by hand to the virtualizer patch of the tenant file (keos-fleet `4c5a6481`), not through `apps migrate`, because `apps migrate` replaces the whole existing patch with one computed from live and would have dropped the connector jars and the monitor/ui resources. After the Flux reconcile: the Service kept 10.202.18.2 and nodePort 30897, its selector is the chart's three `app.kubernetes.io/*` labels with **no `cct.stratio.com` key** (`spec.selector` is an atomic map, so the apply replaced it), the port was renamed `lb-port` to `api`, it carries Helm ownership (`managed-by: Helm`, `meta.helm.sh/release-name: virtualizer`), the EndpointSlice has the server pod ready on 13422, the release went to v2 `UpgradeSucceeded`, and no virtualizer pod restarted. A TCP connection to 10.202.18.2:13422 opens and the server answers the TLS handshake (it then rejects a client without a certificate, as expected).
- **The takeover open point is closed:** Helm adopted the existing Service in place and the legacy selector key did not survive.
- **Found while testing, fixed:** the carried patch pinned `metallb.io/ip-allocated-from-pool`, which MetalLB writes itself (`161b97d` drops it).
- **Still open:** the running server pod keeps the certificate issued before the change, so it still lacks the LB host name; the SecretsBundle now declares `virtualizer-loadbalancer.stratio-apps.k8s.eosdev.labs.stratio.com` and the new certificate is used at the next pod restart (not done). Whether the SQL clients connect by IP or by host name was not tested.
- **Operational note:** a PR build republishes the same version string, and `HelmChart` objects use `reconcileStrategy: ChartVersion`, so Flux does not re-pull it (the chart artifact stayed at the 10-26 digest, and `flux reconcile source chart --with-source` did not change it; there is no `--force` for charts). Deleting the `HelmChart` object (the HelmRelease recreates it) made the cluster pull the new chart.
