package tenantfile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const dependenciesTenant = `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  name: stratio
spec:
  defaultValues:
    components:
      dgAgent:
      - name: dg-hdfs-agent
      hdfs:
      - name: hdfs1
      rocket:
      - name: rocket
        config:
          dependencies:
            dgAgent:
              name: dg-agent
            hdfs:
              name: hdfs1
            virtualizer:
              name: virtualizer
            governancePostgres:
              name: postgreskeos
      bare:
      - name: bare
`

func TestUnresolvedDependencies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant.yaml")
	if err := os.WriteFile(path, []byte(dependenciesTenant), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// governancePostgres isn't a component key, so it can't be checked.
	isComponentKey := func(key string) bool { return key != "governancePostgres" }

	tests := []struct {
		entry string
		want  []UnresolvedDependency
	}{
		{"rocket", []UnresolvedDependency{
			{Key: "dgAgent", Name: "dg-agent", Declared: []string{"dg-hdfs-agent"}},
			{Key: "virtualizer", Name: "virtualizer"},
		}},
		{"bare", nil},
	}
	for _, tt := range tests {
		t.Run(tt.entry, func(t *testing.T) {
			entry, err := FindComponentEntry(d, tt.entry)
			if err != nil {
				t.Fatal(err)
			}
			got, err := UnresolvedDependencies(d, entry, isComponentKey)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UnresolvedDependencies = %+v, want %+v", got, tt.want)
			}
		})
	}
}
