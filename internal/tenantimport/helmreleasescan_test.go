package tenantimport

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func helmRelease(name, namespace, chart string, values map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"chart": map[string]any{"spec": map[string]any{"chart": chart}},
		},
	}
	if values != nil {
		spec := obj["spec"].(map[string]any)
		spec["values"] = values
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestScanHelmReleases_EnrichesAlreadyDiscoveredEntry(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		helmRelease("virtualizer", "stratio-apps", "virtualizer", map[string]any{"storageType": "hdfs"}),
	).Build()

	components := Components{"virtualizer": {{Name: "virtualizer", Deps: map[string]string{}}}}
	if err := scanHelmReleases(context.Background(), c, cat, "stratio", components); err != nil {
		t.Fatal(err)
	}
	if len(components["virtualizer"]) != 1 {
		t.Fatalf("virtualizer entries = %+v, want still 1 (enriched, not duplicated)", components["virtualizer"])
	}
	entry := components["virtualizer"][0]
	if entry.Type != "hdfs" {
		t.Errorf("Type = %q, want hdfs", entry.Type)
	}
	if entry.HelmReleaseSpec == nil {
		t.Error("HelmReleaseSpec not captured")
	}
}

func TestScanHelmReleases_AddsEntryWhenNoBackingDeployment(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		helmRelease("connectors", "stratio-apps", "connectors", nil),
	).Build()

	components := Components{}
	if err := scanHelmReleases(context.Background(), c, cat, "stratio", components); err != nil {
		t.Fatal(err)
	}
	if names := components.names("connectors"); len(names) != 1 || names[0] != "connectors" {
		t.Errorf("connectors entries = %v", names)
	}
}

func TestScanHelmReleases_OutsideTenantIgnored(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).WithObjects(
		helmRelease("connectors", "other-apps", "connectors", nil),
	).Build()
	components := Components{}
	if err := scanHelmReleases(context.Background(), c, cat, "stratio", components); err != nil {
		t.Fatal(err)
	}
	if len(components) != 0 {
		t.Errorf("components = %+v, want empty", components)
	}
}

func TestScanHelmReleases_HelmReleaseCRDNotInstalledNoError(t *testing.T) {
	cat := loadFixtureCatalog(t)
	c := fake.NewClientBuilder().WithScheme(mustScheme(t)).Build()
	if err := scanHelmReleases(context.Background(), c, cat, "stratio", Components{}); err != nil {
		t.Errorf("scanHelmReleases returned error: %v, want nil (missing CRD is expected)", err)
	}
}

func TestInferStorageType(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		want string
	}{
		{"explicit storageType", map[string]any{"values": map[string]any{"storageType": "S3"}}, "s3"},
		{"explicit storage_type", map[string]any{"values": map[string]any{"storage_type": "hdfs"}}, "hdfs"},
		{"explicit type", map[string]any{"values": map[string]any{"type": "hdfs"}}, "hdfs"},
		{"hdfs key present implies hdfs", map[string]any{"values": map[string]any{"hdfs": map[string]any{}}}, "hdfs"},
		{"hdfsEnabled key present implies hdfs", map[string]any{"values": map[string]any{"hdfsEnabled": true}}, "hdfs"},
		{"nothing recognizable", map[string]any{"values": map[string]any{"foo": "bar"}}, ""},
		{"no values at all", map[string]any{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inferStorageType(c.spec); got != c.want {
				t.Errorf("inferStorageType(%v) = %q, want %q", c.spec, got, c.want)
			}
		})
	}
}
