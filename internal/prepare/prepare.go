// Package prepare implements the four one-time preconditions some apps
// require before they can be migrated (config.App.Prepare): suspending the
// legacy datamarket-agent, removing legacy objects that would collide with
// their GitOps-managed replacements, and (the one step whose verification
// still needs a human, even though the tool runs it) a Postgres data
// rewrite for genai.
//
// An automated step never acts blindly: Plan reads live cluster state —
// never a persisted flag — and returns every Operation it would perform,
// each carrying the exact live object it acts on, so apps migrate can show
// them (and --dry-run stop there) before the operator confirms. An empty
// plan means the precondition already holds, so re-running is always safe,
// matching flux-keos's own internal/migrate idempotency model. Applying an
// Operation is pinned to the object's UID: if the object was replaced
// since it was planned, the operation fails instead of touching the new one.
//
// The legacy objects a step acts on are found by the CCT application id
// label CCT puts on everything it deploys for an app (<live name>.<live
// namespace>), never by a hardcoded name, and anything Flux manages is
// skipped — so a step can't remove the GitOps objects replacing them.
//
// A Query step (DBQuery) is the odd one out: a live-data rewrite no
// Kubernetes API can perform or verify. RunQuery finds its target pod by
// label the same way legacyObjects finds an app's legacy objects, execs
// the query, and returns the real captured output — apps migrate always
// asks its own confirmation of that output before proceeding, never
// skipped by --yes, because the Python client this was ported from had an
// automated version of this same check that exited 0 unconditionally,
// marking the step migrated on faith whether or not the SQL was ever run.
package prepare

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
)

// Options carries what a prepare step needs.
type Options struct {
	TenantName string
	// LiveName and LiveNamespace are the migrated app's primary live
	// object's (config.App.LiveName/LiveNamespace): they identify the
	// legacy objects a step acts on.
	LiveName, LiveNamespace string
	Client                  client.Client
	// Execer runs a Query step's command in its target pod; nil unless a
	// step with a Query is actually invoked.
	Execer kubeclient.Execer
	Log    *log.Logger
}

// Step is one precondition an app can require before migration
// (config.App.Prepare names it by Name).
type Step struct {
	Name        string
	Description string
	// Automated is true when Plan's operations perform the precondition;
	// false for a step that only has Instructions for the operator to
	// follow by hand — the gating decision belongs to the caller.
	Automated bool
	// Plan lists, without changing anything, every operation needed for
	// the precondition to hold; empty when it already does. Nil for a
	// step that isn't Automated.
	Plan func(ctx context.Context, opts Options) ([]Operation, error)
	// Instructions tells the operator what to do by hand (non-automated,
	// non-Query steps only); "<tenant>" stands for the tenant name.
	Instructions string
	// Query, when set, is a live-data rewrite no Kubernetes API can
	// verify (prepare-genai): RunQuery finds its target pod and execs it,
	// and apps migrate shows the operator the real captured output before
	// asking its own confirmation, never skipped by --yes. Mutually
	// exclusive with Automated/Plan and Instructions.
	Query *DBQuery
}

// DBQuery is a SQL statement a Query step runs against a live pod — the
// generalized shape any component's own manual-data-rewrite prepare step
// can declare, without writing new pod-discovery or exec plumbing.
type DBQuery struct {
	// Namespace returns the namespace to run in, given the tenant name.
	Namespace func(tenant string) string
	// PodSelector finds the one target pod within that namespace.
	PodSelector map[string]string
	// Container is the pod's container to exec into.
	Container string
	// Command is exec'd with SQL (tenant-substituted) piped to its stdin.
	Command []string
	// SQL is the statement(s) to run; "<tenant>" stands for the tenant name.
	SQL string
}

// InstructionsFor returns Instructions for tenant.
func (s Step) InstructionsFor(tenant string) string {
	return strings.ReplaceAll(s.Instructions, "<tenant>", tenant)
}

// RunQuery resolves Query's target pod in opts and, unless dryRun, execs
// Command with SQL (tenant-substituted) piped to its stdin. It returns the
// resolved pod even under dryRun or on an exec error, so the caller can
// always show what it acted (or would act) on. Zero or more than one
// matching pod is a clear error rather than a guess.
func (s Step) RunQuery(ctx context.Context, opts Options, tenant string, dryRun bool) (pod *corev1.Pod, stdout, stderr string, err error) {
	q := s.Query
	if q == nil {
		return nil, "", "", fmt.Errorf("prepare step %q has no query", s.Name)
	}
	namespace := q.Namespace(tenant)
	var pods corev1.PodList
	if err := opts.Client.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels(q.PodSelector)); err != nil {
		return nil, "", "", fmt.Errorf("finding %s's target pod in %s: %w", s.Name, namespace, err)
	}
	switch len(pods.Items) {
	case 1:
		pod = &pods.Items[0]
	case 0:
		return nil, "", "", fmt.Errorf("prepare step %q: no pod matching %v found in %s", s.Name, q.PodSelector, namespace)
	default:
		return nil, "", "", fmt.Errorf("prepare step %q: %d pods matching %v found in %s, expected exactly one", s.Name, len(pods.Items), q.PodSelector, namespace)
	}
	if dryRun {
		return pod, "", "", nil
	}
	sql := strings.ReplaceAll(q.SQL, "<tenant>", tenant)
	stdout, stderr, err = opts.Execer.Exec(ctx, namespace, pod.Name, q.Container, q.Command, strings.NewReader(sql))
	if err != nil {
		return pod, stdout, stderr, fmt.Errorf("running prepare step %q against pod %s/%s: %w", s.Name, namespace, pod.Name, err)
	}
	return pod, stdout, stderr, nil
}

// Operation is one change a step makes to one live object.
type Operation struct {
	// Action says what is done to Object, e.g. "delete".
	Action string
	// Object is the live object as Plan read it.
	Object *unstructured.Unstructured
	apply  func(ctx context.Context, c client.Client) error
}

// String renders the operation as "<action> <Kind> <namespace>/<name>".
func (o Operation) String() string {
	return fmt.Sprintf("%s %s %s/%s", o.Action, o.Object.GetKind(), o.Object.GetNamespace(), o.Object.GetName())
}

// Apply performs the operation.
func (o Operation) Apply(ctx context.Context, c client.Client) error {
	if err := o.apply(ctx, c); err != nil {
		return fmt.Errorf("%s: %w", o, err)
	}
	return nil
}

// Manifest renders Object as YAML, without the server-side bookkeeping
// (managedFields, status, last-applied-configuration) that says nothing
// about which object it is.
func (o Operation) Manifest() ([]byte, error) {
	obj := o.Object.DeepCopy()
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "kubectl.kubernetes.io/last-applied-configuration")
	unstructured.RemoveNestedField(obj.Object, "status")
	return yaml.Marshal(obj.Object)
}

// Steps are the four ported prepare preconditions, in no particular
// order — an app names the one it needs via config.App.Prepare.
var Steps = []Step{
	stepDatamarketAgent,
	stepDatarest,
	stepDLC,
	stepGenAI,
}

// Find returns the step named name, or nil if none matches.
func Find(name string) *Step {
	for i := range Steps {
		if Steps[i].Name == name {
			return &Steps[i]
		}
	}
	return nil
}

var (
	gvkIngress     = schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"}
	gvkDeployment  = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	gvkHelmRelease = schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}
)

// cctAppIDLabel is the label CCT sets on every object it deploys for an
// app, valued "<name>.<namespace>".
const cctAppIDLabel = "cct.stratio.com/application_id"

// fluxLabels are set on every object a Flux Kustomization or HelmRelease
// applies — the GitOps side, which a prepare step must never touch.
var fluxLabels = []string{"kustomize.toolkit.fluxcd.io/name", "helm.toolkit.fluxcd.io/name"}

func fluxManaged(obj *unstructured.Unstructured) bool {
	for _, l := range fluxLabels {
		if _, ok := obj.GetLabels()[l]; ok {
			return true
		}
	}
	return false
}

// legacyObjects lists the objects of kind gvk CCT deployed for the app
// (by cctAppIDLabel) in its live namespace, leaving out anything Flux
// manages.
func legacyObjects(ctx context.Context, opts Options, gvk schema.GroupVersionKind) ([]*unstructured.Unstructured, error) {
	if opts.LiveName == "" || opts.LiveNamespace == "" {
		return nil, fmt.Errorf("no live object to identify the app's legacy objects by")
	}
	id := opts.LiveName + "." + opts.LiveNamespace
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err := opts.Client.List(ctx, list, client.InNamespace(opts.LiveNamespace), client.MatchingLabels{cctAppIDLabel: id}); err != nil {
		return nil, fmt.Errorf("listing %ss labelled %s=%s in %s: %w", gvk.Kind, cctAppIDLabel, id, opts.LiveNamespace, err)
	}
	var out []*unstructured.Unstructured
	for i := range list.Items {
		obj := &list.Items[i]
		if fluxManaged(obj) {
			continue
		}
		obj.SetGroupVersionKind(gvk)
		out = append(out, obj)
	}
	return out, nil
}

// deleteOp deletes obj — only that very object (UID precondition), in
// the background, so a Deployment's ReplicaSets and pods go with it.
func deleteOp(obj *unstructured.Unstructured) Operation {
	return Operation{Action: "delete", Object: obj, apply: func(ctx context.Context, c client.Client) error {
		uid := obj.GetUID()
		target := &unstructured.Unstructured{}
		target.SetGroupVersionKind(obj.GroupVersionKind())
		target.SetNamespace(obj.GetNamespace())
		target.SetName(obj.GetName())
		return client.IgnoreNotFound(c.Delete(ctx, target,
			client.Preconditions{UID: &uid}, client.PropagationPolicy("Background")))
	}}
}

// patchOp merge-patches spec into obj, pinned to its UID: the API server
// rejects the patch if the object has since been replaced.
func patchOp(action string, obj *unstructured.Unstructured, spec map[string]any) Operation {
	return Operation{Action: action, Object: obj, apply: func(ctx context.Context, c client.Client) error {
		data, err := json.Marshal(map[string]any{
			"metadata": map[string]any{"uid": string(obj.GetUID())},
			"spec":     spec,
		})
		if err != nil {
			return fmt.Errorf("marshaling merge patch: %w", err)
		}
		return c.Patch(ctx, obj.DeepCopy(), client.RawPatch(types.MergePatchType, data))
	}}
}

// waitForNoPods polls until no pod matching selector remains in namespace,
// matching `kubectl wait pod --for=delete`.
func waitForNoPods(ctx context.Context, c client.Client, namespace string, selector map[string]string, timeout time.Duration) error {
	if len(selector) == 0 {
		return fmt.Errorf("no pod selector to wait on")
	}
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		var pods corev1.PodList
		if err := c.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels(selector)); err != nil {
			return false, err
		}
		return len(pods.Items) == 0, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for pods %v in %s to terminate: %w", selector, namespace, err)
	}
	return nil
}
