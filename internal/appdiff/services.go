package appdiff

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

// cctAppIDLabel is the label CCT selects an app's pods by, valued
// "<app>.<namespace>".
const cctAppIDLabel = "cct.stratio.com/application_id"

// Affixes of a backup's Service manifest names (ServiceManifestFile).
const (
	serviceManifestPrefix = "service."
	serviceManifestSuffix = ".yaml"
)

// ServiceManifestFile is the backup file name holding a Service the app's
// workloads are reached through: service.<name>.yaml.
func ServiceManifestFile(name string) string {
	return serviceManifestPrefix + name + serviceManifestSuffix
}

// legacyServices are the Services, live or (with Baseline) in the backup, in
// the release's and the app's live namespaces, that reach the app's legacy
// pods and no pod the chart renders (see findLegacyServices). A Service
// list the cluster refuses is warned about, not an error: the comparison
// itself doesn't depend on it.
func legacyServices(ctx context.Context, opts Options, hrNamespace string, renderedDocs []*unstructured.Unstructured, live []diff.LiveWorkloadEnv) ([]diff.LegacyService, error) {
	var services []*unstructured.Unstructured
	if opts.Baseline != "" {
		var err error
		if services, err = readBaselineServices(opts.Baseline); err != nil {
			return nil, err
		}
	} else if opts.Client != nil {
		for _, ns := range uniqueNonEmpty(hrNamespace, opts.App.LiveNamespace()) {
			list, err := listServices(ctx, opts.Client, ns)
			if err != nil {
				opts.Log.Warningf("listing the Services of %s to find the legacy ones (%v): Services are not compared", ns, err)
				return nil, nil
			}
			services = append(services, list...)
		}
	}
	return findLegacyServices(services, workloadTargets(opts, hrNamespace, renderedDocs), live, renderedDocs, hrNamespace, opts.App.LiveNamespace()), nil
}

// findLegacyServices keeps the Services that the cutover leaves selecting
// nothing: not Flux- or Helm-managed, not owned, with a selector that no
// rendered pod matches, under a name the chart doesn't render (Helm would
// take that one over with the chart's selector), and that select a
// target's legacy pods: the pod labels of its live workload while that one
// is still the legacy one, or, once it was replaced, CCT's own selector for
// it (cct.stratio.com/application_id=<name>.<namespace>). A target's legacy
// workload is looked for where the diff found it live, and also in the app's
// live namespace (liveNamespace): a release can live elsewhere than the
// legacy app it replaces (governance-datamarket-agent in stratio-apps,
// datamarket-agent in stratio-datastores).
func findLegacyServices(services []*unstructured.Unstructured, targets []workloadTarget, live []diff.LiveWorkloadEnv, renderedDocs []*unstructured.Unstructured, hrNamespace, liveNamespace string) []diff.LegacyService {
	type podLabels struct {
		namespace string
		labels    map[string]string
	}
	var renderedPods []podLabels
	for _, kind := range workloadKinds {
		for _, doc := range yamldocs.FindByKind(renderedDocs, kind) {
			renderedPods = append(renderedPods, podLabels{orDefault(doc.GetNamespace(), hrNamespace), templateLabels(doc)})
		}
	}
	renderedServices := map[string]bool{}
	for _, doc := range yamldocs.FindByKind(renderedDocs, "Service") {
		renderedServices[orDefault(doc.GetNamespace(), hrNamespace)+"/"+doc.GetName()] = true
	}
	// Per target: the namespaces its legacy workload may be in, and the pod
	// labels of the live one while it is still the legacy one.
	namespaces := make([][]string, len(targets))
	legacyLabels := make([]map[string]string, len(targets))
	legacyNamespace := make([]string, len(targets))
	for i, t := range targets {
		var liveNS string
		for _, lw := range live {
			if lw.Rendered != t.rendered || lw.Live == nil {
				continue
			}
			liveNS = lw.Live.GetNamespace()
			if !managed(lw.Live) {
				legacyLabels[i], legacyNamespace[i] = templateLabels(lw.Live), liveNS
			}
		}
		namespaces[i] = uniqueNonEmpty(t.namespace, liveNS, liveNamespace)
	}

	var out []diff.LegacyService
	for _, svc := range services {
		selector, _, _ := unstructured.NestedStringMap(svc.Object, "spec", "selector")
		if len(selector) == 0 || managed(svc) || len(svc.GetOwnerReferences()) > 0 ||
			renderedServices[svc.GetNamespace()+"/"+svc.GetName()] {
			continue
		}
		reachesRendered := false
		for _, p := range renderedPods {
			if p.namespace == svc.GetNamespace() && subset(selector, p.labels) {
				reachesRendered = true
				break
			}
		}
		if reachesRendered {
			continue
		}
		for i, t := range targets {
			ns := svc.GetNamespace()
			if !slices.Contains(namespaces[i], ns) {
				continue
			}
			cctOnly := len(selector) == 1 && selector[cctAppIDLabel] == t.name+"."+ns
			if cctOnly || (legacyNamespace[i] == ns && subset(selector, legacyLabels[i])) {
				out = append(out, diff.LegacyService{Service: svc, Workload: t.rendered})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Service, out[j].Service
		return a.GetNamespace()+"/"+a.GetName() < b.GetNamespace()+"/"+b.GetName()
	})
	return out
}

// ServicesSelecting lists the Services in the workloads' namespaces whose
// selector picks one of their pods: what clients reach them through, for a
// backup to keep beside them.
func ServicesSelecting(ctx context.Context, c client.Client, workloads []*unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	var namespaces []string
	for _, w := range workloads {
		namespaces = append(namespaces, w.GetNamespace())
	}
	var out []*unstructured.Unstructured
	for _, ns := range uniqueNonEmpty(namespaces...) {
		services, err := listServices(ctx, c, ns)
		if err != nil {
			return nil, err
		}
		for _, svc := range services {
			selector, _, _ := unstructured.NestedStringMap(svc.Object, "spec", "selector")
			if len(selector) == 0 {
				continue
			}
			for _, w := range workloads {
				if w.GetNamespace() == ns && subset(selector, templateLabels(w)) {
					out = append(out, svc)
					break
				}
			}
		}
	}
	return out, nil
}

func listServices(ctx context.Context, c client.Client, namespace string) ([]*unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion("v1")
	list.SetKind("ServiceList")
	if err := c.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	out := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		svc := &list.Items[i]
		svc.SetAPIVersion("v1")
		svc.SetKind("Service")
		out = append(out, svc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out, nil
}

// readBaselineServices reads every ServiceManifestFile of a backup
// directory; a backup taken before Services were kept has none.
func readBaselineServices(dir string) ([]*unstructured.Unstructured, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading baseline %s: %w", dir, err)
	}
	var out []*unstructured.Unstructured
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, serviceManifestPrefix) || !strings.HasSuffix(name, serviceManifestSuffix) {
			continue
		}
		obj, err := readBaselineYAML(dir, name)
		if err != nil {
			return nil, err
		}
		if obj.GetKind() == "Service" {
			out = append(out, obj)
		}
	}
	return out, nil
}

// managed reports whether Flux or Helm owns obj: Flux's labels, or Helm's
// release annotation (what an object Helm took over carries).
func managed(obj *unstructured.Unstructured) bool {
	return fluxManagedBy(obj) != "" || obj.GetAnnotations()["meta.helm.sh/release-name"] != ""
}

func templateLabels(w *unstructured.Unstructured) map[string]string {
	labels, _, _ := unstructured.NestedStringMap(w.Object, "spec", "template", "metadata", "labels")
	return labels
}

func subset(sub, of map[string]string) bool {
	for k, v := range sub {
		if of[k] != v {
			return false
		}
	}
	return true
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func uniqueNonEmpty(values ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
