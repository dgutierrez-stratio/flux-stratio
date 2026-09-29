package config

// Well-known CCT metadata a legacy (Ansible/CCT-deployed) Stratio
// application carries on its primary object — the most reliable way to
// tell which kind of component a live object is, since object names are
// environment-specific and often shared across kinds (a "genai" PgDatabase
// lives alongside the "genai" chart's own Deployments).
const (
	annService = "cct.stratio.com/application_service"
	annModel   = "cct.stratio.com/application_model"
)

// Kinds seeded types match on.
const (
	kindDeployment   = "apps/v1/Deployment"
	kindPgCluster    = "postgres.stratio.com/v1/PgCluster"
	kindPgBouncer    = "postgres.stratio.com/v1/PgBouncer"
	kindOsCluster    = "opensearch.stratio.com/v1/OsCluster"
	kindOsDashboards = "opensearch.stratio.com/v1/OsDashboards"
	kindKafkaCluster = "kafka.stratio.com/v1/KafkaCluster"
	kindHDFSCluster  = "hdfs.stratio.com/v1/HDFSCluster"
)

// ResourceSet templates seeded types live in, relative to keos-use-cases.
const (
	rsetApps         = "apps/components/resourceset-apps-apps.yaml"
	rsetDatastores   = "apps/components/resourceset-apps-datastores.yaml"
	rsetDLC          = "apps/components/resourceset-apps-dlc.yaml"
	rsetGenAI        = "apps/components/resourceset-apps-genai.yaml"
	rsetIntelligence = "apps/components/resourceset-apps-intelligence.yaml"
	rsetRocket       = "apps/components/resourceset-apps-rocket.yaml"
)

// gosecAgentEntry recovers a gosec agent's parent postgres/opensearch
// entry name from its live Deployment's name, whichever legacy naming
// convention deployed it ("psql-agent" or "opensearch1-gosec-agent" were
// both seen live).
const gosecAgentEntry = `{{ .Live.Name | trimSuffix "-gosec-agent" | trimSuffix "-agent" }}`

// SeedCatalog returns flux-stratio's known Stratio component catalog — the
// application set the legacy Python migration client's config.json
// shipped, translated into typed components. It's what `flux stratio
// config init` writes to disk. Review it before use: an environment may
// run components this catalog doesn't know about yet.
//
// Every selector was derived from the CCT annotations on real captured
// legacy objects (the eosdev backups, the Python client's own backups, and
// the live eosdev cluster).
//
// Compared to the old per-instance app list, renamed/previousNamespace are
// gone (the live object's own name and namespace are used), and dg-agent
// now covers every dg-agent-chart instance (hdfs and rdbms flavors alike)
// instead of one hardcoded dg-hdfs-agent.
func SeedCatalog() Catalog {
	return Catalog{Types: []ComponentType{
		{
			Type:      "postgres",
			Name:      "Postgres",
			Component: "postgres",
			Rset:      rsetDatastores,
			Match:     cctMatch(kindPgCluster, "Postgres", ""),
			Exclude: []string{
				"spec.bootstrap.pgBackup",
				"spec.bootstrap.pgbackrest.repository.azure",
				"spec.bootstrap.pgbackrest.repository.gcs",
			},
		},
		{
			Type:      "pgbouncer",
			Name:      "Postgres pgbouncer",
			Component: "pgbouncer",
			Rset:      rsetDatastores,
			Match:     cctMatch(kindPgBouncer, "PgBouncer", ""),
		},
		{
			Type:      "postgres-gosec-agent",
			Name:      "Postgres gosec agent",
			Component: "postgres",
			Rset:      rsetDatastores,
			Match:     cctMatch(kindDeployment, "pg-gosec-agent", ""),
			Entry:     gosecAgentEntry,
			Object:    "{{ .Entry }}-gosec-agent",
			Chart:     &Chart{Path: "charts/gosec-agent"},
			Exclude: []string{
				"spec.values.gosecAgent.environment.domainsConfig.mappingUrl",
				"spec.values.gosecAgent.environment.agent.agentServiceName",
				"spec.values.gosecAgent.general.serviceId",
				"spec.values.gosecAgent.environment.vault.vaultRole",
			},
		},
		{
			Type:      "opensearch",
			Name:      "Opensearch",
			Component: "opensearch",
			Rset:      rsetDatastores,
			Match:     cctMatch(kindOsCluster, "Opensearch", ""),
			Exclude:   []string{"spec.image"},
		},
		{
			// The CR owns its Deployment, Service and Ingress (the operator
			// manages them), and Flux adopts the same-named CR in place: no
			// prepare step needed. No excludes, deliberately: spec.image
			// (the legacy version pin) and spec.exposition.host (the legacy
			// admin.<tenant>.<domain> URL, vs. GitOps' admin.<domain>) are
			// both kept, so cutover changes neither version nor URL.
			Type:      "opendashboards",
			Name:      "Opensearch dashboards",
			Component: "opendashboards",
			Rset:      rsetDatastores,
			Match:     cctMatch(kindOsDashboards, "Dashboards-Opensearch", ""),
		},
		{
			Type:      "opensearch-gosec-agent",
			Name:      "Opensearch gosec agent",
			Component: "opensearch",
			Rset:      rsetDatastores,
			Match:     cctMatch(kindDeployment, "os-gosec-agent", ""),
			Entry:     gosecAgentEntry,
			Object:    "{{ .Entry }}-gosec-agent",
			Chart:     &Chart{Path: "charts/gosec-agent"},
			Exclude: []string{
				"spec.values.gosecAgent.environment.vault",
				"spec.values.gosecAgent.environment.agent.agentServiceName",
				"spec.values.gosecAgent.general.serviceId",
				"spec.values.opensearch.environment.opensearch_service.host",
			},
		},
		{
			Type:      "hdfs",
			Name:      "HDFS",
			Component: "hdfs",
			Rset:      rsetDatastores,
			Match:     cctMatch(kindHDFSCluster, "HDFS", ""),
		},
		{
			Type:      "kafka",
			Name:      "Kafka",
			Component: "kafka",
			Rset:      rsetDatastores,
			Match:     cctMatch(kindKafkaCluster, "Kafka", ""),
		},
		{
			Type:      "dg-agent",
			Name:      "Governance agent",
			Component: "dgAgent",
			Rset:      rsetDatastores,
			// The HelmRelease's name isn't the entry's: each storage-type
			// overlay fixes it (dg-hdfs-agent, dg-s3-agent), which is also
			// the legacy Deployment's name, while the rset names the
			// Kustomization apps-<entry>.
			Object:        "{{ .Live.Name }}",
			Kustomization: "apps-{{ .Entry }}",
			Match: Match{
				Kinds: []string{kindDeployment},
				Annotations: &Selector{MatchExpressions: []SelectorRequirement{
					{Key: annService, Operator: OpIn, Values: []string{"connectors-dfs", "connectors-rdbms"}},
				}},
			},
			Chart: &Chart{Path: "charts/dg-agent"},
		},
		{
			Type:      "eureka-agent",
			Name:      "Eureka agent",
			Component: "eurekaAgent",
			Rset:      rsetDatastores,
			Match:     cctMatch(kindDeployment, "bdl", "agent-bdl-default"),
			Chart:     &Chart{Path: "charts/eureka-agent"},
			Exclude: []string{
				"spec.values.eureka.general.externalConfiguration.governanceSettings",
				"spec.values.eureka.general.identity",
			},
		},
		{
			Type:      "bdl-datarest",
			Name:      "DataRest",
			Component: "bdlDatarest",
			Rset:      rsetDatastores,
			// The model pins the pginternal flavor, matching ValuesRoot; the
			// chart's pgmd5/pgtls flavors would be their own types.
			Match:   cctMatch(kindDeployment, "bdl", "datarest-pginternal"),
			Chart:   &Chart{Path: "charts/bdl-datarest", ValuesRoot: "datarestPgInternal"},
			Prepare: "prepare-datarest",
			Notes:   "prepare-datarest runs automatically before migrating: removes the legacy ingress that would collide with the GitOps-managed one.",
			Exclude: []string{"spec.values.datarestPgInternal.general.identity.approlename"},
		},
		{
			Type:      "virtualizer",
			Name:      "Virtualizer",
			Component: "virtualizer",
			Rset:      rsetApps,
			// model=default is the virtualizer server itself; its -monitor
			// and -ui siblings are separate CCT apps of the same chart.
			Match: cctMatch(kindDeployment, "virtualizer", "default"),
			Chart: &Chart{Path: "charts/virtualizer"},
			Exclude: []string{
				"spec.values.virtualizerMonitor.general.approlename",
				"spec.values.virtualizerServer.general.governanceRegistration.governanceDeployment",
				"spec.values.virtualizerServer.general.governanceRegistration.governanceBaseUri",
			},
		},
		{
			Type:      "discovery",
			Name:      "Discovery",
			Component: "discovery",
			Rset:      rsetApps,
			Match:     cctMatch(kindDeployment, "discovery", ""),
			Chart:     &Chart{Path: "charts/discovery"},
			Exclude:   []string{"spec.values.discovery.environment.approlename"},
		},
		{
			Type:      "datamarket-agent",
			Name:      "Datamarket agent",
			Component: "datamarketAgent",
			Rset:      rsetApps,
			Match:     cctMatch(kindDeployment, "data-marketplace", "agent-default"),
			Entry:     "governance-{{ .Live.Name }}",
			Chart:     &Chart{Path: "charts/governance-datamarket-agent"},
			Prepare:   "prepare-datamarket-agent",
			Notes:     "prepare-datamarket-agent runs automatically before migrating: suspends the legacy HelmRelease and scales it to 0.",
			Exclude: []string{
				"spec.values.datamarketAgent.general.datamarket.datamarketURL",
				"spec.values.datamarketAgent.environment.appId",
				"spec.values.datamarketAgent.environment.namespacesId",
				"spec.values.datamarketAgent.general.datamarketAgentInstanceName",
				"spec.values.datamarketAgent.environment.approlename",
			},
		},
		{
			Type:      "genai",
			Name:      "GenAI",
			Component: "genai",
			Rset:      rsetGenAI,
			// genai-api is the chart's anchor workload; genai-gateway and
			// genai-litellm are its siblings, fetched by chart templating.
			Match:   cctMatch(kindDeployment, "genai", "genai-api"),
			Entry:   `{{ .Live.Name | trimSuffix "-api" }}`,
			Chart:   &Chart{Path: "charts/genai"},
			Prepare: "prepare-genai",
			Notes:   "prepare-genai is a manual Postgres data rewrite apps migrate cannot verify itself; it always asks its own confirmation before proceeding, never skipped by --yes.",
			Exclude: []string{
				"spec.values.genaiUi.settings.generalProperties.governanceUrl",
				"spec.values.genaiUi.general.governanceRegistration.governanceDeployment",
				"spec.values.genaiUi.general.governanceRegistration.governanceBaseUri",
				"spec.values.genaiGateway.general.identity.approlename",
				"spec.values.genaiUi.settings.externalDashboards.discoveryDatabase",
			},
		},
		{
			Type:      "rocket",
			Name:      "Rocket",
			Component: "rocket",
			Rset:      rsetRocket,
			// Rocket's own sub-Deployments (rocket-catalog, -validator, ...)
			// are owned by this one and skipped by classification anyway;
			// model=default excludes rocket-catalog-standalone.
			Match: cctMatch(kindDeployment, "rocket", "default"),
			Chart: &Chart{Path: "charts/rocket"},
			Exclude: []string{
				"spec.values.rocketCommon.settings.governanceIntegration.crossdataCatalogGovernanceUri",
				"spec.values.rocketCommon.settings.governanceIntegration.crossdataCatalogGovernancePost",
				"spec.values.rocketCommon.settings.governanceIntegration.lineageHttpRequestUri",
				"spec.values.rocketCommon.general.genAI.genaiLayerConf.chainsGovernanceUrl",
				"spec.values.rocketCommon.general.governanceRegistration.governanceDeployment",
				"spec.values.rocketCommon.general.governanceRegistration.governanceBaseUri",
			},
		},
		{
			Type:      "intelligence",
			Name:      "Intelligence",
			Component: "intelligence",
			Rset:      rsetIntelligence,
			Match:     cctMatch(kindDeployment, "intelligence", ""),
			Chart:     &Chart{Path: "charts/intelligence"},
			Exclude: []string{
				"spec.values.configuration.security.vault.multiuser.approlename",
				"spec.values.configuration.general.genaiSettings.genaiLayerConf.genaiAPIIntegration.genaiChainsConf.genaiChainsGovernanceUrl",
			},
		},
		{
			Type:      "dlc-entity",
			Name:      "DLC Entity",
			Component: "dlcEntity",
			Rset:      rsetDLC,
			Match:     cctMatch(kindDeployment, "dlc-entity", ""),
			Chart:     &Chart{Path: "charts/dlc-entity"},
			Prepare:   "prepare-dlc",
			Notes:     "prepare-dlc runs automatically before migrating: removes the legacy ingress and Deployment (the chart recreates the Deployment under an immutable selector label).",
			Exclude: []string{
				"spec.values.dlcEntity.general.governance.governanceURL",
				"spec.values.dlcEntity.general.governance.governanceDiscoveryManagerURL",
				"spec.values.dlcEntity.general.identity.approlename",
				"spec.values.dlcEntity.settings.alertmanager",
			},
		},
	}}
}

// cctMatch selects kind objects by their CCT application_service
// annotation and, when model is non-empty, their application_model too.
func cctMatch(kind, service, model string) Match {
	sel := map[string]string{annService: service}
	if model != "" {
		sel[annModel] = model
	}
	return Match{Kinds: []string{kind}, Annotations: &Selector{MatchLabels: sel}}
}

// SeedEnvironment returns the environment `flux stratio config init`
// writes from its --base/--cluster/--tenant/--charts flags. Pass "" for
// chartsBase when the chart-source repo is a sibling of the keos-* repos
// under base, as is the common case.
func SeedEnvironment(base, cluster, tenant, chartsBase string) Environment {
	return Environment{Base: base, ChartsBase: chartsBase, Cluster: cluster, Tenant: tenant}
}
