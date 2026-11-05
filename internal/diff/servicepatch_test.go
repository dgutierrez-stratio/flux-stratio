package diff

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// chartWorkload is a rendered Deployment whose pods carry the chart's
// labels for controller ctrl, with one container declaring ports.
func chartWorkload(name, ctrl string, ports ...map[string]any) *unstructured.Unstructured {
	containerPorts := make([]any, len(ports))
	for i, p := range ports {
		containerPorts[i] = p
	}
	labels := map[string]any{"app.kubernetes.io/name": "virtualizer", "app.kubernetes.io/instance": "virtualizer", "app.kubernetes.io/component": ctrl}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": "stratio-apps"},
		"spec": map[string]any{
			"selector": map[string]any{"matchLabels": labels},
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec":     map[string]any{"containers": []any{map[string]any{"name": name, "ports": containerPorts}}},
			},
		},
	}}
}

// legacyLB is the Service CCT's createLoadBalancer creates.
func legacyLB(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{
			"name": name, "namespace": "stratio-apps",
			"annotations": map[string]any{
				"kubectl.kubernetes.io/last-applied-configuration": "{}",
				"cct.stratio.com/whatever":                         "x",
				"metallb.universe.tf/address-pool":                 "public",
				"metallb.io/ip-allocated-from-pool":                "public",
			},
		},
		"spec": map[string]any{
			"type":            "LoadBalancer",
			"selector":        map[string]any{"cct.stratio.com/application_id": "virtualizer.stratio-apps"},
			"ports":           []any{map[string]any{"name": "lb-port", "protocol": "TCP", "port": int64(13422), "targetPort": int64(13422), "nodePort": int64(31234)}},
			"sessionAffinity": "None", "externalTrafficPolicy": "Cluster",
		},
		"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "10.202.18.2"}}}},
	}}
}

func virtualizerValues(service map[string]any) map[string]any {
	values := map[string]any{"controllers": map[string]any{"virtualizer": map[string]any{}, "monitor": map[string]any{}}}
	if service != nil {
		values["service"] = service
	}
	return values
}

func TestServiceEdits_CarriesTheLegacyLoadBalancer(t *testing.T) {
	in := ChartDiffInput{Release: "virtualizer", LegacyServices: []LegacyService{{Service: legacyLB("virtualizer-loadbalancer"), Workload: chartWorkload("virtualizer", "virtualizer")}}}

	t.Run("the whole entry when the chart has none", func(t *testing.T) {
		patch, findings := serviceEdits(virtualizerValues(nil), in)
		want := map[string]any{"service": map[string]any{"loadbalancer": map[string]any{
			"enabled": true, "controller": "virtualizer", "nameOverride": "loadbalancer", "type": "LoadBalancer",
			"ports":          map[string]any{"lb-port": map[string]any{"enabled": true, "port": int64(13422), "protocol": "TCP"}},
			"loadBalancerIP": "10.202.18.2",
			"annotations":    map[string]any{"metallb.universe.tf/address-pool": "public"},
		}}}
		if !reflect.DeepEqual(patch, want) {
			t.Errorf("patch = %#v\nwant %#v", patch, want)
		}
		if len(findings) != 1 || findings[0].Path != "service.loadbalancer" || findings[0].Reason != "" ||
			findings[0].Address != "10.202.18.2" || findings[0].Ports != "13422/TCP" || findings[0].Workload != "virtualizer" {
			t.Errorf("findings = %+v", findings)
		}
	})

	t.Run("only what differs from the chart's entry, under the chart's port name", func(t *testing.T) {
		chart := map[string]any{"loadbalancer": map[string]any{
			"enabled": false, "controller": "virtualizer", "nameOverride": "loadbalancer", "type": "LoadBalancer",
			"ports": map[string]any{"api": map[string]any{"enabled": true, "port": 13422, "protocol": "TCP"}},
		}}
		patch, findings := serviceEdits(virtualizerValues(chart), in)
		want := map[string]any{"service": map[string]any{"loadbalancer": map[string]any{
			"enabled": true, "loadBalancerIP": "10.202.18.2",
			"annotations": map[string]any{"metallb.universe.tf/address-pool": "public"},
		}}}
		if !reflect.DeepEqual(patch, want) {
			t.Errorf("patch = %#v\nwant %#v", patch, want)
		}
		if len(findings) != 1 || findings[0].Path != "service.loadbalancer" {
			t.Errorf("findings = %+v", findings)
		}
	})

	t.Run("a NodePort keeps its node port, a different target port is written", func(t *testing.T) {
		svc := legacyLB("virtualizer-np")
		_ = unstructured.SetNestedField(svc.Object, "NodePort", "spec", "type")
		_ = unstructured.SetNestedSlice(svc.Object, []any{map[string]any{"name": "sql", "port": int64(13422), "targetPort": int64(8080), "nodePort": int64(31234)}}, "spec", "ports")
		patch, findings := serviceEdits(virtualizerValues(nil), ChartDiffInput{Release: "virtualizer", LegacyServices: []LegacyService{{Service: svc, Workload: chartWorkload("virtualizer", "virtualizer")}}})
		entry := patch["service"].(map[string]any)["np"].(map[string]any)
		wantPorts := map[string]any{"sql": map[string]any{"enabled": true, "port": int64(13422), "targetPort": int64(8080), "nodePort": int64(31234)}}
		if !reflect.DeepEqual(entry["ports"], wantPorts) || entry["type"] != "NodePort" || entry["loadBalancerIP"] != nil {
			t.Errorf("entry = %#v", entry)
		}
		if findings[0].Address != "node ports 31234" {
			t.Errorf("address = %q", findings[0].Address)
		}
	})
}

func TestServiceEdits_NotCarried(t *testing.T) {
	named := legacyLB("virtualizer-loadbalancer")
	_ = unstructured.SetNestedSlice(named.Object, []any{map[string]any{"port": int64(13422), "targetPort": "sql"}}, "spec", "ports")
	for name, tt := range map[string]struct {
		svc      *unstructured.Unstructured
		workload *unstructured.Unstructured
		values   map[string]any
		exclude  []string
		reason   string
	}{
		"a name the chart can't render": {legacyLB("sql-lb"), chartWorkload("virtualizer", "virtualizer"), virtualizerValues(nil), nil,
			"the chart names its Services virtualizer-<name>"},
		"no controller for the workload": {legacyLB("virtualizer-loadbalancer"), chartWorkload("virtualizer", "unknown"), virtualizerValues(nil), nil,
			"the chart controller of virtualizer can't be told"},
		"a named target port the new pod lacks": {named, chartWorkload("virtualizer", "virtualizer"), virtualizerValues(nil), nil,
			`its port 13422 targets the container port "sql", which virtualizer doesn't declare`},
		"excluded by the catalog": {legacyLB("virtualizer-loadbalancer"), chartWorkload("virtualizer", "virtualizer"), virtualizerValues(nil),
			[]string{"spec.values.service.loadbalancer"}, "the catalog excludes service.loadbalancer"},
	} {
		t.Run(name, func(t *testing.T) {
			patch, findings := serviceEdits(tt.values, ChartDiffInput{Release: "virtualizer", Exclude: tt.exclude,
				LegacyServices: []LegacyService{{Service: tt.svc, Workload: tt.workload}}})
			if patch != nil {
				t.Errorf("patch = %#v, want none", patch)
			}
			if len(findings) != 1 || findings[0].Path != "" || findings[0].Reason != tt.reason {
				t.Errorf("findings = %+v, want reason %q", findings, tt.reason)
			}
		})
	}

	t.Run("a named target port the new pod declares is carried", func(t *testing.T) {
		workload := chartWorkload("virtualizer", "virtualizer", map[string]any{"name": "sql", "containerPort": int64(13422)})
		_, findings := serviceEdits(virtualizerValues(nil), ChartDiffInput{Release: "virtualizer", LegacyServices: []LegacyService{{Service: named, Workload: workload}}})
		if findings[0].Path != "service.loadbalancer" {
			t.Errorf("findings = %+v", findings)
		}
	})
}

func TestServiceEdits_ClusterIPIsReportedWithItsReplacement(t *testing.T) {
	legacy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": "dg-hdfs-agent", "namespace": "stratio-datastores"},
		"spec": map[string]any{
			"selector": map[string]any{"cct.stratio.com/application_id": "dg-hdfs-agent.stratio-datastores"},
			"ports":    []any{map[string]any{"name": "metrics", "port": int64(13421)}},
		},
	}}
	workload := chartWorkload("dg-hdfs-agent", "dg-agent")
	podLabels := map[string]any{"app.kubernetes.io/name": "virtualizer", "app.kubernetes.io/instance": "virtualizer", "app.kubernetes.io/component": "dg-agent"}
	metrics := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": "dg-hdfs-agent-metrics", "labels": map[string]any{"app.kubernetes.io/service": "dg-hdfs-agent-metrics"}},
		"spec":     map[string]any{"selector": podLabels, "ports": []any{map[string]any{"name": "metrics", "port": int64(13421)}}},
	}}
	monitor := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "monitoring.coreos.com/v1", "kind": "ServiceMonitor",
		"metadata": map[string]any{"name": "dg-hdfs-agent"},
		"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/service": "dg-hdfs-agent-metrics"}}},
	}}
	in := ChartDiffInput{Release: "dg-hdfs-agent", LegacyServices: []LegacyService{{Service: legacy, Workload: workload}}}

	for name, tt := range map[string]struct {
		rendered    []*unstructured.Unstructured
		replacement string
		monitored   bool
	}{
		"replaced and scraped":  {[]*unstructured.Unstructured{workload, metrics, monitor}, "dg-hdfs-agent-metrics", true},
		"replaced, not scraped": {[]*unstructured.Unstructured{workload, metrics}, "dg-hdfs-agent-metrics", false},
		"nothing replaces it":   {[]*unstructured.Unstructured{workload}, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			in.Rendered = tt.rendered
			patch, findings := serviceEdits(virtualizerValues(nil), in)
			if patch != nil {
				t.Errorf("patch = %#v, a ClusterIP Service is never carried", patch)
			}
			if len(findings) != 1 || findings[0].Exposed() || findings[0].Replacement != tt.replacement || findings[0].Monitored != tt.monitored {
				t.Errorf("findings = %+v", findings)
			}
		})
	}
}

func TestVerifyServices(t *testing.T) {
	result := func() *ChartDiffResult {
		return &ChartDiffResult{
			Patch: &PatchDoc{TargetKind: "HelmRelease", Patch: map[string]any{"spec": map[string]any{"values": map[string]any{
				"service": map[string]any{"loadbalancer": map[string]any{"enabled": true}},
			}}}},
			Services: []ServiceFinding{{Name: "virtualizer-loadbalancer", Type: "LoadBalancer", Workload: "virtualizer", Path: "service.loadbalancer", livePorts: []int64{13422}}},
		}
	}
	workload := chartWorkload("virtualizer", "virtualizer")
	rendered := func(svcType string, selector map[string]any, port int64) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "virtualizer-loadbalancer"},
			"spec": map[string]any{"type": svcType, "selector": selector, "ports": []any{map[string]any{"port": port}}},
		}}
	}
	chartSelector := map[string]any{"app.kubernetes.io/name": "virtualizer", "app.kubernetes.io/instance": "virtualizer", "app.kubernetes.io/component": "virtualizer"}

	t.Run("rendered as carried: kept", func(t *testing.T) {
		r := result()
		if failed := VerifyServices(r, []*unstructured.Unstructured{workload, rendered("LoadBalancer", chartSelector, 13422)}); len(failed) != 0 || r.Patch == nil || r.Services[0].Path == "" {
			t.Errorf("failed = %+v, services = %+v", failed, r.Services)
		}
	})

	for name, svc := range map[string]*unstructured.Unstructured{
		"not rendered":    nil,
		"another type":    rendered("ClusterIP", chartSelector, 13422),
		"another port":    rendered("LoadBalancer", chartSelector, 1),
		"other pods":      rendered("LoadBalancer", map[string]any{"app.kubernetes.io/component": "monitor"}, 13422),
		"legacy selector": rendered("LoadBalancer", map[string]any{"cct.stratio.com/application_id": "virtualizer.stratio-apps"}, 13422),
	} {
		t.Run(name+": taken back out of the patch", func(t *testing.T) {
			r := result()
			docs := []*unstructured.Unstructured{workload}
			if svc != nil {
				docs = append(docs, svc)
			}
			failed := VerifyServices(r, docs)
			if len(failed) != 1 || r.Services[0].Path != "" || r.Services[0].Reason != "the chart doesn't render it as carried" || r.Patch != nil {
				t.Errorf("failed = %+v, services = %+v, patch = %+v", failed, r.Services, r.Patch)
			}
		})
	}
}
