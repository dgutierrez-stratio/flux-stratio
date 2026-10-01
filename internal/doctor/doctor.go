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
	"path/filepath"
	"regexp"
	"strings"

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
	CheckTypes       CheckName = "catalog types"
	CheckSourcePaths CheckName = "kustomization paths"
	CheckCluster     CheckName = "cluster access"
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
		templates, err := catalog.Load(repos.UseCases)
		report.Checks = append(report.Checks, narrate(opts.Log, checkTypes(cat, templates, err)))
		if templates != nil {
			report.Checks = append(report.Checks, narrate(opts.Log, checkSourcePaths(cat, templates, repos)))
		}
	}
	report.Checks = append(report.Checks, checkCluster(ctx, opts))
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
