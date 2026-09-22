package tenantfile

import "path/filepath"

// Path returns the on-disk path of a tenant's ResourceSetInputProvider
// file: <base>/keos-fleet/clusters/<cluster>/tenants/config/<tenant>.yaml —
// the convention the Python client's apply_patch_to_tenant_yaml and
// flux_renderer.py both hard-code.
func Path(base, cluster, tenant string) string {
	return filepath.Join(base, "keos-fleet", "clusters", cluster, "tenants", "config", tenant+".yaml")
}
