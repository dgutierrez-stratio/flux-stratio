// Package prepare implements the one-time preconditions some apps
// require before they can be migrated (config.App.Prepare): suspending the
// legacy datamarket-agent, removing legacy objects that would collide with
// their GitOps-managed replacements (a name, an ingress host, an immutable
// selector), and (the one step whose verification
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
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
	// IsLegacy reports whether a live object is one of the app's legacy
	// workloads — the app's own and its chart siblings' (config.App.Family),
	// by the same selectors classification uses. Only a step that replaces
	// a whole family of workloads needs it (prepare-virtualizer); nil
	// otherwise.
	IsLegacy func(*unstructured.Unstructured) bool
	Client   client.Client
	// Execer runs a command in a pod: a Query step's statement, or the
	// vault CLI for a step that NeedsVault. A step that needs it fails
	// clearly when it is nil.
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
	// ReplacesWorkloads is true when the step deletes the legacy workloads
	// the chart then recreates under the same names, so the chart changing
	// their (immutable) selector is no obstacle.
	ReplacesWorkloads bool
	// PasswordMoves are the legacy Vault passwords the step moves to the
	// chart's names (planPasswordMoves, through package vault with
	// Options.Execer); doctor reads the same Vault paths for every legacy
	// instance of a type whose step has any (NeedsVault).
	PasswordMoves []PasswordMove
	// Plan lists, without changing anything, every operation needed for
	// the precondition to hold; empty when it already does. Set for an
	// Automated step, and for a Query step that also deletes objects once
	// its query has run (prepare-genai's legacy Deployments); nil otherwise.
	Plan func(ctx context.Context, opts Options) ([]Operation, error)
	// Instructions tells the operator what to do by hand (non-automated,
	// non-Query steps only); "<tenant>" stands for the tenant name.
	Instructions string
	// Query, when set, is a live-data rewrite no Kubernetes API can
	// verify (prepare-genai): RunQuery finds its target pod and execs it,
	// and apps migrate shows the operator the real captured output before
	// asking its own confirmation, never skipped by --yes. Mutually
	// exclusive with Automated and Instructions; it may carry a Plan.
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
	// ReadCommand is exec'd, with a read-only statement piped to its stdin,
	// to count and copy the Touches rows: it prints bare values (psql -tA).
	ReadCommand []string
	// Touches are the tables SQL changes rows of. Pending counts the rows
	// still to change (none: the rewrite already happened, so there is
	// nothing to run), and DumpRows copies them before SQL runs.
	Touches []TouchedTable
}

// TouchedTable is a table (and the rows of it) a DBQuery's SQL changes.
type TouchedTable struct {
	// Table is the qualified, quoted table name ("<tenant>" stands for the
	// tenant name); Where selects the rows SQL changes (SQL's own WHERE).
	Table, Where string
	// Optional is true for a table that may not exist (SQL guards for it
	// too): it then counts for nothing and has nothing to copy.
	Optional bool
	// File names the copy DumpRows makes ("<tenant>" stands for the tenant).
	File string
}

// DataBackup is one table's rows as DumpRows copied them: Content is a psql
// script that loads them back.
type DataBackup struct {
	File    string
	Rows    int
	Content string
}

// NeedsVault reports whether the step reads and writes Vault.
func (s Step) NeedsVault() bool { return len(s.PasswordMoves) > 0 }

// InstructionsFor returns Instructions for tenant.
func (s Step) InstructionsFor(tenant string) string {
	return strings.ReplaceAll(s.Instructions, "<tenant>", tenant)
}

// targetPod resolves Query's target pod in opts. Zero or more than one
// matching pod is a clear error rather than a guess.
func (s Step) targetPod(ctx context.Context, opts Options, tenant string) (*corev1.Pod, error) {
	q := s.Query
	if q == nil {
		return nil, fmt.Errorf("prepare step %q has no query", s.Name)
	}
	namespace := q.Namespace(tenant)
	var pods corev1.PodList
	if err := opts.Client.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels(q.PodSelector)); err != nil {
		return nil, fmt.Errorf("finding %s's target pod in %s: %w", s.Name, namespace, err)
	}
	switch len(pods.Items) {
	case 1:
		return &pods.Items[0], nil
	case 0:
		return nil, fmt.Errorf("prepare step %q: no pod matching %v found in %s", s.Name, q.PodSelector, namespace)
	default:
		return nil, fmt.Errorf("prepare step %q: %d pods matching %v found in %s, expected exactly one", s.Name, len(pods.Items), q.PodSelector, namespace)
	}
}

// RunQuery resolves Query's target pod in opts and, unless dryRun, execs
// Command with SQL (tenant-substituted) piped to its stdin. It returns the
// resolved pod even under dryRun or on an exec error, so the caller can
// always show what it acted (or would act) on.
func (s Step) RunQuery(ctx context.Context, opts Options, tenant string, dryRun bool) (pod *corev1.Pod, stdout, stderr string, err error) {
	pod, err = s.targetPod(ctx, opts, tenant)
	if err != nil {
		return nil, "", "", err
	}
	if dryRun {
		return pod, "", "", nil
	}
	sql := strings.ReplaceAll(s.Query.SQL, "<tenant>", tenant)
	stdout, stderr, err = opts.Execer.Exec(ctx, pod.Namespace, pod.Name, s.Query.Container, s.Query.Command, strings.NewReader(sql))
	if err != nil {
		return pod, stdout, stderr, fmt.Errorf("running prepare step %q against pod %s/%s: %w", s.Name, pod.Namespace, pod.Name, err)
	}
	return pod, stdout, stderr, nil
}

// read execs a read-only statement through ReadCommand.
func (s Step) read(ctx context.Context, opts Options, tenant string, pod *corev1.Pod, sql string) (string, error) {
	if len(s.Query.ReadCommand) == 0 {
		return "", fmt.Errorf("prepare step %q has no read command", s.Name)
	}
	if opts.Execer == nil {
		return "", fmt.Errorf("prepare step %q reads the database through a pod exec, and no exec is available", s.Name)
	}
	stdout, stderr, err := opts.Execer.Exec(ctx, pod.Namespace, pod.Name, s.Query.Container, s.Query.ReadCommand,
		strings.NewReader(strings.ReplaceAll(sql, "<tenant>", tenant)))
	if err != nil {
		return "", fmt.Errorf("reading from pod %s/%s for prepare step %q (stderr: %s): %w", pod.Namespace, pod.Name, s.Name, strings.TrimSpace(stderr), err)
	}
	return stdout, nil
}

// rowsOf counts the rows t's Where selects, and whether t exists at all
// (a table not Optional is taken to exist: reading it fails if it doesn't).
func (s Step) rowsOf(ctx context.Context, opts Options, tenant string, pod *corev1.Pod, t TouchedTable) (int, error) {
	if t.Optional {
		out, err := s.read(ctx, opts, tenant, pod, fmt.Sprintf("SELECT to_regclass('%s') IS NOT NULL;", t.Table))
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(out) != "t" {
			return 0, nil
		}
	}
	out, err := s.read(ctx, opts, tenant, pod, fmt.Sprintf("SELECT count(*) FROM %s WHERE (%s);", t.Table, t.Where))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("prepare step %q: counting rows of %s: unexpected output %q", s.Name, t.Table, strings.TrimSpace(out))
	}
	return n, nil
}

// Pending is how many rows Query's SQL would still change, counted with
// read-only SELECTs (also fine under --dry-run): zero means the rewrite
// already happened.
func (s Step) Pending(ctx context.Context, opts Options, tenant string, pod *corev1.Pod) (int, error) {
	total := 0
	for _, t := range s.Query.Touches {
		n, err := s.rowsOf(ctx, opts, tenant, pod, t)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// DumpRows copies, as a psql script that loads them back, the rows SQL
// is about to change: the workload backup holds none of them, and an
// UPDATE or DELETE has no undo. A table with no row to change has no copy.
func (s Step) DumpRows(ctx context.Context, opts Options, tenant string, pod *corev1.Pod) ([]DataBackup, error) {
	var out []DataBackup
	for _, t := range s.Query.Touches {
		n, err := s.rowsOf(ctx, opts, tenant, pod, t)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		copied, err := s.read(ctx, opts, tenant, pod, fmt.Sprintf("COPY (SELECT * FROM %s WHERE (%s)) TO STDOUT;", t.Table, t.Where))
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(copied) == "" {
			return nil, fmt.Errorf("prepare step %q: copying the %d row(s) of %s returned nothing", s.Name, n, t.Table)
		}
		table := strings.ReplaceAll(t.Table, "<tenant>", tenant)
		content := fmt.Sprintf("-- prepare step %q: the %d row(s) of %s it rewrites (WHERE %s), as they were.\n"+
			"-- To put them back: delete the rewritten rows (same primary key), then run this file with psql against the same database.\n"+
			"COPY %s FROM stdin;\n%s\\.\n", s.Name, n, table, strings.ReplaceAll(t.Where, "\n", " "), table, ensureNewline(copied))
		out = append(out, DataBackup{File: strings.ReplaceAll(t.File, "<tenant>", tenant), Rows: n, Content: content})
	}
	return out, nil
}

func ensureNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
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

// Steps are the prepare preconditions, in no particular order — an app
// names the one it needs via config.App.Prepare.
var Steps = []Step{
	stepDatamarketAgent,
	stepDatarest,
	stepDLC,
	stepGenAI,
	stepVirtualizer,
	stepDgAgent,
	stepDiscovery,
	stepEurekaAgent,
	stepIntelligence,
	stepRocket,
	stepLitellm,
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

// planDeleteLegacy plans the deletion of every legacy object of the given
// kinds CCT deployed for the app (by its CCT app id), in the order given.
func planDeleteLegacy(ctx context.Context, opts Options, gvks ...schema.GroupVersionKind) ([]Operation, error) {
	var ops []Operation
	for _, gvk := range gvks {
		objs, err := legacyObjects(ctx, opts, gvk)
		if err != nil {
			return nil, err
		}
		for _, obj := range objs {
			ops = append(ops, deleteOp(obj))
		}
	}
	return ops, nil
}

// ownedBy reports whether one of obj's ownerReferences names uid.
func ownedBy(obj *unstructured.Unstructured, uid types.UID) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == uid {
			return true
		}
	}
	return false
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
