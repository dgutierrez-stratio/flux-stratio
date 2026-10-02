package discovery

import "k8s.io/apimachinery/pkg/runtime/schema"

// kustomizationGVK and helmReleaseGVK are the same well-known Flux GVKs
// already used elsewhere in this repo (e.g.
// internal/tenantimport/helmreleasescan.go's helmReleaseGVK).
var (
	kustomizationGVK = schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"}
	helmReleaseGVK   = schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}
)

// workloadGVKs are the Kubernetes workload kinds a live app's chart might
// deploy as — the same 3 kinds internal/appdiff/chart.go's workloadKinds
// already treats as one "workload" concept for diffing.
var workloadGVKs = []schema.GroupVersionKind{
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "apps", Version: "v1", Kind: "StatefulSet"},
	{Group: "apps", Version: "v1", Kind: "DaemonSet"},
}

// crdGVKs are the 14 Stratio operator CRDs the legacy Python migration
// client's discovery.py hardcodes as OPERATOR_CRDS, so a manifest-mode
// app's live custom resource can be found regardless of which of these
// kinds it is. Group/Kind/Version were verified directly against the live
// eosdev cluster's installed CRDs (kubectl get crd <name> -o
// jsonpath='{.spec.names.kind}'/'{.status.storedVersions[0]}') rather than
// copied verbatim from Python's own, differently-cased dict values.
var crdGVKs = []schema.GroupVersionKind{
	{Group: "hdfs.stratio.com", Version: "v1", Kind: "HDFSCluster"},
	{Group: "idp.stratio.com", Version: "v1", Kind: "IdpCluster"},
	{Group: "kafka.stratio.com", Version: "v1", Kind: "KafkaCluster"},
	{Group: "kafka.stratio.com", Version: "v1", Kind: "KafkaTopic"},
	{Group: "opensearch.stratio.com", Version: "v1", Kind: "OsBackup"},
	{Group: "opensearch.stratio.com", Version: "v1", Kind: "OsCluster"},
	{Group: "opensearch.stratio.com", Version: "v1", Kind: "OsDashboards"},
	{Group: "opensearch.stratio.com", Version: "v1", Kind: "OsDashboardsTenant"},
	{Group: "postgres.stratio.com", Version: "v1", Kind: "PgBackupRepository"},
	{Group: "postgres.stratio.com", Version: "v1", Kind: "PgBackup"},
	{Group: "postgres.stratio.com", Version: "v1", Kind: "PgBouncer"},
	{Group: "postgres.stratio.com", Version: "v1", Kind: "PgCluster"},
	{Group: "postgres.stratio.com", Version: "v1", Kind: "PgDatabase"},
	{Group: "postgres.stratio.com", Version: "v1", Kind: "PgReplicationSlot"},
}
