package tenantimport

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Stratio/flux-stratio/internal/components"
)

// tenantObject reports whether obj is one of tenantName's objects: in one
// of its namespaces (inTenantNamespace), and not marked as another
// tenant's (components.BelongsToTenant) — the same exclusion `apps`
// applies, so a platform object in a namespace that merely shares the
// tenant's prefix (keos-core, for tenant keos) is never imported as one
// of the tenant's components.
func tenantObject(obj metav1.Object, tenantName string) bool {
	return inTenantNamespace(obj.GetNamespace(), tenantName) && components.BelongsToTenant(obj, tenantName)
}

// inTenantNamespace reports whether namespace belongs to tenantName:
// either exactly tenantName, or prefixed "tenantName-". A plain substring
// match would wrongly accept "stratiotest-apps" for tenant "stratio" —
// this only matches on a full "-" boundary.
func inTenantNamespace(namespace, tenantName string) bool {
	return namespace == tenantName || strings.HasPrefix(namespace, tenantName+"-")
}
