package tenantimport

import "testing"

func TestEnrich_ResolvesDependencyToDiscoveredName(t *testing.T) {
	cat := loadFixtureCatalog(t)
	components := Components{
		"pgbouncer":          {{Name: "pool-psql", Deps: map[string]string{}}},
		"postgres":           {{Name: "psql", Deps: map[string]string{}}},
		"pgbackuprepository": {{Name: "pgbackuprepository", Deps: map[string]string{}}},
	}
	enrich(cat, components, func(string, ...any) {})

	if got := components.find("pgbouncer", "pool-psql").Deps["postgres"]; got != "psql" {
		t.Errorf("pgbouncer.Deps[postgres] = %q, want %q", got, "psql")
	}
	if got := components.find("postgres", "psql").Deps["pgbackuprepository"]; got != "pgbackuprepository" {
		t.Errorf("postgres.Deps[pgbackuprepository] = %q, want %q", got, "pgbackuprepository")
	}
}

func TestEnrich_UnresolvedDependencyGetsEmptyNameAndWarning(t *testing.T) {
	cat := loadFixtureCatalog(t)
	components := Components{"pgbouncer": {{Name: "pool-psql", Deps: map[string]string{}}}} // no postgres discovered

	var warned bool
	enrich(cat, components, func(string, ...any) { warned = true })

	entry := components.find("pgbouncer", "pool-psql")
	if v, ok := entry.Deps["postgres"]; !ok || v != "" {
		t.Errorf("Deps[postgres] = %q (present=%v), want empty string present", v, ok)
	}
	if !warned {
		t.Error("expected a warning for the unresolved dependency")
	}
}

func TestEnrich_AlreadyKnownDepFromCRDScanIsPreserved(t *testing.T) {
	cat := loadFixtureCatalog(t)
	components := Components{
		"pgbouncer": {{Name: "pool-psql", Deps: map[string]string{"postgres": "already-set-by-crd-scan"}}},
		"postgres":  {{Name: "psql", Deps: map[string]string{}}},
	}
	enrich(cat, components, func(string, ...any) {})

	if got := components.find("pgbouncer", "pool-psql").Deps["postgres"]; got != "already-set-by-crd-scan" {
		t.Errorf("Deps[postgres] = %q, want the pre-existing value preserved", got)
	}
}

func TestEnrich_ExtraConfigDefaultApplied(t *testing.T) {
	cat := loadFixtureCatalog(t)
	components := Components{"kafka": {{Name: "kafka", Deps: map[string]string{}}}}
	enrich(cat, components, func(string, ...any) {})

	entry := components.find("kafka", "kafka")
	if entry.ExtraConfig["kafkaClusterExposed"] != "false" {
		t.Errorf("ExtraConfig[kafkaClusterExposed] = %v, want %q (schema default)", entry.ExtraConfig["kafkaClusterExposed"], "false")
	}
}

func TestEnrich_HDFSStorageTypeInferredGenerically(t *testing.T) {
	cat := loadFixtureCatalog(t)
	components := Components{
		"virtualizer": {{Name: "virtualizer", Deps: map[string]string{}}}, // no Type set yet
		"hdfs":        {{Name: "hdfs1", Deps: map[string]string{}}},       // discovered -> implies hdfs
	}
	enrich(cat, components, func(string, ...any) {})

	entry := components.find("virtualizer", "virtualizer")
	if entry.Type != "hdfs" {
		t.Errorf("Type = %q, want hdfs (inferred from the discovered hdfs component)", entry.Type)
	}
	// And with hdfs inferred, the hdfs-only deps must now resolve.
	if entry.Deps["hdfs"] != "hdfs1" {
		t.Errorf("Deps[hdfs] = %q, want %q", entry.Deps["hdfs"], "hdfs1")
	}
}

func TestEnrich_NoHDFSComponentLeavesTypeUnset(t *testing.T) {
	cat := loadFixtureCatalog(t)
	components := Components{"virtualizer": {{Name: "virtualizer", Deps: map[string]string{}}}}
	enrich(cat, components, func(string, ...any) {})
	if entry := components.find("virtualizer", "virtualizer"); entry.Type != "" {
		t.Errorf("Type = %q, want empty (no hdfs component discovered)", entry.Type)
	}
}

func TestEnrich_MultipleInstancesDefaultsAndWarns(t *testing.T) {
	cat := loadFixtureCatalog(t)
	components := Components{
		"pgbouncer": {{Name: "pool-psql", Deps: map[string]string{}}},
		"postgres": {
			{Name: "psql-a", Deps: map[string]string{}},
			{Name: "psql-b", Deps: map[string]string{}},
		},
	}
	var warned bool
	enrich(cat, components, func(string, ...any) { warned = true })

	got := components.find("pgbouncer", "pool-psql").Deps["postgres"]
	if got != "psql-a" {
		t.Errorf("Deps[postgres] = %q, want the first discovered instance %q", got, "psql-a")
	}
	if !warned {
		t.Error("expected a warning about the ambiguous multi-instance dependency")
	}
}
