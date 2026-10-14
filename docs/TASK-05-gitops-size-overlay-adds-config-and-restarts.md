# TASK-05 — the GitOps size overlay adds configuration the legacy install never had (hdfs1, kafka1, psql), the diff cannot see it, and the migration restarted the data plane

**Status: open. hdfs/kafka replication analysed 2026-10-13 (verdict and recommendation below, awaiting the user's decision); psql settings still open. No cluster change made for this finding.**
Found in the eosdev batch-migration audit of 2026-10-09 (see `TASK-00-eosdev-audit-index.md`).
Tools to change: `flux-stratio` (this repo, branch `feat/application-migration`) and the GitOps component
overlays in `keos-apps` (local worktree used by the plugin:
`/stratio/aws/flux/.worktrees/keos-apps-env-list-overlays`; repo checkout `/stratio/gitops/keos-apps`).

## Correction first

The published audit report says these settings are "most likely server-side defaulting; no listed field
manager owns them". **That was wrong.** The follow-up investigation traced them to the keos-apps size
overlay `S`, applied by Flux at 10:55:35Z. They appear unowned only because `nodeGroups` is an atomic list
in the CRDs, so `managedFields` shows just `f:nodeGroups: {}` under `kustomize-controller`.

## Requirement for this task

Read "Migration principle" in `TASK-00`. Keep each client's original configuration as close as possible;
welcome real improvements; assume neither direction. For **each** setting below, investigate whether the
GitOps value makes sense for the size and topology of the installation it lands on **and why**, record the
conclusion with evidence in this file before changing a tool, and classify it **preserve legacy**,
**accept new** or **needs a product decision**. The first verdicts are the audit's reading, not final.

## What happened

`apps migrate --all` wrote patches for the operator-backed datastores. Flux then reconciled the
Kustomizations (10:55:35Z) with the default size overlay for each:

- `apps-hdfs1`, `apps-kafka1`: `path: components/<hdfs|kafka>/app/overlays/default/S`
- `apps-psql`: `postgres/.../backup/physical/s3/S` (the Postgres size values come from
  `components/size/S/pgcluster-patch.yaml`)

(Resolve exact paths with `kubectl get kustomization apps-hdfs1 -n stratio-datastores -o
jsonpath='{.spec.path}'`; the audit abbreviated them.)

The tenant patches the plugin wrote never touched `config`, so the overlay values reached the CRs. The
plugin's `apps diff --baseline` said "no differences" for all of them and `apps migrate` printed nothing
about them, because the comparison only back-ports legacy values into a patch (see "Plugin behaviour").

## Audit facts (read-only subagent; verify what you rely on)

| Setting | Before (legacy CR / running) | Now | Set by | What it does | First verdict |
|---|---|---|---|---|---|
| hdfs `dfs.replication` (namenode and datanode node groups) | absent; Hadoop default 3 | `"1"` (`hdfs-site.xml` in namenode-0 shows 1) | overlay `hdfs/.../S/hdfscluster-patch.yaml:29,60` via env `DFS_REPLICATION` (operator maps `nodeGroup.Config.HDFS` to env vars, `hdfs-operator create.go:1328`) | replication of files whose writer sets none | **Reject (preserve legacy)**, see "Analysis: the two replication settings". `S` assumes 1 datanode; eosdev has 3; the HDFS is empty and clients default to 3. |
| kafka `offsets.topic.replication.factor` (both node groups) | absent; Kafka default 3 | `"1"` in `server.properties` | overlay `kafka/.../S/kafkacluster-patch.yaml:6,33` via env `KAFKA_EXTRA_CONFIG` | RF of `__consumer_offsets` at creation only | **Reject; fix in keos-apps**, see the analysis. No effect on the existing topic (all 3 brokers hold all 50 partitions, so it is RF 3), but a recreated or new topic gets RF 1 although S has 3 brokers. |
| pg `work_mem` | 4MB | 2427kB | `components/size/S/pgcluster-patch.yaml:26` (M and L overlays set 4854kB / 9709kB) | per-sort memory | **Decision.** 40% lower than legacy. |
| pg `max_wal_size` / `min_wal_size` | 1GB / 80MB | 4GB / 1GB | same overlay | fewer checkpoints, more WAL on disk | Accept, but check the 10Gi WAL volume against `wal_keep_size` 4GB plus 4GB. |
| pg `huge_pages` | try | off, **pending restart** | same overlay | `try` falls back to off without reserved hugepages | Accept, neutral. |
| pg `default_statistics_target` | 100 | 100 | same overlay | equals the Postgres default | Accept, no-op. |
| `backup.type`, `stanza`, `bootstrap.pgbackrest.config`, `repository.path`, s3 bucket/endpoint | absent | present | overlays `backup/physical/s3`, `restore/physical/s3` | read only when `walArchiver` is true; the tenant patch keeps it `false`, bootstrap type is `initdb` | Accept, inert (re-check if someone enables `walArchiver`). |

### Side effects of the migration (verified by the audit)

- **HDFS and Kafka restarted.** ControllerRevision diffs show the only change is `DFS_REPLICATION=1` /
  `KAFKA_EXTRA_CONFIG` (image and resources equal). hdfs namenode and datanode revisions were created at
  10:55:36Z, kafka broker and controller revisions at 10:55:38Z; those pods restarted at about 10:56.
  Operator logs "Created or updated" at 10:55:35Z (hdfs) and 10:55:38Z (kafka). Journalnodes, zk and
  opensearch were untouched (generation 1, ~24h old).
- **Postgres did not restart, but needs one.** Pods are 25h old with 0 restarts. Patroni log at 10:57:06:
  `Changed work_mem from '4MB' to '2427kB'`, `max_wal_size`/`min_wal_size` changed by reload, and
  `huge_pages cannot be changed without restarting`. The PgCluster status is `WARNING`: "needs to be
  restarted". The `psql` StatefulSet has an unrolled revision `psql-847448b674` (only adds Flux labels) and
  update strategy `OnDelete`. The cluster is left in that state; nothing was restarted by the audit.
- A new `pgbackuprepository` (PgBackupRepository + StatefulSet) was created at 10:55:35Z by
  `apps-pgbackuprepository` (new workload, not a legacy equivalent).
- **Not verified:** which HDFS clients write files without their own replication setting; the replication of
  existing files (`fsck` needs Kerberos); whether any node has hugepages reserved; live Postgres
  `pg_settings` (password auth blocked it; Patroni logs and `postgresql.conf` were used); WAL volume usage;
  the kafka operator source and webhook code (not found); why the Flux apply on the psql CR is stamped
  11:02:03Z while the operator validated the update at 10:57:05Z.

## Analysis: the two replication settings (hdfs `dfs.replication`, kafka `offsets.topic.replication.factor`)

Done 2026-10-13, read-only. psql settings (`work_mem`, WAL sizes, `huge_pages`) are **not** analysed here and stay open.

### What the legacy install had

Pre-migration backups (`~/.fluxcd/flux-stratio/backups/{hdfs1,kafka1}/2026-10-09T10-51-40Z/cr.yaml`):

- hdfs1: journalnode 3, namenode 2, datanode 3 instances; `config` of namenode/datanode holds only `jvm`, **no `dfs.replication`**.
- kafka1: controller 3, broker 3 instances, `config: {}` on both groups, **no replication settings**.
- The legacy descriptors (`apps-layer-automation/apps/hdfs`, `apps/kafka`) have no replication parameter either. The legacy "size S" only changed
  cpu/memory (`apps/{hdfs,kafka}/sizes/S`); instance counts stayed at `DATANODE_INSTANCES=3` (`hdfs/vars.env:18`), 3 controllers, 3 brokers.
- So in legacy both used the product defaults: HDFS `dfs.replication` 3 and Kafka `offsets.topic.replication.factor` 3.

### Where the GitOps values come from and what they assume

`keos-apps` overlays (`/stratio/aws/flux/.worktrees/keos-apps-env-list-overlays/components/{hdfs,kafka}/app/overlays/default/{S,M,L}`), tenant size `S`
(`keos-fleet/.../stratio.yaml:12`):

| Overlay | HDFS | Kafka |
|---|---|---|
| S | `dfs.replication: "1"` on namenode and datanode groups; **1 datanode, 1 journalnode, 1 zookeeper** (`824a020` "HDFS instances for S size" lowered 3 -> 1 and removed the key from the journalnode group but kept it on the others) | `offsets.topic.replication.factor: "1"` on controllers and brokers, **with 3 controllers and 3 brokers** |
| M / L | no `dfs.replication` (Hadoop default 3); 3 datanodes (M) / 5 (L) | `offsets.topic.replication.factor: "3"` explicit |

- HDFS S is internally consistent: one datanode, so replication 1. The legacy deployment never used that topology. The eosdev tenant patch restores the legacy instance
  counts (journalnode 3, datanode 3, zk 3) because the plugin back-ports values that **differ**, but it cannot back-port the **absence** of a key, so the overlay's
  `dfs.replication: "1"` stays. Result: 3 datanodes running a profile designed for 1.
- Kafka S is **not** consistent: it asks for 3 brokers and sets offsets RF 1. Introduced in `8be7b38` (PLT-4023 "Integrate Kafka", no rationale in the message);
  `8b1ed78` only fixed the broker patch index. Nothing in git history or the docs explains the 1.
- Origin of the live values (verified): HDFS operator turns `nodeGroup.config.hdfs` into env vars (`hdfs-operator/internal/controller/hdfscluster/create.go:1328-1334`,
  `dfs.replication` -> `DFS_REPLICATION`) and `hdfs-site.xml` in `hdfs1-namenode-0` has `dfs.replication=1`. Kafka: broker env `KAFKA_EXTRA_CONFIG=offsets.topic.replication.factor=1`
  and `/opt/kafka/conf/server.properties:3` has it. The kafka operator source is not available locally.

### What the settings do, and what they affect on eosdev today

**HDFS `dfs.replication`** (Hadoop `hdfs-default.xml`: "Default block replication. The actual number of replications can be specified when the file is created.
The default is used if replication is not specified in create time"). It is a *default*, per file, and is read from the configuration of whoever creates the file:
`DistributedFileSystem.getDefaultReplication()` returns `dfs.getConf().getDefaultReplication()`, i.e. the **client** configuration (Hadoop 3.4 source). The NameNode-side value only
matters for creators that rely on the server's configuration (WebHDFS/HttpFS creates without a `replication` parameter, tools run inside the namenode/datanode pods); this last point is from
general Hadoop behaviour, not tested here.

- Clients on eosdev (virtualizer/Spark, dg-hdfs-agent, dlc) get their `hdfs-site.xml` from the replicated ConfigMap `hdfs1-stratio-datastores-config`
  (copies in `stratio-apps`, `stratio-dlc`). **It does not contain `dfs.replication`** (grepped: no match), so those clients create files with the Hadoop client default, 3.
- The HDFS is **empty**: the CR status shows `numBlocks: 0` on all three datanodes, 0 missing blocks, 0 corrupt; the block pool was created 2026-10-08. Nothing is stored with RF 1 today.
- Replication of existing files never changes by editing the default (only `setrep` does), and there are no files.
- Risk that remains: any server-side-default writer gets RF 1 on a 3-datanode cluster, so losing one datanode loses data; and a future reader of the configuration sees "1".

**Kafka `offsets.topic.replication.factor`** (Kafka doc: "The replication factor for the offsets topic (set higher to ensure availability). Internal topic creation will fail until the
cluster size meets this replication factor requirement"; default 3). It is read **only when `__consumer_offsets` is created**, which is when the first consumer group commits.

- On eosdev the topic already exists and is RF 3: each of the three brokers holds all 50 `__consumer_offsets` partition directories (`/var/kafka/data/broker`), plus the operator's
  `stratio-healthcheck` topic. The key therefore has **no effect today**.
- Nothing in the cluster uses `kafka1` except its own operator (no other workload or ConfigMap references it).
- It bites when the topic is created again: a fresh Kafka install with overlay S, or someone deleting `__consumer_offsets`. A new topic would be RF 1, so each partition lives on one broker.
  Then every broker restart (each rolling upgrade, which the operator does) makes the consumer groups coordinated by that broker unavailable, and a lost broker disk loses the committed offsets
  and group metadata of those groups, so consumers restart from earliest/latest. The brokers' own `transaction.state.log` stays at the Kafka default 3, so the cluster is inconsistent with itself.

### Verdict

| Setting | Verdict | Reason |
|---|---|---|
| hdfs `dfs.replication: "1"` on eosdev | **Reject (preserve legacy)** | Not in legacy; overlay S was designed for 1 datanode and eosdev has 3. No data and no known client depends on it today, so removing it is cheap now and gets more expensive once data exists. |
| hdfs `dfs.replication` in overlay S for a real 1-datanode tenant | Keep | Consistent with S. Needs the plugin to drop it whenever the patch raises the datanode count. |
| kafka `offsets.topic.replication.factor: "1"` in overlay S (3 brokers) | **Reject; fix in keos-apps** | Contradicts S's own 3 brokers, no rationale in history, M/L use 3. Latent on eosdev (topic exists at RF 3), harmful on any new install. |

Other differences of the same overlays (resources, storage 4Gi/10Gi, JVM options) are the legitimate size profile and are already carried by the tenant patch where they differ.

### Recommendation

1. **keos-apps (kafka S):** change `offsets.topic.replication.factor` to `"3"` in `kafka/app/overlays/default/S/kafkacluster-patch.yaml` (groups 0 and 1), as M and L do. Zero effect on existing
   clusters (the key is only read at topic creation) and it stops new installs from creating an RF 1 offsets topic. If a real single-broker profile is wanted, make it an explicit variant
   whose broker count and RF are set together.
2. **keos-apps (hdfs S):** keep `dfs.replication: "1"` while S means one datanode. Do not "fix" it by adding it to M/L. Document in the overlay that it assumes `datanode.instances: 1`.
3. **eosdev (tenant file), when the user schedules it:** add JSON 6902 `remove` ops for `/spec/nodeGroups/1/config/hdfs` and `/spec/nodeGroups/2/config/hdfs` on hdfs1 and for
   `/spec/nodeGroups/0/config/kafka` and `/spec/nodeGroups/1/config/kafka` on kafka1, so the CRs match the backup. This **rolls the namenodes, datanodes and kafka brokers/controllers once
   more** (the operator drops the env var from the StatefulSets). The HDFS is empty and kafka holds only offsets and the healthcheck topic, so the risk is low; pick a quiet moment and
   check `kubectl -n stratio-datastores get hdfscluster,kafkacluster` for `OK` between steps. Do not apply it before item 1 is merged if the tenant may be re-created from the overlay.
4. **Plugin (this is the general fix, see work items):** a patch that raises `datanode.instances` above the overlay value, or any GitOps-only key that lowers durability
   (`dfs.replication`, `*.replication.factor`, `min.insync.replicas`), must be reported by `apps diff` / `apps migrate` and stop `--yes`. This is the case where "no differences" was misleading.

### Not verified for this analysis

- The effective client configuration inside a Spark/virtualizer pod (only the source ConfigMap was read); no file was written to the cluster to test the default.
- The NameNode-side use of `dfs.replication` for WebHDFS/HttpFS creates (general Hadoop behaviour).
- Kafka operator behaviour on config removal (rollout order), and whether the operator itself needs `offsets.topic.replication.factor` for its healthcheck.

## Plugin behaviour today (facts for the implementer)

- `ManifestDiff` and `DeepDiff` (`internal/diff/manifest.go`, and the `ToJSON6902Ops` doc: "a key only in
  local is skipped") are **one-directional**: keys present only in the rendered GitOps state are skipped. So
  `--baseline` printing "no differences" means nothing needed back-porting, **not** that the migration kept
  the legacy configuration. GitOps-only additions are invisible in `apps diff`, `apps migrate` and `--yes`.
- `apps diff psql` (default mode) lists ~127 lines while hdfs1/kafka1/pool-psql say "no differences". The
  psql patch is non-nil: it holds only the s3 bucket and endpoint. In the plugin's render they stay as
  `${BACKUPS_S3_BUCKET_NAME}` while live has `eosdev-backups` / `eu-north-1`, because the RSIP substitute
  value is itself a `${...}` the render does not expand (`resourceset-apps-datastores.yaml:531`). The other
  ~120 lines are the unfiltered `Before`/`After` spec text (`internal/appdiff/manifest.go`), not patch
  content. In `--baseline` mode the backup has no bucket, so the diff comes out empty.
- `--drift` (`internal/drift/drift.go`, `compareSpec`) is a plain spec text diff of two captures with no
  ignore list; the catalog `exclude` only filters patches. Operator-defaulted keys sit on both sides and
  cancel out, so drift only shows real changes. Nothing on these CRs is operator-managed.

## Work items

### keos-apps (GitOps component overlays) — decide first, then change
1. Investigate why the `S` overlays set `dfs.replication=1` and `offsets.topic.replication.factor=1`
   (single-node assumption) and how the size is chosen per tenant. Decide whether size `S` is right for
   eosdev, and whether durability keys should follow the instance count (or live in an explicit
   single-node variant) so a multi-node install never inherits them. Apply the same question to the
   Postgres tuning values (`work_mem`, WAL sizes, `huge_pages`) against what a CCT-installed cluster ran.
2. Check the M and L overlays for the same single-node assumptions.

### Plugin (`flux-stratio`)
1. **Report GitOps-only keys.** In `apps diff` and `apps migrate`, list keys the render adds that the live or
   backup object lacks (the reverse of what the patch carries) as an informational review section, like the
   chart-mode `Excluded` list. Group them by origin (overlay path when known), and make durability-related
   ones visible. Decide with the user whether any should stop `--yes`.
2. **Say when a migration will restart workloads.** Detect changes that alter a StatefulSet/Deployment pod
   template (env, config hashes) or require a database restart (`pending restart`), and print an
   `[ACTION]`/`[NOTE]` before the user applies the patch. At least for the operator CRs, compare the
   rendered pod-affecting fields with the backup.
3. **Expand nested `${VAR}` substitutions** (`postBuild.substitute` values that are themselves `${...}`)
   when rendering, so `apps diff psql` stops reporting the s3 bucket/endpoint and the default-mode noise
   disappears. Also stop printing the unfiltered spec `Before`/`After` text when the patch is empty or small.
4. **Optional, ask the user:** a legacy-preserving mode that emits JSON 6902 `remove` ops (or sets legacy
   values) for GitOps-only keys the user chooses to reject, driven by the report from item 1.
5. **Docs:** state in `CONTEXT.md` section 6, `docs/migration-runbook.md` step 6 and the `apps diff --help`
   text that "no differences" in `--baseline` does not cover GitOps-only additions.

### Tenant file / cluster (interim, eosdev only, NOT applied)
Anyone who needs the legacy values before the keos-apps change lands can add JSON 6902 `remove` ops for
`/spec/nodeGroups/{1,2}/config/hdfs` on hdfs1 and `/spec/nodeGroups/{0,1}/config/kafka` on kafka1, and set
the legacy `work_mem`, `max_wal_size`, `min_wal_size` and `huge_pages` back in the psql patch. Removing the
HDFS and Kafka keys will roll those pods again. The psql `huge_pages` restart is still pending; schedule it
deliberately (Patroni switchover), do not leave it to chance.

## Acceptance criteria

- The settings table ends with a recorded final verdict per row, with evidence, and the open decisions are
  listed for the user at the end.
- Plugin tests: a render with a GitOps-only key (for example `config.hdfs.dfs.replication`) that the legacy
  CR lacks produces the new review entry in `apps diff` and `apps migrate` and, if agreed, stops `--yes`; an
  unchanged key produces nothing; a template change that alters a StatefulSet pod template produces the
  restart notice; nested `${VAR}` substitutions expand and the psql default diff on a fixture with the
  eosdev values has no bucket/endpoint noise.
- If a keos-apps change is made: `flux-operator build rset` of the datastore resourcesets renders the
  expected overlay for a 3-node tenant, and `apps diff --drift` against a fresh backup shows no durability
  change after rollout.
- `CONTEXT.md`, `docs/migration-runbook.md`, `docs/config-reference.md` (if the catalog changes) and
  `CHANGELOG.md` updated; `go test ./...` passes.

## How to reproduce

```
flux stratio apps diff hdfs1 --baseline ~/.fluxcd/flux-stratio/backups/hdfs1/2026-10-09T10-51-40Z   # "no differences"
flux stratio apps diff hdfs1 --drift    ~/.fluxcd/flux-stratio/backups/hdfs1/2026-10-09T10-51-40Z   # dfs.replication "1" appears
flux stratio apps diff kafka1 --drift   ~/.fluxcd/flux-stratio/backups/kafka1/2026-10-09T10-51-40Z
flux stratio apps diff psql             # default mode: ~127 lines, bucket/endpoint placeholder noise
kubectl -n stratio-datastores get kustomization apps-hdfs1 apps-kafka1 apps-psql -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.path}{"\n"}{end}'
kubectl -n stratio-datastores get pgcluster psql -o jsonpath='{.status}'    # needs restart
```

## Open questions for the user

- Is size `S` the intended size for eosdev, given 3 datanodes and 3 brokers?
- For Postgres: preserve the legacy tuning (`work_mem` 4MB, WAL 1GB/80MB) or accept the overlay? When should
  the pending `huge_pages` restart happen?
- Should a GitOps-only key that lowers durability stop `apps migrate --yes`?

## Related

- `TASK-00` index; `TASK-04` (live-only variables the chart does not render: the mirror image of this task);
  `docs/TASK-keos-apps-env-list-overlays.md` (overlays and env lists); `TASK-01` (prepare steps and side
  effects).
- Audit artifact with all raw outputs: https://claude.ai/artifact/7TbG1ew9A6CGCMUDShFDx4 (its finding-5 text
  needs the correction above).
