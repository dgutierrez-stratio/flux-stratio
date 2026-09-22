package tenantimport

import "testing"

func TestExpandMandatoryComponents_AddsSkeletonForMissingDependency(t *testing.T) {
	cat := loadFixtureCatalog(t)
	// postgres depends on pgbackuprepository, which isn't discovered.
	components := Components{"postgres": {{Name: "psql", Deps: map[string]string{}}}}

	var warnings []string
	expandMandatoryComponents(cat, components, func(f string, a ...any) {
		warnings = append(warnings, f)
	})

	if names := components.names("pgbackuprepository"); len(names) != 1 || names[0] != "pgbackuprepository" {
		t.Errorf("pgbackuprepository entries = %v, want a skeleton named \"pgbackuprepository\"", names)
	}
	if len(warnings) == 0 {
		t.Error("expected a warning about the skeleton entry")
	}
}

func TestExpandMandatoryComponents_FixpointTransitiveDeps(t *testing.T) {
	cat := loadFixtureCatalog(t)
	// pgbouncer -> postgres -> pgbackuprepository: a single pass only
	// reveals postgres; the fixpoint loop must also add pgbackuprepository.
	components := Components{"pgbouncer": {{Name: "pool-psql", Deps: map[string]string{}}}}

	expandMandatoryComponents(cat, components, func(string, ...any) {})

	if len(components.names("postgres")) != 1 {
		t.Errorf("postgres entries = %v, want 1", components.names("postgres"))
	}
	if len(components.names("pgbackuprepository")) != 1 {
		t.Errorf("pgbackuprepository entries = %v, want 1 (transitive dependency)", components.names("pgbackuprepository"))
	}
}

func TestExpandMandatoryComponents_DoesNotDuplicateExisting(t *testing.T) {
	cat := loadFixtureCatalog(t)
	components := Components{
		"postgres":           {{Name: "psql", Deps: map[string]string{}}},
		"pgbackuprepository": {{Name: "pgbackuprepository", Deps: map[string]string{}}},
	}
	expandMandatoryComponents(cat, components, func(string, ...any) {})
	if len(components["pgbackuprepository"]) != 1 {
		t.Errorf("pgbackuprepository entries = %+v, want still 1", components["pgbackuprepository"])
	}
}

func TestExpandMandatoryComponents_StorageConditionalDepsRespectType(t *testing.T) {
	cat := loadFixtureCatalog(t)
	// virtualizer depends on connectors/hdfs only when Type == "hdfs".
	// With no Type set (s3 default), neither should be forced into existence.
	components := Components{"virtualizer": {{Name: "virtualizer", Deps: map[string]string{}}}}
	expandMandatoryComponents(cat, components, func(string, ...any) {})
	if len(components["connectors"]) != 0 || len(components["hdfs"]) != 0 {
		t.Errorf("connectors=%v hdfs=%v, want both empty (storage type is not hdfs)", components["connectors"], components["hdfs"])
	}

	componentsHdfs := Components{"virtualizer": {{Name: "virtualizer", Type: "hdfs", Deps: map[string]string{}}}}
	expandMandatoryComponents(cat, componentsHdfs, func(string, ...any) {})
	if len(componentsHdfs["connectors"]) != 1 || len(componentsHdfs["hdfs"]) != 1 {
		t.Errorf("connectors=%v hdfs=%v, want both populated (storage type is hdfs)", componentsHdfs["connectors"], componentsHdfs["hdfs"])
	}
}

func TestExpandMandatoryComponents_DepWithNoSchemaNeverExpanded(t *testing.T) {
	cat := loadFixtureCatalog(t)
	// Inject a fake dep with no matching schema (simulating "governancePostgres").
	components := Components{"fictional": {{Name: "x", Deps: map[string]string{}}}}
	// fictional isn't even a real component key (no schema), so the outer
	// loop skips it entirely — nothing should be added, and this must not panic.
	expandMandatoryComponents(cat, components, func(string, ...any) {})
	if len(components) != 1 {
		t.Errorf("components = %+v, want unchanged (no schema for \"fictional\")", components)
	}
}
