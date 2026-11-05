package appdiff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

var (
	newPodLabels    = map[string]any{"app.kubernetes.io/name": "virtualizer", "app.kubernetes.io/component": "virtualizer"}
	legacyPodLabels = map[string]any{"cct.stratio.com/application_id": "virtualizer.stratio-apps", "app": "virtualizer"}
)

func podWorkload(namespace, name string, labels map[string]any, extraLabels map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"selector": map[string]any{"matchLabels": labels},
			"template": map[string]any{"metadata": map[string]any{"labels": labels}},
		},
	}}
	if extraLabels != nil {
		u.SetLabels(toStrings(extraLabels))
	}
	return u
}

func service(namespace, name string, selector map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec":     map[string]any{"type": "LoadBalancer", "selector": selector, "ports": []any{map[string]any{"port": int64(13422)}}},
	}}
}

func toStrings(m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v.(string)
	}
	return out
}

func names(found []diff.LegacyService) []string {
	var out []string
	for _, f := range found {
		out = append(out, f.Service.GetName())
	}
	return out
}

// TestFindLegacyServices: a Service the cutover leaves selecting nothing is
// found before the cutover (it selects the legacy workload's pods) and
// after it (the live workload is the chart's; CCT's own selector for the
// workload still names it). Everything the chart, Flux or Helm will serve or
// owns is left out.
func TestFindLegacyServices(t *testing.T) {
	rendered := podWorkload("", "virtualizer", newPodLabels, nil)
	targets := []workloadTarget{{rendered: rendered, gvk: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, namespace: "stratio-apps", name: "virtualizer"}}
	legacyLive := []diff.LiveWorkloadEnv{{Rendered: rendered, Live: podWorkload("stratio-apps", "virtualizer", legacyPodLabels, nil)}}
	migratedLive := []diff.LiveWorkloadEnv{{Rendered: rendered, Live: podWorkload("stratio-apps", "virtualizer", newPodLabels, map[string]any{helmNameLabel: "virtualizer"})}}
	renderedSvc := service("", "virtualizer", newPodLabels)
	docs := []*unstructured.Unstructured{rendered, renderedSvc}
	cct := map[string]any{"cct.stratio.com/application_id": "virtualizer.stratio-apps"}

	managedSvc := service("stratio-apps", "managed", cct)
	managedSvc.SetLabels(map[string]string{helmNameLabel: "virtualizer"})
	adopted := service("stratio-apps", "adopted", cct)
	adopted.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "virtualizer"})
	owned := service("stratio-apps", "owned", cct)
	owned.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: "parent", UID: "u"}})

	for name, tt := range map[string]struct {
		live     []diff.LiveWorkloadEnv
		services []*unstructured.Unstructured
		want     []string
	}{
		"before the cutover: selects a subset of the legacy pod labels": {
			legacyLive, []*unstructured.Unstructured{service("stratio-apps", "virtualizer-loadbalancer", map[string]any{"app": "virtualizer"})},
			[]string{"virtualizer-loadbalancer"},
		},
		"after the cutover: CCT's selector for the workload": {
			migratedLive, []*unstructured.Unstructured{service("stratio-apps", "virtualizer-loadbalancer", cct)},
			[]string{"virtualizer-loadbalancer"},
		},
		"after the cutover, another legacy label is not recognised": {
			migratedLive, []*unstructured.Unstructured{service("stratio-apps", "virtualizer-loadbalancer", map[string]any{"app": "virtualizer"})},
			nil,
		},
		"a Service of the same name the chart renders: Helm takes it over": {
			legacyLive, []*unstructured.Unstructured{service("stratio-apps", "virtualizer", cct)}, nil,
		},
		"a Service selecting the new pods": {
			legacyLive, []*unstructured.Unstructured{service("stratio-apps", "other", map[string]any{"app.kubernetes.io/name": "virtualizer"})}, nil,
		},
		"another namespace, another app, no selector": {
			legacyLive, []*unstructured.Unstructured{
				service("stratio-datastores", "lb", cct),
				service("stratio-apps", "rocket-lb", map[string]any{"cct.stratio.com/application_id": "rocket.stratio-apps"}),
				service("stratio-apps", "bare", nil),
			}, nil,
		},
		"Flux-managed, taken over by Helm, or owned": {
			legacyLive, []*unstructured.Unstructured{managedSvc, adopted, owned}, nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := names(findLegacyServices(tt.services, targets, tt.live, docs, "stratio-apps", "stratio-apps"))
			if len(got) != len(tt.want) || (len(got) > 0 && got[0] != tt.want[0]) {
				t.Errorf("found %v, want %v", got, tt.want)
			}
		})
	}
}

// A release can live elsewhere than the legacy app it replaces: the legacy
// Service is found in the app's live namespace, before the cutover (the
// legacy workload was found there) and after it (CCT's selector).
func TestFindLegacyServices_InTheAppsLiveNamespace(t *testing.T) {
	rendered := podWorkload("stratio-apps", "governance-datamarket-agent", newPodLabels, nil)
	targets := []workloadTarget{{rendered: rendered, gvk: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, namespace: "stratio-apps", name: "datamarket-agent"}}
	legacyPods := map[string]any{"cct.stratio.com/application_id": "datamarket-agent.stratio-datastores"}
	svc := service("stratio-datastores", "datamarket-agent", legacyPods)
	for name, live := range map[string][]diff.LiveWorkloadEnv{
		"legacy workload found live": {{Rendered: rendered, Live: podWorkload("stratio-datastores", "datamarket-agent", legacyPods, nil)}},
		"legacy workload gone":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			got := names(findLegacyServices([]*unstructured.Unstructured{svc}, targets, live, []*unstructured.Unstructured{rendered}, "stratio-apps", "stratio-datastores"))
			if len(got) != 1 || got[0] != "datamarket-agent" {
				t.Errorf("found %v, want datamarket-agent", got)
			}
		})
	}
}

func TestReadBaselineServices(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, obj map[string]any) {
		data, err := yaml.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(ServiceManifestFile("virtualizer-loadbalancer"), service("stratio-apps", "virtualizer-loadbalancer", map[string]any{"a": "b"}).Object)
	write("deployment.yaml", podWorkload("stratio-apps", "virtualizer", legacyPodLabels, nil).Object)
	got, err := readBaselineServices(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GetName() != "virtualizer-loadbalancer" {
		t.Errorf("read %v", got)
	}
}

func TestServicesSelecting(t *testing.T) {
	wl := podWorkload("stratio-apps", "virtualizer", legacyPodLabels, nil)
	selecting := service("stratio-apps", "virtualizer-loadbalancer", map[string]any{"app": "virtualizer"})
	other := service("stratio-apps", "rocket", map[string]any{"app": "rocket"})
	elsewhere := service("keos-core", "virtualizer", map[string]any{"app": "virtualizer"})
	c := fake.NewClientBuilder().WithObjects(selecting, other, elsewhere).Build()
	got, err := ServicesSelecting(context.Background(), c, []*unstructured.Unstructured{wl})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GetName() != "virtualizer-loadbalancer" {
		t.Errorf("got %v", got)
	}
}

// serviceHelmSim is a helmSim that also renders each enabled service.<key>
// of the values it's given the way the common library does: a Service
// "<release>-<nameOverride>" selecting the pods of its controller's
// workload, on its ports, with its type and loadBalancerIP.
type serviceHelmSim struct{ helmSim }

func (h serviceHelmSim) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	stdout, stderr, err := h.helmSim.Run(ctx, name, args...)
	if err != nil || name != "helm" || len(args) < 3 || args[0] != "template" {
		return stdout, stderr, err
	}
	var values map[string]any
	for i, a := range args {
		if a == "-f" && i+1 < len(args) {
			data, err := os.ReadFile(args[i+1])
			if err != nil {
				return nil, nil, err
			}
			if err := yaml.Unmarshal(data, &values); err != nil {
				return nil, nil, err
			}
		}
	}
	docs, err := yamldocs.Decode(stdout)
	if err != nil {
		return nil, nil, err
	}
	services, _ := values["service"].(map[string]any)
	out := string(stdout)
	for _, raw := range services {
		svc, _ := raw.(map[string]any)
		if svc["enabled"] != true {
			continue
		}
		var selector map[string]any
		for _, d := range docs {
			labels := d.GetLabels()
			if d.GetKind() == "Deployment" && labels["app.kubernetes.io/component"] == svc["controller"] {
				selector, _, _ = unstructured.NestedMap(d.Object, "spec", "selector", "matchLabels")
			}
		}
		var ports []any
		for _, p := range svc["ports"].(map[string]any) {
			ports = append(ports, map[string]any{"port": p.(map[string]any)["port"]})
		}
		spec := map[string]any{"type": svc["type"], "selector": selector, "ports": ports}
		if ip := svc["loadBalancerIP"]; ip != nil {
			spec["loadBalancerIP"] = ip
		}
		data, err := yaml.Marshal(map[string]any{
			"apiVersion": "v1", "kind": "Service",
			"metadata": map[string]any{"name": fmt.Sprintf("%s-%s", args[1], svc["nameOverride"])},
			"spec":     spec,
		})
		if err != nil {
			return nil, nil, err
		}
		out += "---\n" + string(data)
	}
	return []byte(out), stderr, nil
}

const helmTemplateOutputWithController = `
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: psql-gosec-agent-config
data:
  LOG_LEVEL: INFO
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: psql-gosec-agent
  namespace: stratio-datastores
  labels:
    app.kubernetes.io/component: main
spec:
  selector:
    matchLabels:
      app.kubernetes.io/component: main
  template:
    metadata:
      labels:
        app.kubernetes.io/component: main
    spec:
      containers:
        - name: main
`

// TestDiff_ChartMode_CarriesTheLegacyLoadBalancer runs the whole chart diff
// against a live legacy workload reached through a LoadBalancer the chart
// doesn't render: the patch declares it under its own name, with its
// address, when the patched chart renders it that way, and takes it back
// out when it doesn't.
func TestDiff_ChartMode_CarriesTheLegacyLoadBalancer(t *testing.T) {
	for name, tt := range map[string]struct {
		renderServices bool
		wantPath       string
		wantReason     string
	}{
		"rendered as carried": {true, "service.lb", ""},
		"not rendered":        {false, "", "the chart doesn't render it as carried"},
	} {
		t.Run(name, func(t *testing.T) {
			base := fixtureBase(t)
			if err := os.MkdirAll(filepath.Join(base, "keos-apps", "components", "gosec-agent", "app", "overlays", "postgres", "S"), 0o755); err != nil {
				t.Fatal(err)
			}
			opts := chartDiffOptions(t, base)
			if err := os.WriteFile(filepath.Join(base, "charts", "gosec-agent", "values.yaml"), []byte("controllers:\n  main: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			sim := helmSim{&runner.Fake{Responses: map[string]runner.FakeResponse{
				"flux-operator": {Stdout: []byte(rsetOutputHelmRelease)},
				"flux":          {Stdout: []byte(kustomizationBuildOutputHelmRelease)},
				"helm":          {Stdout: []byte(helmTemplateOutputWithController)},
			}}}
			opts.Runner = sim
			if tt.renderServices {
				opts.Runner = serviceHelmSim{sim}
			}
			lb := service("stratio-datastores", "psql-gosec-agent-lb", map[string]any{"app": "psql-gosec-agent"})
			_ = unstructured.SetNestedSlice(lb.Object, []any{map[string]any{"ip": "10.0.0.7"}}, "status", "loadBalancer", "ingress")
			opts.Client = fake.NewClientBuilder().WithScheme(mustScheme(t)).
				WithObjects(deploymentWithEnv(t, "psql-gosec-agent", "stratio-datastores", "INFO"), lb).Build()

			result, err := Diff(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Services) != 1 || result.Services[0].Path != tt.wantPath || result.Services[0].Reason != tt.wantReason ||
				result.Services[0].Address != "10.0.0.7" {
				t.Fatalf("services = %+v", result.Services)
			}
			var carried any
			if result.Patch != nil {
				carried, _, _ = unstructured.NestedFieldNoCopy(result.Patch.Patch.(map[string]any), "spec", "values", "service", "lb", "loadBalancerIP")
			}
			if tt.renderServices && carried != "10.0.0.7" {
				t.Errorf("patch = %+v, want service.lb.loadBalancerIP 10.0.0.7", result.Patch)
			}
			if !tt.renderServices && carried != nil {
				t.Errorf("patch = %+v, want no service", result.Patch)
			}
		})
	}
}
