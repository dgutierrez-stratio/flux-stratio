package tenantfile

import (
	"path/filepath"
	"testing"
)

func TestPath(t *testing.T) {
	got := Path("/stratio/gitops", "eosdev", "stratio")
	want := filepath.Join("/stratio/gitops", "keos-fleet", "clusters", "eosdev", "tenants", "config", "stratio.yaml")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}
