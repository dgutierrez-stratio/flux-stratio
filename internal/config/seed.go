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
			// What a legacy agent was configured with says which GitOps flavor it
			// is: the storage-type overlay (hdfs, s3) is the entry's config.type,
			// and keos-apps has none for the other extractor types (the legacy
			// POSTGRESQL agents), which `tenant import` therefore leaves out
			// instead of deploying an S3 agent under their name. The governance
			// pgbouncer is the host's first label; the governance PgCluster is
			// named after the database it serves (postgreskeos, as in every
			// reference tenant).
			ConfigFromEnv: []EnvConfig{
				{Key: "type", Env: "CUSTOM_SERVICE_EXTRACTOR_TYPE", Values: map[string]string{"HDFS": "hdfs", "S3": "s3"}},
				{Key: "dependencies.governancePgbouncer.name", Env: "DATASTORE_HOST", Pattern: "^([^.]+)"},
				{Key: "dependencies.governancePostgres.name", Env: "DATASTORE_DATABASE"},
			},
			Prepare: "prepare-dg-agent",
			Notes:   "prepare-dg-agent runs automatically before migrating: removes the legacy agent Deployment (the chart gives it a new, immutable selector under the same name).",
		},
		{
			Type:      "eureka-agent",
			Name:      "Eureka agent",
			Component: "eurekaAgent",
			Rset:      rsetDatastores,
			Match:     cctMatch(kindDeployment, "bdl", "agent-bdl-default"),
			Chart:     &Chart{Path: "eureka-agent"},
			MemoryEnv: []string{"VR_POD_MEMORY_LIMIT"},
			Prepare:   "prepare-eureka-agent",
			Notes:     "prepare-eureka-agent runs automatically before migrating: removes the legacy eureka-agent Deployment (the chart gives it a new, immutable selector under the same name); nothing else of the app is touched.",
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
			Match: cctMatch(kindDeployment, "bdl", "datarest-pginternal"),
			Chart: &Chart{Path: "bdl-datarest", ValuesRoot: "datarestPgInternal"},
			// The chart's pginternal flavor sets JDBC_DATABASE from the
			// postgresDatabase config (dependencies.postgres.database), whose
			// template default is another database: the legacy value is what the
			// app is connected to.
			ConfigFromEnv: []EnvConfig{{Key: "postgresDatabase", Env: "JDBC_DATABASE"}},
			MemoryEnv:     []string{"VR_POD_MEMORY_LIMIT"},
			Prepare:       "prepare-datarest",
			Notes:         "prepare-datarest runs automatically before migrating: removes the legacy ingress that would collide with the GitOps-managed one, then the legacy Deployment (the chart keeps its name but gives it a new, immutable selector); the Service and everything else stay.",
			Exclude:       []string{"spec.values.datarestPgInternal.general.identity.approlename"},
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
			}, EnabledBy: "uiEnabled"},
			MemoryEnv: []string{"VR_POD_MEMORY_LIMIT"},
			Prepare:   "prepare-virtualizer",
			Notes:     "prepare-virtualizer runs automatically before migrating: removes the legacy virtualizer, monitor and ui Deployments (the chart gives them new, immutable selectors under the same names); legacy ingresses stay.",
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
			MemoryEnv: []string{"MEMORY"},
			Prepare:   "prepare-discovery",
			Notes:     "prepare-discovery runs automatically before migrating: removes the legacy Deployment (the chart gives it a new, immutable selector under the same name); the legacy ingress stays.",
			Exclude:   []string{"spec.values.discovery.environment.approlename"},
		},
		{
			// CCT deployed it as the connectors-management service; the GitOps
			// component is "connectors" (keos-apps fixes the HelmRelease's name to
			// it, and the other components' dependencies point at the entry of
			// that name), so the entry drops the legacy "-management" suffix.
			// The rset names the Kustomization apps-<entry>.
			//
			// No siblings, no chart.uses/enabledBy (the ResourceSet gates nothing
			// on it) and no prepare step: the chart's Deployment gets a new,
			// immutable selector under the same name, so the legacy one has to
			// go before the release is applied (see Notes). The exclusions are
			// not reviewed yet, so none are set.
			Type:          "connectors",
			Name:          "Connectors",
			Component:     "connectors",
			Rset:          rsetApps,
			Match:         cctMatch(kindDeployment, "connectors-management", "connectors-management-service-default"),
			Entry:         `{{ .Live.Name | trimSuffix "-management" }}`,
			Object:        "connectors",
			Kustomization: "apps-{{ .Entry }}",
			Chart:         &Chart{Path: "connectors"},
			Notes:         "No prepare step exists for connectors yet, and its patch exclusions have not been reviewed: before `apps migrate connectors`, review `apps diff connectors --view patch`, and remove the legacy Deployment by hand (the chart gives it a new, immutable selector under the same name).",
		},
		{
			Type:      "datamarket-agent",
			Name:      "Datamarket agent",
			Component: "datamarketAgent",
			Rset:      rsetApps,
			Match:     cctMatch(kindDeployment, "data-marketplace", "agent-default"),
			Entry:     "governance-{{ .Live.Name }}",
			// keos-apps fixes the HelmRelease's name whatever the entry is
			// called, while the rset names the Kustomization apps-<entry>.
			Object:        "governance-datamarket-agent",
			Kustomization: "apps-{{ .Entry }}",
			Chart:         &Chart{Path: "governance-datamarket-agent"},
			Prepare:       "prepare-datamarket-agent",
			Notes:         "prepare-datamarket-agent runs automatically before migrating: suspends the legacy HelmRelease and scales it to 0.",
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
			// The chart cannot run without it, though the template defaults it to an
			// empty container: `tenant import` warns until it is set.
			RequiredConfig: []string{"llmModels"},
			Chart: &Chart{Path: "genai", Siblings: []Match{
				cctMatch(kindDeployment, "genai", "genai-ui"),
				cctMatch(kindDeployment, "genai", "genai-developer-proxy"),
			}},
			Prepare: "prepare-genai",
			Notes:   "prepare-genai is a Postgres data rewrite apps migrate cannot verify itself: after a backup of the app's workloads and a copy of the rows it changes (<backup>/prepare/genai-<tenant>-chain.sql, -endpoint.sql), it runs the SQL via pod exec and shows the result, always asking its own confirmation before and after, never skipped by --yes. Then it deletes the legacy genai-api, genai-ui and genai-developer-proxy Deployments (the chart changed their immutable selectors; asked like any step, --yes answers it) and leaves their Ingresses and Services. A re-run with no row left to rewrite and no legacy Deployment reports it already satisfied.",
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
			// The Vault passwords are carried over by prepare-litellm: the
			// chart's SecretsBundle owns userland/passwords/<release>.<namespace>/
			// and the secrets operator only accepts hyphenated names, so the
			// legacy underscored master_api_key, db_salt_key and keos_key_secret
			// would be replaced by fresh random ones — leaving the provider
			// credentials LiteLLM encrypted with the legacy salt unreadable and
			// every key derived from keos_key_secret (genai-api's) invalid. The
			// step copies each legacy value to the chart's name before the chart
			// is applied (the operator keeps a value of the declared length) and
			// keeps the originals under legacy/, since the bundle deletes every
			// entry it doesn't declare (see Notes for a release that was
			// migrated before).
			//
			// Excluded: the networking/SSO URLs (the chart's Ingress is always
			// at its release path, so legacy URLs would point the app at the
			// legacy Ingress's route) and the vault approlename (the chart's
			// SecretsIdentity role). Kept: the legacy Postgres database/schema,
			// gosec groups and autoUvicornWorkers.
			Match:  cctMatch(kindDeployment, "genai", "genai-litellm"),
			Object: "litellm",
			// The chart cannot run without it, though the template defaults it to an
			// empty container: `tenant import` warns until it is set.
			RequiredConfig: []string{"models"},
			Chart:          &Chart{Path: "litellm"},
			Prepare:        "prepare-litellm",
			Exclude: []string{
				"spec.values.liteLlm.general.networking.ingressHost",
				"spec.values.liteLlm.general.networking.ingressBasePath",
				"spec.values.liteLlm.general.networking.oauth2ProxyExternalHost",
				"spec.values.liteLlm.environment.sso.oauth2ProxyLogoutNextUrl",
				"spec.values.liteLlm.general.identity.approlename",
			},
			Notes: "prepare-litellm runs automatically before migrating: removes the legacy Deployment (the chart gives it a new, immutable selector under the same name); the legacy ingress stays. The tenant entry's name stays the legacy live name (genai-litellm): keos-use-cases substitutes it into LITELLM_NAME, which only sets the Helm release name (spec.releaseName), so the release keeps the legacy identity and database grants while the GitOps object (Kustomization/HelmRelease) stays the generic 'litellm', uniform with every other component. The patch keeps the legacy Postgres database and schema. prepare-litellm also keeps the legacy Vault passwords: before the chart is applied it copies db_salt_key, keos_key_secret and master_api_key (userland/passwords/genai-litellm.<ns>/) to the chart's db-salt-key, keos-key-secret and master-api-key, and keeps the originals under legacy/ there (the chart's SecretsBundle deletes the underscored ones once applied). Stored credentials keep decrypting and keys derived from keos_key_secret (genai-api's) stay valid. Run `flux stratio doctor` first: it checks Vault is reachable the same way. If the release was applied before the step ran, the legacy values are gone (the step only warns): delete the credential rows the legacy salt encrypted (LiteLLM_CredentialsTable) and the users/teams/keys registered before the migration, restart LiteLLM (it caches them), and re-run the post-requisites and genai prerequisites jobs. Needs the litellm chart whose GosecPolicy follows postgresDatabase, and keos-apps/keos-use-cases with releaseName decoupled from the GitOps object name (LITELLM_NAME only substitutes releaseName) The gosec groups (liteLlm.general.gosecIntegration.adminGroup/usersGroup) are the single source for the app, the chart's prerequisites job and keos-apps' post-requisites job (it reads them from the release's ConfigMap), so keeping the legacy values in the patch is enough; removing them moves the release to the chart's default groups, which start with no members.",
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
			// The paths are legacy configuration the migration patch keeps in the
			// HelmRelease values: the evidence that rocket uses the integration
			// (see Chart.Uses).
			Chart: &Chart{Path: "rocket", Uses: []Use{
				{Dependency: "genai", Values: []string{"rocketCommon.general.genAI.genaiEnabled"}},
				{Dependency: "virtualizer", Values: []string{"rocketCommon.general.catalog.virtualizerHost"}},
			}},
			Prepare: "prepare-rocket",
			Notes:   "prepare-rocket runs automatically before migrating: removes the legacy rocket Deployment and the Deployments its server created (rocket-catalog, -debug, -externalservice and -validator, owned by it; the new server recreates them), the chart having changed rocket's immutable selector. rocket-catalog-standalone, the Ingresses and the Services stay.",
			Exclude: []string{
				"spec.values.rocketServer.settings.governanceIntegration.crossdataCatalogGovernanceUri",
				"spec.values.rocketServer.settings.governanceIntegration.crossdataCatalogGovernancePost",
				"spec.values.rocketServer.settings.governanceIntegration.lineageHttpRequestUri",
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
			// As rocket: legacy configuration kept in the HelmRelease values.
			Chart: &Chart{Path: "intelligence", Uses: []Use{
				{Dependency: "genai", Values: []string{"configuration.general.genaiSettings.genaiEnabled"}},
				{Dependency: "virtualizer", Values: []string{"configuration.settings.AnalyticSettings.crossdataServer.CrossdataServerHost"}},
				{Dependency: "opensearch", Values: []string{"configuration.general.genaiSettings.genaiLayerConf.genaiAPIIntegration.genaiChainsConf.sqlChainConf.sqlChainOpensearchEnabled"}},
				{Dependency: "litellm", Values: []string{"configuration.general.genaiSettings.genaiLayerConf.litellmIntegration.litellmUrl"}},
			}},
			Prepare: "prepare-intelligence",
			Notes:   "prepare-intelligence runs automatically before migrating: removes the legacy intelligence Deployment (the chart gives it a new, immutable selector under the same name); the legacy ingress intelligence-rocket.<domain> stays (the chart's admin ingress has another host).",
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
