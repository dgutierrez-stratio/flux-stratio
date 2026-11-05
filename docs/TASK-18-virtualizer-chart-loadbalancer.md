# TASK-18 — the `virtualizer` chart cannot expose the server through a LoadBalancer Service, a feature the CCT descriptor offered (`createLoadBalancer`)

**Status: implemented 2026-10-28 in the PR #258 branch, commit `b8471b8` (`virtualizer/values.yaml`, `virtualizer/templates/common.yaml`, README, `helm-template-examples.md`, `ci/loadbalancer-values.yaml`, CHANGELOG), committed locally and not pushed yet. Checked with `helm template`: disabled, the render is byte-identical to the branch head for s3 and hdfs storage; enabled, the only differences are the Service `virtualizer-loadbalancer` (LoadBalancer, 13422, the `virtualizer` Deployment's selector, `loadBalancerIP` as given) and the SAN on the server certificate of the bundle in use. `helm lint` passes with both CI values files. `bin/check_descriptors.sh virtualizer` fails 0/3 with "unrendered '{{ }}'" both before and after the change (the Vault agent templates); the count of unrendered markers is the same.**

Implementation note: the SAN value is `service.loadbalancer.extraSAN` (default `<release>-loadbalancer.<namespace>.<cluster.externalDomain>`); it is added in `templates/common.yaml` to every certificate of the `virtualizer` and `virtualizer-no-credentials` bundles while `service.loadbalancer.enabled` is true.

Found in the eosdev legacy->GitOps run of 2026-10-26/27 (migration report finding **I33**; run folder `~/.fluxcd/flux-stratio/runs/eosdev-stratio/`).
Repo to change: **`Stratio/charts`**, chart `virtualizer`. Deliver in the **existing open PR https://github.com/Stratio/charts/pull/258** ("[NOJIRA] Align charts with kubernetes-universe 15.1.0 release", branch `dgutierrez-stratio/align-15.1.0` -> `master`), which already changes `virtualizer/values.yaml` and `virtualizer/README.md`. **No new branch or PR.** Local worktree of that branch: `/stratio/aws/flux/.worktrees/charts-align-15.1.0` (clean, level with origin at `a915226` on 2026-10-28).
`keos-apps` needs no change (see "What to do", item 5).
Related: `TASK-17` (flux-stratio: `apps diff` detects legacy Services, and the migration patch carries a legacy LoadBalancer so Helm takes it over). This task is **not a blocker** for TASK-17; it makes the LoadBalancer a documented, first-class option of the chart and adds the certificate SAN the plugin cannot add.

## What the legacy product did

`kubernetes-universe/packages/virtualizer/virtualizer-default.json` (local checkout `/stratio/p/kubernetes-universe`, branch `branch-15.1`, commit `f14a1355`; descriptor `virtualizer:default`, parent `stratiobase:default` 15.1.1-SNAPSHOT). Section "Public access" of the parameters:

| Parameter | Default | Line (about) | Notes |
|---|---|---|---|
| `createLoadBalancer` | `false` | 1471 | "Create LoadBalancer service to expose Virtualizer (13422 port)" |
| `loadBalancerK8sServiceName` | `${Application.name}-loadbalancer` | 1482 | visible only when enabled, not updatable |
| `loadBalancerExtraSANToAddToCertificate` | `${loadBalancerK8sServiceName}.${Application.domain}.${external_domain}` | 1499 | "should match the hostname used to access the service" |

Lifecycle tasks (install around line 5451, update around 5896, upgrade around 6367), "Creating Load Balancer Service", type `kubernetes/customresource/update`, `enabled: ${createLoadBalancer}`:

```
apiVersion: v1
kind: Service
name: ${loadBalancerK8sServiceName}
namespace: ${Application.domain}
spec:
  ports: [{name: lb-port, protocol: TCP, port: 13422, targetPort: 13422}]
  selector: {cct.stratio.com/application_id: ${Application.id}}
  sessionAffinity: None
  type: LoadBalancer
```

A matching "Deleting Load Balancer Service" task runs when it is disabled (around lines 5463, 5908, 6379). The server certificate task `applicationCertificate` (around line 4981) puts `${loadBalancerExtraSANToAddToCertificate}` in its `altNames`:
`vr-local,${Application.name},${Application.id}.${eos.internalDomain},${Application.id}.${external_domain},${loadBalancerExtraSANToAddToCertificate},${virtualizerSANServerCertificate}`.

## What the chart does today (branch of PR #258)

- `virtualizer/values.yaml:560` `service:` has only `virtualizer` (ClusterIP, `controller: virtualizer`, ports `api` 13422, `health` 13423, `sparkui` 4440, `metrics` 4090) and `dns` (headless: `remoting` 2552, `hazelcast` 5701, `management` 8558). **No LoadBalancer.**
- `virtualizer/values.yaml:777-` `secretsBundle`: the server certificate is in `virtualizer` (non-hdfs storage) with `altNames` `vr-local`, `<release>`, `<release>.<ns>`, `<release>.<ns>.svc.<cluster.domain>`, and in `virtualizer-no-credentials` (hdfs storage, `:805-`) with `vr-local`, `<release>-<ns>`, `<release>-<ns>.<ns>`, `*.<ns>`. **No load-balancer SAN** in either.
- The `common` library already supports everything needed, so **no library change**:
  - Service name `<fullname>-<nameOverride>` (`common/templates/lib/service/_valuesToObject.tpl:12-15`); `virtualizer/templates/common.yaml` sets `global.nameOverride` to the release name, so `nameOverride: "loadbalancer"` renders `virtualizer-loadbalancer`, the legacy default name;
  - `type: LoadBalancer` with `loadBalancerIP`, `loadBalancerSourceRanges`, `loadBalancerClass`, plus `externalTrafficPolicy`, `sessionAffinity`, `annotations` (`common/templates/classes/_service.tpl:16-75`);
  - certificates are rendered as `certificates: {{ tpl (toYaml .) $rootContext }}` (`common/templates/classes/_secretsBundle.tpl:48-49`): each `altNames` entry is a template string.
- `cluster.externalDomain` is already a value of the chart (used at `virtualizer/values.yaml:1024`; set by keos-apps from `${CLUSTER_EXTERNAL_DOMAIN}`, `keos-apps/components/virtualizer/app/base/helm.yaml`).

## What happened on eosdev

The tenant had `createLoadBalancer=true`: Service `stratio-apps/virtualizer-loadbalancer`, external address **10.202.18.2:13422**, used by SQL clients. After the migration (2026-10-26 11:19Z) the chart's pods carry `app.kubernetes.io/{name,instance,component}` and no CCT label, the chart renders no such Service, and Flux manages only what the chart renders. So the legacy Service selects nothing and the external address does not answer (decision U15: left as is until a fix exists). Nothing in Git declares it.

## What to do

1. **`virtualizer/values.yaml`**, under `service:`, add (default off):
   ```yaml
   loadbalancer:
     # -- Expose the server outside the cluster on 13422 (the legacy CCT "createLoadBalancer").
     enabled: false
     controller: virtualizer
     nameOverride: "loadbalancer"
     type: LoadBalancer
     # loadBalancerIP: ""        # pin the external address (keeps it across a recreation)
     # annotations: {}           # provider-specific (address pool, internal LB, ...)
     ports:
       api:
         enabled: true
         port: 13422
         protocol: TCP
   ```
   Keep the port name `api` (the chart's), not the legacy `lb-port`: nothing found refers to the port by name.
2. **Certificate SAN.** When `service.loadbalancer.enabled`, add the load-balancer host name to the server certificate of **both** bundles (`virtualizer` and `virtualizer-no-credentials`), default `<release>-loadbalancer.<namespace>.<cluster.externalDomain>` (the descriptor's default), overridable by a new value (e.g. `service.loadbalancer.extraSAN`, or under `server.`; pick one and document it).
   Trap: an `altNames` entry written as `'{{ if ... }}...{{ end }}'` renders as an **empty** entry when disabled, which the SecretsBundle may reject. Prefer appending the entry to the two lists in `virtualizer/templates/common.yaml` before `bjw-s.common.loader.generate` runs, the way that file already `mergeOverwrite`s `global.nameOverride`; or always include the name (harmless, simplest). Note in the PR that changing `altNames` makes the certificate be reissued, which may restart the server.
3. **Docs:** `virtualizer/README.md`, a short section "Expose the server through a LoadBalancer" (what it renders, the SAN, pinning the address, and that a cluster migrated from CCT keeps its legacy Service because Helm takes it over by name); `virtualizer/helm-template-examples.md`, one example with it enabled.
4. **CI:** add `virtualizer/ci/loadbalancer-values.yaml` (only `ci/default-values.yaml` exists today) with `service.loadbalancer.enabled: true` and a `loadBalancerIP`. Check with `helm template` (dependencies built as the repo's `Makefile` does):
   - enabled: one Service `virtualizer-loadbalancer`, `type: LoadBalancer`, port 13422, selector equal to the `virtualizer` Deployment's `spec.selector.matchLabels`, `loadBalancerIP` as given; the SAN present in the rendered SecretsBundle of the storage type in use;
   - disabled: the render is identical to the branch head before the change.
5. **keos-apps:** nothing. The entry is off by default; a tenant enables it through its HelmRelease values patch in the tenant file (the flux-stratio migration writes it, TASK-17). No `${...}` variable is needed.

## Takeover of an existing legacy Service (why no deletion is needed)

A cluster migrated from CCT already has `virtualizer-loadbalancer`, with no Helm ownership metadata. The Flux helm-controller's `install.disableTakeOwnership` / `upgrade.disableTakeOwnership` default to `false`, so Helm takes the existing object over instead of failing with "invalid ownership metadata"; on eosdev Flux took over legacy objects of the same name in place (psql, pooler, hdfs, kafka, opensearch, dashboards, and the same-named Services such as `discovery`). The object, and with it the external address, stay. To be confirmed on the first real migration (see TASK-17, "Open points"): that no `cct.stratio.com/application_id` key survives in the selector after the takeover.

## Interaction with TASK-17

- Chart **without** this entry (any release before this PR): the flux-stratio patch carries the whole `service.loadbalancer` block.
- Chart **with** it: the patch carries only what differs (`enabled: true`, `loadBalancerIP`, annotations).
- The SAN is only here: the plugin never carries certificate `altNames`.

## Acceptance

- `helm template` checks of item 4 pass, and the repo's own lint/CI for the chart passes on the PR.
- A tenant with `service.loadbalancer.enabled: true` gets `<release>-loadbalancer` with endpoints from the server pod, and a TLS connection to the LB host name validates against the server certificate.
- On a cluster migrated from CCT with the legacy Service present, the migration ends with the same external address, the Service owned by Helm/Flux, and no `cct.stratio.com/*` reference on it.
- One commit on `dgutierrez-stratio/align-15.1.0` (suggested: `[NOJIRA] virtualizer: optional LoadBalancer Service (CCT createLoadBalancer)`), pushed and added to the PR #258 description only with the user's yes.

## Evidence

- eosdev run folder `units/11-virtualizer/`: `findings.tsv` (I33); `after/workloads.txt` (the chart's Deployments and their `app.kubernetes.io/*` selectors). The `before/` and `after/net.stratio-apps.yaml` captures are empty (a kubectl error; see TASK-17, item 5), so the live legacy Service YAML was never saved: save it before the first change on eosdev.
- Migration report: I33, I34; decision U15 ("leave it until D10").
