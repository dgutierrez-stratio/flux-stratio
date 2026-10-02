// Package doctor implements `flux stratio doctor`: a single preflight pass
// over everything a migration command depends on — required binaries, the
// component catalog and environment files, the GitOps repo layout, each
// chart-mode type's on-disk chart directory, each type's component key
// and prepare step, every Kustomization path its template renders, cluster
// access, and the target tenant file —
// so a broken prerequisite is caught up front, not discovered
// mid-migration.
package doctor

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/prepare"
	"github.com/Stratio/flux-stratio/internal/reporequire"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

// Options configures a doctor run. NewClient and Runner default to the
// real cluster client and real subprocess Runner in production; tests
// inject fakes.
type Options struct {
	// ConfigFlag and EnvConfigFlag are the --config (catalog) and
	// --env-config (environment) flag values.
	ConfigFlag, EnvConfigFlag string
	// Overrides are the root command's --base/--cluster/--tenant flags,
	// applied on top of the environment file.
	Overrides      config.Environment
	KubeconfigArgs *genericclioptions.ConfigFlags
	Runner         runner.Runner
	// NewClient builds a cluster client from KubeconfigArgs. Defaults to
	// kubeclient.New; overridden in tests to avoid a real kubeconfig.
	NewClient func(*genericclioptions.ConfigFlags) (client.Client, error)
	Log       *log.Logger
	// Git runs `git -C dir args...` and returns its stdout. Defaults to the
	// real git through Runner; tests inject a fake.
	Git func(ctx context.Context, dir string, args ...string) (string, error)
}

// CheckName identifies one of doctor's checks.
type CheckName string

// The checks doctor runs, in order.
const (
	CheckBinaries    CheckName = "binaries"
	CheckCatalog     CheckName = "catalog"
	CheckEnvironment CheckName = "environment"
	CheckRepoLayout  CheckName = "repo layout"
	CheckChartPaths  CheckName = "chart paths"
	CheckExcludes    CheckName = "exclude paths"
	CheckTypes       CheckName = "catalog types"
	CheckSourcePaths CheckName = "kustomization paths"
	CheckCluster     CheckName = "cluster access"
	CheckRevisions   CheckName = "repo revisions"
	CheckTenant      CheckName = "tenant file"
	// CheckMeld is Optional: apps diff --meld is the only thing it gates.
	CheckMeld CheckName = "meld (optional)"
)

// Check is one doctor check's outcome.
type Check struct {
	Name   CheckName
	OK     bool
	Detail string
	// Optional checks are reported but never fail the overall Report —
	// meld's presence, for instance, only gates apps diff --meld, not
	// every other command.
	Optional bool
}

// Report is the outcome of a full doctor run: one Check per stage that ran.
// A check after a hard prerequisite failure (an unloadable environment
// makes the repo-layout, cluster and tenant checks meaningless; an
// unloadable catalog, the chart-path and type checks) is skipped rather
// than reported as a confusing false failure.
type Report struct {
	Checks []Check
}

// OK reports whether every non-optional check that ran passed.
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if !c.OK && !c.Optional {
			return false
		}
	}
	return true
}

// Err returns one aggregated, actionable error naming every failing
// non-optional check, or nil if OK().
func (r Report) Err() error {
	if r.OK() {
		return nil
	}
	var lines []string
	for _, c := range r.Checks {
		if !c.OK && !c.Optional {
			lines = append(lines, fmt.Sprintf("  - %s: %s", c.Name, c.Detail))
		}
	}
	return fmt.Errorf("doctor found problems:\n%s", strings.Join(lines, "\n"))
}

// Run performs every doctor check in turn, narrating progress through
// opts.Log (stderr; see the project's console-output rule) and returning a
// Report a caller can inspect or turn into an exit code via Err().
func Run(ctx context.Context, opts Options) Report {
	var report Report

	binCheck := checkBinaries(ctx, opts)
	report.Checks = append(report.Checks, binCheck)
	report.Checks = append(report.Checks, checkMeld(ctx, opts))

	cat, catCheck := checkCatalog(opts)
	report.Checks = append(report.Checks, catCheck)
	env, envCheck := checkEnvironment(opts)
	report.Checks = append(report.Checks, envCheck)
	if env == nil {
		// Nothing below this point can be meaningfully checked without
		// base/cluster/tenant to check against.
		return report
	}

	repos := env.RepoPaths()
	report.Checks = append(report.Checks, narrate(opts.Log, checkRepoLayout(repos)))
	if cat != nil {
		report.Checks = append(report.Checks, narrate(opts.Log, checkChartPaths(cat, repos.Charts)))
		report.Checks = append(report.Checks, narrateOptional(opts.Log, checkExcludePaths(cat, repos.Charts)))
		templates, err := catalog.Load(repos.UseCases)
		report.Checks = append(report.Checks, narrate(opts.Log, checkTypes(cat, templates, err)))
		if templates != nil {
			report.Checks = append(report.Checks, narrate(opts.Log, checkSourcePaths(cat, templates, repos)))
		}
	}
	clusterCheck := checkCluster(ctx, opts)
	report.Checks = append(report.Checks, clusterCheck)
	if clusterCheck.OK {
		report.Checks = append(report.Checks, narrateOptional(opts.Log, checkRepoRevisions(ctx, opts, repos)))
	}
	report.Checks = append(report.Checks, narrate(opts.Log, checkTenantFile(repos.Fleet, env.Cluster, env.Tenant)))

	return report
}

// narrate logs a check that doesn't narrate itself — its outcome and the
// detail it resolved (the repo base, charts root, tenant file path, ...),
// so a doctor run shows every location it checked, not only failures —
// and returns it unchanged.
func narrate(l *log.Logger, c Check) Check {
	if c.OK {
		l.Successf("%s: %s", c.Name, c.Detail)
	} else {
		l.Failuref("%s: %s", c.Name, c.Detail)
	}
	return c
}

// narrateOptional is narrate for an Optional check, which warns rather than
// fails.
func narrateOptional(l *log.Logger, c Check) Check {
	if c.OK {
		l.Successf("%s: %s", c.Name, c.Detail)
	} else {
		l.Warningf("%s: %s", c.Name, c.Detail)
	}
	return c
}

func checkBinaries(ctx context.Context, opts Options) Check {
	opts.Log.Actionf("checking required binaries (flux-operator, flux, helm, kubectl)")
	preflight := runner.Preflight(ctx, opts.Runner)
	if err := preflight.Err(); err != nil {
		opts.Log.Failuref("%v", err)
		return Check{Name: CheckBinaries, OK: false, Detail: err.Error()}
	}
	opts.Log.Successf("all required binaries found")
	return Check{Name: CheckBinaries, OK: true}
}

// checkMeld reports whether meld (https://meldmerge.org/) is on PATH —
// optional, since it only gates apps diff --meld, never any other command.
func checkMeld(ctx context.Context, opts Options) Check {
	opts.Log.Actionf("checking optional binaries (meld)")
	if _, _, err := opts.Runner.Run(ctx, "meld", "--version"); err != nil {
		opts.Log.Warningf("meld not found; apps diff --meld will be unavailable")
		return Check{Name: CheckMeld, OK: false, Optional: true, Detail: "not found on PATH; install it from https://meldmerge.org/ to use apps diff --meld"}
	}
	opts.Log.Successf("meld found")
	return Check{Name: CheckMeld, OK: true, Optional: true}
}

func checkCatalog(opts Options) (*config.Catalog, Check) {
	opts.Log.Actionf("loading component catalog")
	cat, err := config.Load(opts.ConfigFlag)
	if err != nil {
		opts.Log.Failuref("%v", err)
		return nil, Check{Name: CheckCatalog, OK: false, Detail: err.Error()}
	}
	opts.Log.Successf("catalog loaded (%d type(s))", len(cat.Types))
	return cat, Check{Name: CheckCatalog, OK: true, Detail: fmt.Sprintf("%d type(s)", len(cat.Types))}
}

func checkEnvironment(opts Options) (*config.Environment, Check) {
	opts.Log.Actionf("loading environment")
	env, err := config.LoadEnvironment(opts.EnvConfigFlag, opts.Overrides)
	if err != nil {
		opts.Log.Failuref("%v", err)
		return nil, Check{Name: CheckEnvironment, OK: false, Detail: err.Error()}
	}
	detail := fmt.Sprintf("cluster %s, tenant %s", env.Cluster, env.Tenant)
	opts.Log.Successf("environment loaded (%s)", detail)
	return &env, Check{Name: CheckEnvironment, OK: true, Detail: detail}
}

func checkRepoLayout(repos config.RepoPaths) Check {
	if err := reporequire.Validate(repos); err != nil {
		return Check{Name: CheckRepoLayout, OK: false, Detail: err.Error()}
	}
	return Check{Name: CheckRepoLayout, OK: true, Detail: strings.Join([]string{repos.Apps, repos.UseCases, repos.Fleet, repos.SystemServices}, ", ")}
}

// checkChartPaths validates that every chart-mode type's on-disk chart
// directory (Chart.Path, resolved against the charts repository checkout —
// the same resolution internal/appdiff.chartPath and internal/backup use)
// actually exists, so a misconfigured or missing chart checkout is caught
// here instead of surfacing mid-run as a "could not find <path>" error
// from the first chart-mode apps diff/backup/migrate.
func checkChartPaths(cat *config.Catalog, chartsRoot string) Check {
	var missing []string
	for _, t := range cat.Types {
		if t.ChartPath() == "" {
			continue
		}
		path := filepath.Join(chartsRoot, t.ChartPath())
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			missing = append(missing, fmt.Sprintf("%s (%s)", t.Type, path))
		}
	}
	if len(missing) > 0 {
		return Check{Name: CheckChartPaths, OK: false, Detail: fmt.Sprintf("chart directory not found for: %s", strings.Join(missing, ", "))}
	}
	return Check{Name: CheckChartPaths, OK: true, Detail: chartsRoot}
}

// checkExcludePaths validates that every spec.values.* path a chart-mode
// type excludes exists in its chart's values.yaml. An exclude naming a path
// the chart doesn't define silently excludes nothing, so the legacy value it
// meant to keep out of the patch (rocket's governance URLs, once, written
// under rocketCommon when the chart keeps them under rocketServer) is
// back-ported into it. Optional: a path under a free-form map the chart
// leaves empty can't be verified and isn't reported, but a typo is only a
// warning, never a reason to stop a migration. Other excludes (a manifest
// type's CRD spec) have no values.yaml to check against.
func checkExcludePaths(cat *config.Catalog, chartsRoot string) Check {
	const prefix = "spec.values."
	var bad []string
	checked := 0
	for _, t := range cat.Types {
		if t.ChartPath() == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(chartsRoot, t.ChartPath(), "values.yaml"))
		if err != nil {
			continue // checkChartPaths already reports a missing chart
		}
		var values map[string]any
		if err := yaml.Unmarshal(data, &values); err != nil {
			bad = append(bad, fmt.Sprintf("%s: values.yaml doesn't parse: %v", t.Type, err))
			continue
		}
		for _, p := range t.Exclude {
			if !strings.HasPrefix(p, prefix) {
				continue
			}
			checked++
			if !valuesPathExists(values, strings.Split(strings.TrimPrefix(p, prefix), ".")) {
				bad = append(bad, fmt.Sprintf("%s: %s", t.Type, p))
			}
		}
	}
	if len(bad) > 0 {
		return Check{Name: CheckExcludes, OK: false, Optional: true,
			Detail: "exclude path(s) not defined in the chart's values.yaml, so they exclude nothing: " + strings.Join(bad, "; ")}
	}
	return Check{Name: CheckExcludes, OK: true, Optional: true, Detail: fmt.Sprintf("%d path(s)", checked)}
}

// valuesPathExists reports whether path names a key in values. It stops
// believing it can tell at an empty or null value, which a chart uses for a
// free-form map the path may legitimately reach into.
func valuesPathExists(values map[string]any, path []string) bool {
	var cur any = values
	for _, seg := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return cur == nil
		}
		if len(m) == 0 {
			return true
		}
		next, ok := m[seg]
		if !ok {
			return false
		}
		cur = next
	}
	return true
}

// checkTypes validates each catalog type against what it refers to
// outside the catalog file itself: its component key must be one
// keos-use-cases's templates declare (else no tenant entry could ever
// render it), and its prepare step, if any, must be one internal/prepare
// knows — both otherwise only discovered mid-migration.
func checkTypes(cat *config.Catalog, templates *catalog.Catalog, loadErr error) Check {
	if loadErr != nil {
		return Check{Name: CheckTypes, OK: false, Detail: loadErr.Error()}
	}
	var problems []string
	for _, t := range cat.Types {
		if _, ok := templates.Schemas[t.Component]; !ok {
			problems = append(problems, fmt.Sprintf("%s: component %q isn't declared by any keos-use-cases template", t.Type, t.Component))
		}
		if t.Prepare != "" && prepare.Find(t.Prepare) == nil {
			problems = append(problems, fmt.Sprintf("%s: unknown prepare step %q", t.Type, t.Prepare))
		}
	}
	if len(problems) > 0 {
		return Check{Name: CheckTypes, OK: false, Detail: strings.Join(problems, "; ")}
	}
	return Check{Name: CheckTypes, OK: true, Detail: fmt.Sprintf("%d type(s)", len(cat.Types))}
}

// templateExprRe matches one << >> template expression in a Kustomization
// path (a size, a storage type), which checkSourcePaths reads as "any
// directory".
var templateExprRe = regexp.MustCompile(`<<.*?>>`)

// checkSourcePaths validates that every Kustomization path a catalog
// type's component template renders exists in the repository its
// sourceRef names, with each << >> expression matching any directory. A
// path that matches nothing means keos-use-cases and keos-apps are out of
// sync (a component directory renamed in one checkout but not the
// other), which apps diff/migrate would otherwise only report at render
// time — after migrate's prepare step already scaled the legacy app down.
// Paths into any other repository aren't checked.
func checkSourcePaths(cat *config.Catalog, templates *catalog.Catalog, repos config.RepoPaths) Check {
	roots := map[string]string{config.RepoApps: repos.Apps, config.RepoUseCases: repos.UseCases}
	var missing []string
	seen := map[catalog.SourcePath]bool{}
	for _, t := range cat.Types {
		for _, sp := range templates.Schemas[t.Component].SourcePaths {
			root, ok := roots[sp.Source]
			if !ok || seen[sp] {
				continue
			}
			seen[sp] = true
			if !anyDir(filepath.Join(root, templateExprRe.ReplaceAllString(sp.Path, "*"))) {
				missing = append(missing, fmt.Sprintf("%s: %s not found in %s", t.Component, sp.Path, sp.Source))
			}
		}
	}
	if len(missing) > 0 {
		return Check{Name: CheckSourcePaths, OK: false, Detail: strings.Join(missing, "; ") + " (are keos-use-cases and keos-apps both up to date?)"}
	}
	return Check{Name: CheckSourcePaths, OK: true, Detail: fmt.Sprintf("%d path(s)", len(seen))}
}

// anyDir reports whether pattern matches at least one directory.
func anyDir(pattern string) bool {
	matches, _ := filepath.Glob(pattern)
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func checkCluster(ctx context.Context, opts Options) Check {
	opts.Log.Actionf("checking cluster access")
	newClient := opts.NewClient
	c, err := newClient(opts.KubeconfigArgs)
	if err == nil {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "NamespaceList"})
		err = c.List(ctx, list, client.Limit(1))
	}
	if err != nil {
		opts.Log.Failuref("%v", err)
		return Check{Name: CheckCluster, OK: false, Detail: err.Error()}
	}
	opts.Log.Successf("cluster reachable")
	return Check{Name: CheckCluster, OK: true}
}

func checkTenantFile(fleet, cluster, tenant string) Check {
	path := tenantfile.Path(fleet, cluster, tenant)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return Check{Name: CheckTenant, OK: false, Detail: fmt.Sprintf("%s not found (run `flux stratio tenant import` first)", path)}
	}
	return Check{Name: CheckTenant, OK: true, Detail: path}
}

var gitRepositoryGVK = schema.GroupVersionKind{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepository"}

// checkRepoRevisions compares each repository checkout with the revision
// the cluster's GitRepository of that repository runs. apps diff/migrate
// render from the checkouts, so one on another branch or commit than the
// cluster's renders templates the cluster doesn't run: a Kustomization
// named differently, a value that's moved — patches computed against that
// are patches for a different desired state. Optional: a checkout
// legitimately ahead of the cluster (the change about to be deployed) is
// only worth a look, never a stop. A repository is matched to its
// GitRepository by the URL's last path element, so the cluster's names
// (keos-fleet is "flux-system") don't matter.
func checkRepoRevisions(ctx context.Context, opts Options, repos config.RepoPaths) Check {
	skip := func(why string) Check {
		return Check{Name: CheckRevisions, OK: true, Optional: true, Detail: "skipped: " + why}
	}
	c, err := opts.NewClient(opts.KubeconfigArgs)
	if err != nil {
		return skip(err.Error())
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gitRepositoryGVK)
	if err := c.List(ctx, list); err != nil {
		return skip("listing the cluster's GitRepositories: " + err.Error())
	}

	dirs := map[string]string{
		config.RepoApps: repos.Apps, config.RepoUseCases: repos.UseCases,
		config.RepoFleet: repos.Fleet, config.RepoSystemServices: repos.SystemServices,
	}
	var problems []string
	compared := 0
	for _, gr := range list.Items {
		url, _, _ := unstructured.NestedString(gr.Object, "spec", "url")
		name := strings.TrimSuffix(path.Base(strings.TrimSuffix(url, "/")), ".git")
		dir, ok := dirs[name]
		if !ok {
			continue
		}
		revision, _, _ := unstructured.NestedString(gr.Object, "status", "artifact", "revision")
		ref, clusterSHA := splitRevision(revision)
		if clusterSHA == "" {
			continue // not fetched yet: nothing to compare against
		}
		out, err := gitOutput(ctx, opts, dir, "rev-parse", "HEAD", "--abbrev-ref", "HEAD")
		fields := strings.Fields(out)
		if err != nil || len(fields) < 2 {
			problems = append(problems, fmt.Sprintf("%s: couldn't read the checkout at %s (is it a git repository?)", name, dir))
			continue
		}
		compared++
		localSHA, branch := fields[0], fields[1]
		if strings.HasPrefix(localSHA, clusterSHA) || strings.HasPrefix(clusterSHA, localSHA) {
			continue
		}
		problem := fmt.Sprintf("%s: the cluster runs %s @ %s, but the checkout at %s is at %s (%s)",
			name, ref, short(clusterSHA), dir, short(localSHA), branch)
		if wt := worktreeAt(ctx, opts, dir, clusterSHA); wt != "" {
			problem += fmt.Sprintf("; the worktree %s is at the cluster's revision — set repos.%s to it in environment.yaml", wt, name)
		}
		problems = append(problems, problem)
	}
	if len(problems) > 0 {
		return Check{Name: CheckRevisions, OK: false, Optional: true,
			Detail: strings.Join(problems, "; ") + " (apps diff/migrate render from the checkouts)"}
	}
	if compared == 0 {
		return skip("the cluster has no GitRepository for these repositories to compare with")
	}
	return Check{Name: CheckRevisions, OK: true, Optional: true, Detail: fmt.Sprintf("%d repo(s) match the cluster's revisions", compared)}
}

// splitRevision splits a Flux artifact revision, "refs/heads/main@sha1:abc…"
// (or "main@sha1:abc…"), into its ref and commit.
func splitRevision(revision string) (ref, sha string) {
	ref, rest, ok := strings.Cut(revision, "@")
	if !ok {
		return "", ""
	}
	_, sha, _ = strings.Cut(rest, ":")
	return ref, sha
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func gitOutput(ctx context.Context, opts Options, dir string, args ...string) (string, error) {
	if opts.Git != nil {
		return opts.Git(ctx, dir, args...)
	}
	stdout, _, err := opts.Runner.Run(ctx, "git", append([]string{"-C", dir}, args...)...)
	return string(stdout), err
}

// worktreeAt returns the path of a worktree of the repository at dir whose
// HEAD is sha, or "".
func worktreeAt(ctx context.Context, opts Options, dir, sha string) string {
	out, err := gitOutput(ctx, opts, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	current := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			head := strings.TrimPrefix(line, "HEAD ")
			if current != "" && (strings.HasPrefix(head, sha) || strings.HasPrefix(sha, head)) {
				return current
			}
		}
	}
	return ""
}
