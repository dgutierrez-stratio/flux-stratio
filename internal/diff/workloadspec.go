package diff

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// SpecSeverity ranks a SpecDiff by how likely it is to hurt the migrated
// workload. It only drives wording: every difference is reported.
type SpecSeverity string

const (
	// SpecRisk is a difference that can break or destabilize the migrated
	// workload: less memory than live had, a limit where live had none, a
	// different image tag, a stopped workload that starts.
	SpecRisk SpecSeverity = "risk"
	// SpecInfo is any other difference (more resources than live, a
	// build-suffix-only image difference).
	SpecInfo SpecSeverity = "info"
)

// SpecDiff is one workload setting that differs between the rendered
// workload and its live counterpart, other than env vars (see ChartDiff).
type SpecDiff struct {
	// Workload is the rendered workload's name.
	Workload string
	// Container is the rendered container's name, or "" for a workload
	// setting (replicas).
	Container string
	// Field is "resources.requests.<name>", "resources.limits.<name>",
	// "image" or "replicas".
	Field string
	// Rendered and Live are the two sides' values as written; "" is unset.
	Rendered, Live string
	Severity       SpecSeverity
	// LiveRef names the live workload ("Deployment <namespace>/<name>") of a
	// SelectorField or LegacyRunsField diff, which may not be the rendered
	// workload's own name.
	LiveRef string
}

// CompareWorkloadSpecs lists what differs between a rendered workload and
// its live counterpart in the settings a chart-mode env comparison never
// sees: each container's resource requests and limits (compared as
// quantities, so 4096M and 4Gi are told apart by value, not spelling), its
// image, and the workload's replicas. Containers are paired by name; when
// each side has exactly one, they pair whatever they are named (legacy
// containers are named after the application, the chart's after its
// controller). A container with no counterpart is skipped: the pairing can't
// say which of the two it should be compared with.
func CompareWorkloadSpecs(rendered, live *unstructured.Unstructured) []SpecDiff {
	if rendered == nil || live == nil {
		return nil
	}
	workload := rendered.GetName()
	var out []SpecDiff
	for _, pair := range pairContainers(containersOf(rendered), containersOf(live)) {
		name, _ := pair.rendered["name"].(string)
		out = append(out, compareResources(workload, name, pair.rendered, pair.live)...)
		if d, ok := compareImage(workload, name, pair.rendered, pair.live); ok {
			out = append(out, d)
		}
	}
	if d, ok := compareReplicas(rendered, live); ok {
		out = append(out, d)
	}
	if d, ok := compareSelector(rendered, live); ok {
		out = append(out, d)
	}
	return out
}

// SelectorField is the Field of a SpecDiff reporting a different
// spec.selector.matchLabels of two workloads with the same kind, namespace and
// name (appdiff keeps it only for those: see LegacyRunsField).
const SelectorField = "selector"

// LegacyRunsField is the Field of a SpecInfo SpecDiff noting that the live
// workload a rendered one is compared with is another object — another name
// or namespace (a legacy "datamarket-agent" against the chart's
// "governance-datamarket-agent") — so the two never meet: Helm installs the
// chart's alongside, the legacy one keeps running, and no immutable selector
// is in the way. Rendered is the chart's "Kind namespace/name", Live the
// legacy workload's replicas.
const LegacyRunsField = "legacy-workload"

// compareSelector compares spec.selector.matchLabels, which a Deployment or
// StatefulSet can never change once created: the legacy CCT workloads select
// on cct.stratio.com/application_id, the chart's on app.kubernetes.io/*, so
// Helm can't update the legacy workload in place — it has to be replaced.
func compareSelector(rendered, live *unstructured.Unstructured) (SpecDiff, bool) {
	r, rOK, _ := stringMapAt(rendered.Object, "spec", "selector", "matchLabels")
	l, lOK, _ := unstructured.NestedStringMap(live.Object, "spec", "selector", "matchLabels")
	if !rOK || !lOK || reflect.DeepEqual(r, l) {
		return SpecDiff{}, false
	}
	return SpecDiff{
		Workload: rendered.GetName(), Field: SelectorField,
		Rendered: formatLabels(r), Live: formatLabels(l), Severity: SpecRisk,
	}, true
}

func formatLabels(labels map[string]string) string {
	pairs := make([]string, 0, len(labels))
	for k, v := range labels {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

type containerPair struct{ rendered, live map[string]any }

func pairContainers(rendered, live []map[string]any) []containerPair {
	if len(rendered) == 1 && len(live) == 1 {
		return []containerPair{{rendered[0], live[0]}}
	}
	byName := map[string]map[string]any{}
	for _, c := range live {
		if name, _ := c["name"].(string); name != "" {
			byName[name] = c
		}
	}
	var pairs []containerPair
	for _, c := range rendered {
		name, _ := c["name"].(string)
		if l, ok := byName[name]; ok && name != "" {
			pairs = append(pairs, containerPair{c, l})
		}
	}
	return pairs
}

// resourceLists are a container's resources sections, in report order.
var resourceLists = []string{"requests", "limits"}

func compareResources(workload, container string, rendered, live map[string]any) []SpecDiff {
	var out []SpecDiff
	for _, list := range resourceLists {
		r := resourceList(rendered, list)
		l := resourceList(live, list)
		names := map[string]bool{}
		for n := range r {
			names[n] = true
		}
		for n := range l {
			names[n] = true
		}
		sorted := make([]string, 0, len(names))
		for n := range names {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		for _, n := range sorted {
			rv, lv := r[n], l[n]
			if sameQuantity(rv, lv) {
				continue
			}
			out = append(out, SpecDiff{
				Workload: workload, Container: container, Field: "resources." + list + "." + n,
				Rendered: rv, Live: lv, Severity: resourceSeverity(list, n, rv, lv),
			})
		}
	}
	return out
}

// resourceList is container's resources.<list> as written, by resource name.
func resourceList(container map[string]any, list string) map[string]string {
	raw, _, _ := unstructured.NestedMap(container, "resources", list)
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s := fmt.Sprint(v); s != "" {
			out[k] = carriedQuantity(s)
		}
	}
	return out
}

// sameQuantity reports whether a and b are the same amount; strings that
// aren't Kubernetes quantities are equal only when identical.
func sameQuantity(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	qa, errA := resource.ParseQuantity(a)
	qb, errB := resource.ParseQuantity(b)
	if errA != nil || errB != nil {
		return false
	}
	return qa.Cmp(qb) == 0
}

// resourceSeverity is SpecRisk for less memory than live had, and for a
// limit where live set none (the workload becomes capped); everything else
// is SpecInfo.
func resourceSeverity(list, name, rendered, live string) SpecSeverity {
	if list == "limits" && live == "" && rendered != "" {
		return SpecRisk
	}
	if name == "memory" && live != "" && lessQuantity(rendered, live) {
		return SpecRisk
	}
	return SpecInfo
}

// lessQuantity reports whether a is a set quantity smaller than b; an
// unset or unparseable side is never less.
func lessQuantity(a, b string) bool {
	qa, errA := resource.ParseQuantity(a)
	qb, errB := resource.ParseQuantity(b)
	return errA == nil && errB == nil && qa.Cmp(qb) < 0
}

func compareImage(workload, container string, rendered, live map[string]any) (SpecDiff, bool) {
	r, _ := rendered["image"].(string)
	l, _ := live["image"].(string)
	if r == l || r == "" || l == "" {
		return SpecDiff{}, false
	}
	return SpecDiff{
		Workload: workload, Container: container, Field: "image", Rendered: r, Live: l,
		Severity: imageSeverity(r, l),
	}, true
}

// imageSeverity is SpecInfo when the images differ only by registry or by a
// build suffix on the tag ("1.2.0" against "1.2.0-6dd7bb1"), SpecRisk when
// the repository or tag is a different release.
func imageSeverity(rendered, live string) SpecSeverity {
	rRepo, rTag := splitImage(rendered)
	lRepo, lTag := splitImage(live)
	if rTag == lTag || buildSuffixOnly(rTag, lTag) {
		if lastPathElement(rRepo) == lastPathElement(lRepo) {
			return SpecInfo
		}
	}
	return SpecRisk
}

// splitImage splits repo:tag at the tag's colon (a registry port's colon
// comes before a slash).
func splitImage(image string) (repo, tag string) {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[:i], image[i+1:]
	}
	return image, ""
}

func lastPathElement(repo string) string {
	return repo[strings.LastIndex(repo, "/")+1:]
}

func buildSuffixOnly(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	return a != "" && strings.HasPrefix(b, a+"-")
}

// compareReplicas compares spec.replicas, an unset value meaning 1 (the
// Kubernetes default). Workloads without replicas (DaemonSet) have none to
// compare.
func compareReplicas(rendered, live *unstructured.Unstructured) (SpecDiff, bool) {
	r, rOK, _ := unstructured.NestedFieldNoCopy(rendered.Object, "spec", "replicas")
	l, lOK, _ := unstructured.NestedFieldNoCopy(live.Object, "spec", "replicas")
	if rendered.GetKind() == "DaemonSet" || (!rOK && !lOK) {
		return SpecDiff{}, false
	}
	rn, ln := int64(1), int64(1)
	if rOK {
		rn = toInt64(r)
	}
	if lOK {
		ln = toInt64(l)
	}
	if rn == ln {
		return SpecDiff{}, false
	}
	sev := SpecInfo
	if ln == 0 && rn > 0 {
		sev = SpecRisk
	}
	return SpecDiff{
		Workload: rendered.GetName(), Field: "replicas",
		Rendered: fmt.Sprint(rn), Live: fmt.Sprint(ln), Severity: sev,
	}, true
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// CompareLiveSpecs runs CompareWorkloadSpecs over every compared workload
// that has both sides. A live workload with no manifest (a backup taken
// before manifests were kept) is skipped, not reported as different.
func CompareLiveSpecs(live []LiveWorkloadEnv) []SpecDiff {
	var out []SpecDiff
	for _, lw := range live {
		out = append(out, CompareWorkloadSpecs(lw.Rendered, lw.Live)...)
	}
	return out
}

// EnvVsLimitField prefixes the Field of a SpecDiff reporting an env var
// whose value outgrows its container's memory limit: the name follows
// the colon.
const EnvVsLimitField = "env-vs-limit:"

var xmxRe = regexp.MustCompile(`-Xmx(\d+)([kKmMgG]?)`)

// CheckEnvAgainstLimits warns about a workload whose env asks for more
// memory than its container may use — the dg-hdfs-agent case, a JVM heap of
// -Xmx3072m in a 2048Mi container, OOMKilled once it fills. Every env var
// of each workload in workloads (as docs, the chart rendered with the patch
// applied, resolves them) is searched for -Xmx<N>[kmg] (a JVM's binary
// units), and the ones named in memoryEnv are read as a number of MB. The
// check needs the container's memory limit and a single container to
// attribute a ConfigMap variable to, so it stays quiet where it can't be
// sure. Each is a SpecRisk.
func CheckEnvAgainstLimits(docs []*unstructured.Unstructured, workloads []string, memoryEnv []string) []SpecDiff {
	index := indexRendered(docs)
	wanted := map[string]bool{}
	for _, w := range workloads {
		wanted[w] = true
	}
	sizeVars := map[string]bool{}
	for _, n := range memoryEnv {
		sizeVars[n] = true
	}
	var out []SpecDiff
	for _, w := range index.workloads {
		if !wanted[w.GetName()] {
			continue
		}
		containers := containersOf(w)
		env := index.workloadEnv(w)
		for _, name := range sortedMapKeys(env) {
			v := env[name]
			want, ok := envMemoryBytes(name, v.Value, sizeVars)
			if !ok {
				continue
			}
			c := containerForVar(containers, v.Container)
			if c == nil {
				continue
			}
			limit := resourceList(c, "limits")["memory"]
			q, err := resource.ParseQuantity(limit)
			if limit == "" || err != nil || want <= q.Value() {
				continue
			}
			cname, _ := c["name"].(string)
			out = append(out, SpecDiff{
				Workload: w.GetName(), Container: cname, Field: EnvVsLimitField + name,
				Rendered: limit, Live: v.Value, Severity: SpecRisk,
			})
		}
	}
	return out
}

// containerForVar is the container an env var belongs to: the named one
// for an inline variable, the only one otherwise.
func containerForVar(containers []map[string]any, name string) map[string]any {
	if name != "" {
		for _, c := range containers {
			if n, _ := c["name"].(string); n == name {
				return c
			}
		}
		return nil
	}
	if len(containers) == 1 {
		return containers[0]
	}
	return nil
}

// envMemoryBytes is the memory an env var's value asks for: the largest
// -Xmx in it, or all of it, as MB, for a variable named in sizeVars.
func envMemoryBytes(name, value string, sizeVars map[string]bool) (int64, bool) {
	var best int64
	for _, m := range xmxRe.FindAllStringSubmatch(value, -1) {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(m[2]) { // a bare -Xmx<N> is bytes
		case "k":
			n <<= 10
		case "m":
			n <<= 20
		case "g":
			n <<= 30
		}
		if n > best {
			best = n
		}
	}
	if sizeVars[name] {
		if mb, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			if n := int64(mb * 1e6); n > best {
				best = n
			}
		}
	}
	return best, best > 0
}
