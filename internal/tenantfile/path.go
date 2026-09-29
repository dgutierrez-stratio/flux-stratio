package tenantfile

import "path/filepath"

// Path returns the on-disk path of a tenant's ResourceSetInputProvider
// file: <keos-fleet>/clusters/<cluster>/tenants/config/<tenant>.yaml, fleet
// being the keos-fleet checkout — the convention the Python client's
// apply_patch_to_tenant_yaml and flux_renderer.py both hard-code.
func Path(fleet, cluster, tenant string) string {
	return filepath.Join(fleet, "clusters", cluster, "tenants", "config", tenant+".yaml")
}
