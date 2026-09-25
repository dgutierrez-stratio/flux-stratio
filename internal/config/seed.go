package config

// Seed returns flux-stratio's known Stratio application catalog — the same
// 16 applications the legacy Python migration client's config.json shipped,
// translated into this package's schema and scoped to base, cluster and
// tenant. It's what `flux stratio config init` writes to disk: a real,
// working starting catalog for this application set, not a placeholder an
// operator has to author from nothing. Review it before use — an
// environment may run a subset of these applications, or run ones this
// catalog doesn't know about yet.
//
// Two corrections were made against the Python source: dlc-entity now
// declares prepare: prepare-dlc (the Python config left it unset, even
// though the step exists specifically to gate this app — see
// migrationTasks in the legacy config.json), and datamarket-agent's
// previousNamespace is derived from tenant instead of hardcoded to
// "stratio-datastores".
//
// chartsBase seeds the optional Config.ChartsBase field; pass "" when the
// chart-source repo is a sibling of keos-apps/keos-use-cases/keos-fleet/
// keos-system-services under base, as is the common case.
func Seed(base, cluster, tenant, chartsBase string) Config {
	return Config{
		Base:       base,
		Cluster:    cluster,
		Tenant:     tenant,
		ChartsBase: chartsBase,
		Apps:       seedApps(tenant),
	}
}

func seedApps(tenant string) []App {
	return []App{
		{
			ID:            "psql",
			Name:          "Postgres psql",
			Rset:          "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-psql",
			Object:        "psql",
			Exclude: []string{
				"spec.bootstrap.pgBackup",
				"spec.bootstrap.pgbackrest.repository.azure",
				"spec.bootstrap.pgbackrest.repository.gcs",
			},
		},
		{
			ID:            "pool-psql",
			Name:          "Postgres pool-psql",
			Rset:          "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-pool-psql",
			Object:        "pool-psql",
		},
		{
			ID:            "psql-gosec-agent",
			Name:          "Postgres gosec agent",
			Rset:          "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-psql-gosec-agent",
			Object:        "psql-gosec-agent",
			ChartPath:     "charts/gosec-agent",
			Renamed:       "psql-agent",
			Exclude: []string{
				"spec.values.gosecAgent.environment.domainsConfig.mappingUrl",
				"spec.values.gosecAgent.environment.agent.agentServiceName",
				"spec.values.gosecAgent.general.serviceId",
				"spec.values.gosecAgent.environment.vault.vaultRole",
			},
		},
		{
			ID:            "hdfs1",
			Name:          "HDFS",
			Rset:          "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-hdfs1",
			Object:        "hdfs1",
		},
		{
			ID:            "virtualizer",
			Name:          "Virtualizer",
			Rset:          "apps/components/resourceset-apps-apps.yaml",
			Kustomization: "apps-virtualizer",
			Object:        "virtualizer",
			ChartPath:     "charts/virtualizer",
			Exclude: []string{
				"spec.values.virtualizerMonitor.general.approlename",
				"spec.values.virtualizerServer.general.governanceRegistration.governanceDeployment",
				"spec.values.virtualizerServer.general.governanceRegistration.governanceBaseUri",
			},
		},
		{
			ID:            "opensearch1",
			Name:          "Opensearch1",
			Rset:          "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-opensearch1",
			Object:        "opensearch1",
			Exclude:       []string{"spec.image"},
		},
		{
			ID:            "opensearch1-gosec-agent",
			Name:          "Opensearch1 gosec agent",
			Rset:          "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-opensearch1-gosec-agent",
			Object:        "opensearch1-gosec-agent",
			ChartPath:     "charts/gosec-agent",
			Renamed:       "opensearch1-agent",
			Exclude: []string{
				"spec.values.gosecAgent.environment.vault",
				"spec.values.gosecAgent.environment.agent.agentServiceName",
				"spec.values.gosecAgent.general.serviceId",
				"spec.values.opensearch.environment.opensearch_service.host",
			},
		},
		{
			ID:            "kafka",
			Name:          "Kafka",
			Rset:          "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-kafka",
			Object:        "kafka",
		},
		{
			ID:            "discovery",
			Name:          "Discovery",
			Rset:          "apps/components/resourceset-apps-apps.yaml",
			Kustomization: "apps-discovery",
			Object:        "discovery",
			ChartPath:     "charts/discovery",
			Exclude:       []string{"spec.values.discovery.environment.approlename"},
		},
		{
			ID:            "eureka-agent",
			Name:          "Eureka Agent",
			Rset:          "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-eureka-agent",
			Object:        "eureka-agent",
			ChartPath:     "charts/eureka-agent",
			Exclude: []string{
				"spec.values.eureka.general.externalConfiguration.governanceSettings",
				"spec.values.eureka.general.identity",
			},
		},
		{
			ID:            "genai",
			Name:          "GenAI",
			Rset:          "apps/components/resourceset-apps-genai.yaml",
			Kustomization: "apps-genai",
			Object:        "genai",
			ChartPath:     "charts/genai",
			Prepare:       "prepare-genai",
			Notes:         "prepare-genai is a manual Postgres data rewrite apps migrate cannot verify itself; it always asks its own confirmation before proceeding, never skipped by --yes.",
			Exclude: []string{
				"spec.values.genaiUi.settings.generalProperties.governanceUrl",
				"spec.values.genaiUi.general.governanceRegistration.governanceDeployment",
				"spec.values.genaiUi.general.governanceRegistration.governanceBaseUri",
				"spec.values.genaiGateway.general.identity.approlename",
				"spec.values.genaiUi.settings.externalDashboards.discoveryDatabase",
			},
		},
		{
			ID:            "rocket",
			Name:          "Rocket",
			Rset:          "apps/components/resourceset-apps-rocket.yaml",
			Kustomization: "apps-rocket",
			Object:        "rocket",
			ChartPath:     "charts/rocket",
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
			ID:                "datamarket-agent",
			Name:              "Datamarket Agent",
			Rset:              "apps/components/resourceset-apps-apps.yaml",
			Kustomization:     "apps-governance-datamarket-agent",
			Object:            "governance-datamarket-agent",
			ChartPath:         "charts/governance-datamarket-agent",
			PreviousNamespace: tenant + "-datastores",
			Prepare:           "prepare-datamarket-agent",
			Notes:             "prepare-datamarket-agent runs automatically before migrating: suspends the legacy HelmRelease and scales it to 0.",
			Exclude: []string{
				"spec.values.datamarketAgent.general.datamarket.datamarketURL",
				"spec.values.datamarketAgent.environment.appId",
				"spec.values.datamarketAgent.environment.namespacesId",
				"spec.values.datamarketAgent.general.datamarketAgentInstanceName",
				"spec.values.datamarketAgent.environment.approlename",
			},
		},
		{
			ID:            "intelligence",
			Name:          "Intelligence",
			Rset:          "apps/components/resourceset-apps-intelligence.yaml",
			Kustomization: "apps-intelligence",
			Object:        "intelligence",
			ChartPath:     "charts/intelligence",
			Exclude: []string{
				"spec.values.configuration.security.vault.multiuser.approlename",
				"spec.values.configuration.general.genaiSettings.genaiLayerConf.genaiAPIIntegration.genaiChainsConf.genaiChainsGovernanceUrl",
			},
		},
		{
			ID:            "dlc-entity",
			Name:          "DLC Entity",
			Rset:          "apps/components/resourceset-apps-dlc.yaml",
			Kustomization: "apps-dlc-entity",
			Object:        "dlc-entity",
			ChartPath:     "charts/dlc-entity",
			Prepare:       "prepare-dlc",
			Notes:         "prepare-dlc runs automatically before migrating: removes the legacy ingress and Deployment (the chart recreates the Deployment under an immutable selector label).",
			Exclude: []string{
				"spec.values.dlcEntity.general.governance.governanceURL",
				"spec.values.dlcEntity.general.governance.governanceDiscoveryManagerURL",
				"spec.values.dlcEntity.general.identity.approlename",
				"spec.values.dlcEntity.settings.alertmanager",
			},
		},
		{
			ID:            "dg-datarest-pgi",
			Name:          "DataRest",
			Rset:          "apps/components/resourceset-apps-datastores.yaml",
			Kustomization: "apps-dg-datarest-pgi",
			Object:        "dg-datarest-pgi",
			ChartPath:     "charts/bdl-datarest",
			ValuesRoot:    "datarestPgInternal",
			Prepare:       "prepare-datarest",
			Notes:         "prepare-datarest runs automatically before migrating: removes the legacy ingress that would collide with the GitOps-managed one.",
			Exclude:       []string{"spec.values.datarestPgInternal.general.identity.approlename"},
		},
	}
}
