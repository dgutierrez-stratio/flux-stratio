package diff

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// LegacyService is a live Service that reaches an app's legacy pods and no
// pod the chart renders (internal/appdiff finds them): at the cutover it is
// left selecting nothing, unless the patch carries it.
type LegacyService struct {
	Service *unstructured.Unstructured
	// Workload is the rendered workload whose legacy counterpart the
	// Service selects.
	Workload *unstructured.Unstructured
}

// ServiceFinding is what the chart diff makes of one LegacyService. An
// exposed one (LoadBalancer, NodePort) is carried into the patch as a
// service.<key> entry rendering the same name, so Helm takes the live
// Service over, with its address, instead of leaving it behind (Path); or
// it can't be (Reason). A ClusterIP one is never carried: it is a leftover,
// usually replaced by a Service the chart renders under another name.
type ServiceFinding struct {
	Namespace, Name, Type string
	// Address is where clients reach an exposed Service from outside: its
	// load-balancer addresses, or its node ports. "" for ClusterIP.
	Address string
	// Ports are the live ports, as "13422/TCP".
	Ports string
	// Workload is the rendered workload whose legacy pods it selects.
	Workload string
	// Path is the .Values path the patch carries it through
	// ("service.loadbalancer"), "" when it isn't carried.
	Path string
	// Reason says why an exposed Service is not carried.
	Reason string
	// Replacement is a Service the chart renders for the same workload on
	// one of its ports (ClusterIP findings only), "" for none.
	Replacement string
	// Monitored is set when a rendered ServiceMonitor scrapes Replacement.
	Monitored bool
	// livePorts are the live port numbers, for VerifyServices.
	livePorts []int64
}

// Exposed reports whether the Service is reachable from outside the cluster.
func (f ServiceFinding) Exposed() bool {
	return f.Type == "LoadBalancer" || f.Type == "NodePort"
}

// droppedAnnotationPrefixes are the live annotations a carried Service
// leaves behind: CCT's own, kubectl's record of its last apply, and Helm's
// ownership (Helm writes its own on takeover).
var droppedAnnotationPrefixes = []string{"cct.stratio.com/", "kubectl.kubernetes.io/last-applied-configuration", "meta.helm.sh/"}

// serviceEdits builds the patch values that carry each exposed legacy
// Service under service.<key>, where <key> is its name without the
// release's "<release>-" prefix (the name the common library renders for
// nameOverride <key>), and a finding for every legacy Service. When the
// chart already declares service.<key> (in.Values), only what differs from
// it is written: a port of the same number keeps the chart's port name, so
// the Service never gets the same port twice.
func serviceEdits(values map[string]any, in ChartDiffInput) (map[string]any, []ServiceFinding) {
	patch := map[string]any{}
	var findings []ServiceFinding
	for _, ls := range in.LegacyServices {
		f := newServiceFinding(ls)
		if !f.Exposed() {
			f.Replacement, f.Monitored = replacementFor(ls, in.Rendered)
			findings = append(findings, f)
			continue
		}
		key, ok := strings.CutPrefix(f.Name, in.Release+"-")
		if in.Release == "" || !ok || key == "" {
			f.Reason = fmt.Sprintf("the chart names its Services %s-<name>", in.Release)
			findings = append(findings, f)
			continue
		}
		if isExcluded("service."+key, in.Exclude) {
			f.Reason = "the catalog excludes service." + key
			findings = append(findings, f)
			continue
		}
		existing, _ := nestedMap(values, "service", key)
		entry, reason := carriedService(values, existing, key, ls)
		if reason != "" {
			f.Reason = reason
			findings = append(findings, f)
			continue
		}
		if delta := changedFields(entry, existing); len(delta) > 0 {
			patch[key] = delta
		}
		f.Path = "service." + key
		findings = append(findings, f)
	}
	if len(patch) == 0 {
		return nil, findings
	}
	return map[string]any{"service": patch}, findings
}

// carriedService is the service.<key> entry that renders ls's Service as
// it is live, for the controller of the rendered workload it reaches; a
// reason instead when that can't be done.
func carriedService(values, existing map[string]any, key string, ls LegacyService) (map[string]any, string) {
	svc := ls.Service
	controller := controllerOf(values, ls.Workload)
	if controller == "" {
		return nil, "the chart controller of " + ls.Workload.GetName() + " can't be told"
	}
	svcType := serviceType(svc)
	existingNames := map[int64]string{}
	if ports, ok := existing["ports"].(map[string]any); ok {
		for name, p := range ports {
			if pm, ok := p.(map[string]any); ok {
				existingNames[toInt64(pm["port"])] = name
			}
		}
	}
	ports := map[string]any{}
	livePorts, _, _ := unstructured.NestedSlice(svc.Object, "spec", "ports")
	for _, p := range livePorts {
		pm, _ := p.(map[string]any)
		number := toInt64(pm["port"])
		name := existingNames[number]
		if name == "" {
			name, _ = pm["name"].(string)
		}
		if name == "" {
			name = fmt.Sprintf("port-%d", number)
		}
		out := map[string]any{"enabled": true, "port": number}
		if protocol, _ := pm["protocol"].(string); protocol != "" {
			out["protocol"] = protocol
		}
		switch target := pm["targetPort"].(type) {
		case string:
			if !hasContainerPort(ls.Workload, target) {
				return nil, fmt.Sprintf("its port %d targets the container port %q, which %s doesn't declare", number, target, ls.Workload.GetName())
			}
			out["targetPort"] = target
		default:
			if n := toInt64(target); n != 0 && n != number {
				out["targetPort"] = n
			}
		}
		if svcType == "NodePort" {
			if n := toInt64(pm["nodePort"]); n != 0 {
				out["nodePort"] = n
			}
		}
		ports[name] = out
	}
	entry := map[string]any{"enabled": true, "controller": controller, "nameOverride": key, "type": svcType, "ports": ports}
	if svcType == "LoadBalancer" {
		if ip := loadBalancerIP(svc); ip != "" {
			entry["loadBalancerIP"] = ip
		}
		if class, _, _ := unstructured.NestedString(svc.Object, "spec", "loadBalancerClass"); class != "" {
			entry["loadBalancerClass"] = class
		}
		if ranges, _, _ := unstructured.NestedStringSlice(svc.Object, "spec", "loadBalancerSourceRanges"); len(ranges) > 0 {
			entry["loadBalancerSourceRanges"] = toAnySlice(ranges)
		}
	}
	if policy, _, _ := unstructured.NestedString(svc.Object, "spec", "externalTrafficPolicy"); policy != "" && policy != "Cluster" {
		entry["externalTrafficPolicy"] = policy
	}
	if affinity, _, _ := unstructured.NestedString(svc.Object, "spec", "sessionAffinity"); affinity != "" && affinity != "None" {
		entry["sessionAffinity"] = affinity
	}
	if annotations := carriedAnnotations(svc); len(annotations) > 0 {
		entry["annotations"] = annotations
	}
	return entry, ""
}

// changedFields is entry without what existing already sets to the same
// value: ports compared port by port and field by field. All of entry when
// existing is nil.
func changedFields(entry, existing map[string]any) map[string]any {
	if existing == nil {
		return entry
	}
	delta := map[string]any{}
	for k, v := range entry {
		if k != "ports" {
			if !sameJSON(v, existing[k]) {
				delta[k] = v
			}
			continue
		}
		existingPorts, _ := existing["ports"].(map[string]any)
		ports := map[string]any{}
		for name, p := range v.(map[string]any) {
			existingPort, _ := existingPorts[name].(map[string]any)
			if changed := changedFields(p.(map[string]any), existingPort); len(changed) > 0 {
				ports[name] = changed
			}
		}
		if len(ports) > 0 {
			delta["ports"] = ports
		}
	}
	return delta
}

// VerifyServices checks every carried Service against patched, the chart
// rendered again with the patch applied: it must come out under its live
// name and type, on every live port, selecting the pods of the workload it
// was carried for. One that doesn't has its path taken back out of the
// patch and becomes not carried. It returns those; Patch becomes nil if
// nothing is left in it.
func VerifyServices(result *ChartDiffResult, patched []*unstructured.Unstructured) []ServiceFinding {
	if result.Patch == nil {
		return nil
	}
	obj, _ := result.Patch.Patch.(map[string]any)
	var failed []ServiceFinding
	for i, f := range result.Services {
		if f.Path == "" || renderedAsCarried(f, patched) {
			continue
		}
		removeDotPath(obj, "spec.values."+f.Path)
		result.Services[i].Path = ""
		result.Services[i].Reason = "the chart doesn't render it as carried"
		failed = append(failed, result.Services[i])
	}
	if len(failed) > 0 {
		pruneEmptyMaps(obj)
		if len(PatchValues(result.Patch)) == 0 {
			result.Patch = nil
		}
	}
	return failed
}

func renderedAsCarried(f ServiceFinding, patched []*unstructured.Unstructured) bool {
	var svc, workload *unstructured.Unstructured
	for _, d := range patched {
		switch {
		case d.GetKind() == "Service" && d.GetName() == f.Name:
			svc = d
		case d.GetName() == f.Workload && d.GetKind() != "Service":
			if _, found, _ := unstructured.NestedMap(d.Object, "spec", "template"); found {
				workload = d
			}
		}
	}
	if svc == nil || workload == nil || serviceType(svc) != f.Type {
		return false
	}
	selector, _, _ := stringMapAt(svc.Object, "spec", "selector")
	podLabels, _, _ := stringMapAt(workload.Object, "spec", "template", "metadata", "labels")
	if len(selector) == 0 || !isSubset(selector, podLabels) {
		return false
	}
	rendered := map[int64]bool{}
	for _, n := range portNumbers(svc) {
		rendered[n] = true
	}
	for _, n := range f.livePorts {
		if !rendered[n] {
			return false
		}
	}
	return true
}

func newServiceFinding(ls LegacyService) ServiceFinding {
	svc := ls.Service
	f := ServiceFinding{
		Namespace: svc.GetNamespace(), Name: svc.GetName(), Type: serviceType(svc),
		Workload: ls.Workload.GetName(), livePorts: portNumbers(svc),
	}
	var ports, nodePorts []string
	livePorts, _, _ := unstructured.NestedSlice(svc.Object, "spec", "ports")
	for _, p := range livePorts {
		pm, _ := p.(map[string]any)
		protocol, _ := pm["protocol"].(string)
		if protocol == "" {
			protocol = "TCP"
		}
		ports = append(ports, fmt.Sprintf("%d/%s", toInt64(pm["port"]), protocol))
		if n := toInt64(pm["nodePort"]); n != 0 {
			nodePorts = append(nodePorts, fmt.Sprint(n))
		}
	}
	f.Ports = strings.Join(ports, ", ")
	switch f.Type {
	case "LoadBalancer":
		f.Address = strings.Join(loadBalancerAddresses(svc), ", ")
	case "NodePort":
		if len(nodePorts) > 0 {
			f.Address = "node ports " + strings.Join(nodePorts, ", ")
		}
	}
	return f
}

// replacementFor is the first Service (by name) rendered for ls's workload
// on one of the legacy Service's ports, and whether a rendered
// ServiceMonitor selects it.
func replacementFor(ls LegacyService, rendered []*unstructured.Unstructured) (string, bool) {
	podLabels, _, _ := stringMapAt(ls.Workload.Object, "spec", "template", "metadata", "labels")
	wanted := map[int64]bool{}
	livePorts, _, _ := unstructured.NestedSlice(ls.Service.Object, "spec", "ports")
	for _, p := range livePorts {
		pm, _ := p.(map[string]any)
		wanted[toInt64(pm["port"])] = true
		if n := toInt64(pm["targetPort"]); n != 0 {
			wanted[n] = true
		}
	}
	var candidates []*unstructured.Unstructured
	for _, d := range rendered {
		if d.GetKind() != "Service" {
			continue
		}
		selector, _, _ := stringMapAt(d.Object, "spec", "selector")
		if len(selector) == 0 || !isSubset(selector, podLabels) {
			continue
		}
		for _, n := range portNumbers(d) {
			if wanted[n] {
				candidates = append(candidates, d)
				break
			}
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].GetName() < candidates[j].GetName() })
	replacement := candidates[0]
	for _, d := range rendered {
		if d.GetKind() != "ServiceMonitor" {
			continue
		}
		selector, _, _ := stringMapAt(d.Object, "spec", "selector", "matchLabels")
		if len(selector) > 0 && isSubset(selector, replacement.GetLabels()) {
			return replacement.GetName(), true
		}
	}
	return replacement.GetName(), false
}

// controllerOf is the controllers.<key> of values that renders workload:
// the one its pod labels name (see controllerLabels), or the only one.
func controllerOf(values map[string]any, workload *unstructured.Unstructured) string {
	controllers, _ := values["controllers"].(map[string]any)
	labels, _, _ := stringMapAt(workload.Object, "spec", "template", "metadata", "labels")
	for _, label := range controllerLabels {
		if c := labels[label]; c != "" {
			if _, ok := controllers[c]; ok {
				return c
			}
		}
	}
	if len(controllers) == 1 {
		for c := range controllers {
			return c
		}
	}
	return ""
}

func hasContainerPort(workload *unstructured.Unstructured, name string) bool {
	containers, _, _ := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		cm, _ := c.(map[string]any)
		ports, _ := cm["ports"].([]any)
		for _, p := range ports {
			if pm, _ := p.(map[string]any); pm["name"] == name {
				return true
			}
		}
	}
	return false
}

func serviceType(svc *unstructured.Unstructured) string {
	if t, _, _ := unstructured.NestedString(svc.Object, "spec", "type"); t != "" {
		return t
	}
	return "ClusterIP"
}

func portNumbers(svc *unstructured.Unstructured) []int64 {
	var out []int64
	ports, _, _ := unstructured.NestedSlice(svc.Object, "spec", "ports")
	for _, p := range ports {
		pm, _ := p.(map[string]any)
		out = append(out, toInt64(pm["port"]))
	}
	return out
}

// loadBalancerIP is the address to pin: the one the Service asked for, else
// the first IP its load balancer was given.
func loadBalancerIP(svc *unstructured.Unstructured) string {
	if ip, _, _ := unstructured.NestedString(svc.Object, "spec", "loadBalancerIP"); ip != "" {
		return ip
	}
	ingress, _, _ := unstructured.NestedSlice(svc.Object, "status", "loadBalancer", "ingress")
	for _, i := range ingress {
		if im, _ := i.(map[string]any); im["ip"] != nil {
			if ip, _ := im["ip"].(string); ip != "" {
				return ip
			}
		}
	}
	return ""
}

func loadBalancerAddresses(svc *unstructured.Unstructured) []string {
	var out []string
	ingress, _, _ := unstructured.NestedSlice(svc.Object, "status", "loadBalancer", "ingress")
	for _, i := range ingress {
		im, _ := i.(map[string]any)
		for _, k := range []string{"ip", "hostname"} {
			if v, _ := im[k].(string); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

func carriedAnnotations(svc *unstructured.Unstructured) map[string]any {
	out := map[string]any{}
	for k, v := range svc.GetAnnotations() {
		dropped := false
		for _, prefix := range droppedAnnotationPrefixes {
			if strings.HasPrefix(k, prefix) {
				dropped = true
				break
			}
		}
		if !dropped {
			out[k] = v
		}
	}
	return out
}

func nestedMap(values map[string]any, fields ...string) (map[string]any, bool) {
	cur := values
	for _, f := range fields {
		next, ok := cur[f].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func isSubset(sub, of map[string]string) bool {
	for k, v := range sub {
		if of[k] != v {
			return false
		}
	}
	return true
}

func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
