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
			Chart:     &Chart{Path: "gosec-agent"},
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
			Chart:     &Chart{Path: "gosec-agent"},
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
			Chart: &Chart{Path: "dg-agent"},
		},
		{
			Type:      "eureka-agent",
			Name:      "Eureka agent",
			Component: "eurekaAgent",
			Rset:      rsetDatastores,
			Match:     cctMatch(kindDeployment, "bdl", "agent-bdl-default"),
			Chart:     &Chart{Path: "eureka-agent"},
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
			Chart:   &Chart{Path: "bdl-datarest", ValuesRoot: "datarestPgInternal"},
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
			Chart: &Chart{Path: "virtualizer", Siblings: []Match{
				cctMatch(kindDeployment, "virtualizer", "virtualizer-monitor"),
				cctMatch(kindDeployment, "virtualizer", "virtualizer-ui"),
			}},
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
			Chart:     &Chart{Path: "discovery"},
			Exclude:   []string{"spec.values.discovery.environment.approlename"},
		},
		{
			Type:      "datamarket-agent",
			Name:      "Datamarket agent",
			Component: "datamarketAgent",
			Rset:      rsetApps,
			Match:     cctMatch(kindDeployment, "data-marketplace", "agent-default"),
			Entry:     "governance-{{ .Live.Name }}",
			Chart:     &Chart{Path: "governance-datamarket-agent"},
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
			// genai-api is the chart's anchor workload; genai-ui and
			// genai-developer-proxy are its siblings, CCT apps of their own
			// (model = workload name, as for every captured genai model).
			// genai-litellm, the same CCT service's other model, is its own
			// litellm component; the legacy genai-gateway isn't rendered by
			// the chart any more, so it isn't a sibling.
			Match: cctMatch(kindDeployment, "genai", "genai-api"),
			Entry: `{{ .Live.Name | trimSuffix "-api" }}`,
			Chart: &Chart{Path: "genai", Siblings: []Match{
				cctMatch(kindDeployment, "genai", "genai-ui"),
				cctMatch(kindDeployment, "genai", "genai-developer-proxy"),
			}},
			Prepare: "prepare-genai",
			Notes:   "prepare-genai is a Postgres data rewrite apps migrate cannot verify itself; it runs the SQL via pod exec and shows the result, but always asks its own confirmation before proceeding, never skipped by --yes.",
			// Every workload's vault approlename is excluded, as for the
			// other chart components: each uses the chart's own GitOps role
			// (its SecretsIdentity), never the legacy CCT one.
			Exclude: []string{
				"spec.values.genaiApi.general.identity.approlename",
				"spec.values.genaiUi.settings.generalProperties.governanceUrl",
				"spec.values.genaiUi.general.governanceRegistration.governanceDeployment",
				"spec.values.genaiUi.general.governanceRegistration.governanceBaseUri",
				"spec.values.genaiUi.general.identity.approlename",
				"spec.values.genaiDeveloperProxy.general.identity.approlename",
				"spec.values.genaiGateway.general.identity.approlename",
				"spec.values.genaiUi.settings.externalDashboards.discoveryDatabase",
			},
		},
		{
			Type:      "litellm",
			Name:      "LiteLLM",
			Component: "litellm",
			Rset:      rsetGenAI,
			// CCT deployed it as the genai service's genai-litellm model.
			// The GitOps object (Kustomization "apps-litellm", HelmRelease
			// "litellm") is always the generic name, uniform with every
			// other component — only the Helm *release* itself (spec.
			// releaseName, decoupled in keos-apps) is the legacy name, so
			// the chart's identity (cert CN = Postgres user, gosec user)
			// derived from it is the legacy one and the patch points it at
			// the legacy database; the chart's GosecPolicy grants whatever
			// postgresDatabase names. Object is pinned to the fixed literal
			// "litellm" (not the default {{ .Entry }}) precisely because
			// Entry stays the legacy live name ("genai-litellm") — it's
			// still what the tenant file's own components.litellm[].name
			// must be, since keos-use-cases substitutes that value into
			// LITELLM_NAME (the release name), but it no longer names the
			// GitOps object.
			//
			// The Vault secrets are NOT carried over: the chart's SecretsBundle
			// owns userland/passwords/<release>.<namespace>/ and deletes every
			// key there it doesn't declare, and the secrets operator only
			// accepts hyphenated names, so the legacy underscored master_api_key,
			// db_salt_key and keos_key_secret can't be kept. The release gets
			// fresh ones; rows LiteLLM encrypted with the legacy salt (stored
			// models and credentials) must be cleaned and re-registered from the
			// tenant entry's config.models (see Notes).
			//
			// Excluded: the networking/SSO URLs (the chart's Ingress is always
			// at its release path, so legacy URLs would point the app at the
			// legacy Ingress's route) and the vault approlename (the chart's
			// SecretsIdentity role). Kept: the legacy Postgres database/schema,
			// gosec groups and autoUvicornWorkers.
			Match:  cctMatch(kindDeployment, "genai", "genai-litellm"),
			Object: "litellm",
			Chart:  &Chart{Path: "litellm"},
			Exclude: []string{
				"spec.values.liteLlm.general.networking.ingressHost",
				"spec.values.liteLlm.general.networking.ingressBasePath",
				"spec.values.liteLlm.general.networking.oauth2ProxyExternalHost",
				"spec.values.liteLlm.environment.sso.oauth2ProxyLogoutNextUrl",
				"spec.values.liteLlm.general.identity.approlename",
			},
			Notes: "The tenant entry's name stays the legacy live name (genai-litellm): keos-use-cases substitutes it into LITELLM_NAME, which only sets the Helm release name (spec.releaseName), so the release keeps the legacy identity and database grants while the GitOps object (Kustomization/HelmRelease) stays the generic 'litellm', uniform with every other component. The patch keeps the legacy Postgres database and schema. The legacy Vault secrets can't be kept: the release gets fresh ones, so before cutover delete the rows the legacy salt encrypted (LiteLLM_ProxyModelTable, LiteLLM_CredentialsTable) and declare the models in the entry's config.models to re-register them. Needs the litellm chart whose GosecPolicy follows postgresDatabase, and keos-apps/keos-use-cases with releaseName decoupled from the GitOps object name (LITELLM_NAME only substitutes releaseName).",
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
			Chart: &Chart{Path: "rocket"},
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
			Chart:     &Chart{Path: "intelligence"},
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
			Chart:     &Chart{Path: "dlc-entity"},
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
// writes from its --base/--repo/--cluster/--tenant flags. repos is empty
// in the common case, every repository a sibling checkout under base.
func SeedEnvironment(base, cluster, tenant string, repos map[string]string) Environment {
	return Environment{Base: base, Repos: repos, Cluster: cluster, Tenant: tenant}
}
