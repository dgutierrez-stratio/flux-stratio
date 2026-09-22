package tenantimport

import "strings"

// inTenantNamespace reports whether namespace belongs to tenantName:
// either exactly tenantName, or prefixed "tenantName-". A plain substring
// match would wrongly accept "stratiotest-apps" for tenant "stratio" —
// this only matches on a full "-" boundary.
func inTenantNamespace(namespace, tenantName string) bool {
	return namespace == tenantName || strings.HasPrefix(namespace, tenantName+"-")
}
