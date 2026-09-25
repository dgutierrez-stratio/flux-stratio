// Package doctor implements `flux stratio doctor`: a single preflight pass
// over everything a migration command depends on — required binaries, the
// app catalog config, the GitOps repo layout, each chart-mode app's
// on-disk chart directory, cluster access, and the target tenant file —
// so a broken prerequisite is caught up front, not discovered
// mid-migration.
package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/reporequire"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

// Options configures a doctor run. NewClient and Runner default to the
// real cluster client and real subprocess Runner in production; tests
// inject fakes.
type Options struct {
	ConfigFlag                                    string
	BaseOverride, ClusterOverride, TenantOverride string
	KubeconfigArgs                                *genericclioptions.ConfigFlags
	Runner                                        runner.Runner
	// NewClient builds a cluster client from KubeconfigArgs. Defaults to
	// kubeclient.New; overridden in tests to avoid a real kubeconfig.
	NewClient func(*genericclioptions.ConfigFlags) (client.Client, error)
	Log       *log.Logger
}

// CheckName identifies one of doctor's checks.
type CheckName string

// The checks doctor runs, in order.
const (
	CheckBinaries   CheckName = "binaries"
	CheckConfig     CheckName = "config"
	CheckRepoLayout CheckName = "repo layout"
	CheckChartPaths CheckName = "chart paths"
	CheckCluster    CheckName = "cluster access"
	CheckTenant     CheckName = "tenant file"
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
// A check after a hard prerequisite failure (an unloadable config makes
// the repo-layout, cluster and tenant checks meaningless) is skipped
// rather than reported as a confusing false failure.
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

	cfg, cfgCheck := checkConfig(opts)
	report.Checks = append(report.Checks, cfgCheck)
	if cfg == nil {
		// Nothing below this point can be meaningfully checked without a
		// loaded config (no base/cluster/tenant to check against).
		return report
	}

	base, cluster, tenant := cfg.Effective(opts.BaseOverride, opts.ClusterOverride, opts.TenantOverride)

	report.Checks = append(report.Checks, checkRepoLayout(base))
	report.Checks = append(report.Checks, checkChartPaths(cfg, base))
	report.Checks = append(report.Checks, checkCluster(ctx, opts))
	report.Checks = append(report.Checks, checkTenantFile(base, cluster, tenant))

	return report
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

func checkConfig(opts Options) (*config.Config, Check) {
	opts.Log.Actionf("loading config")
	cfg, err := config.Load(opts.ConfigFlag)
	if err != nil {
		opts.Log.Failuref("%v", err)
		return nil, Check{Name: CheckConfig, OK: false, Detail: err.Error()}
	}
	opts.Log.Successf("config loaded (%d app(s))", len(cfg.Apps))
	return cfg, Check{Name: CheckConfig, OK: true, Detail: fmt.Sprintf("%d app(s)", len(cfg.Apps))}
}

func checkRepoLayout(base string) Check {
	if err := reporequire.Validate(base); err != nil {
		return Check{Name: CheckRepoLayout, OK: false, Detail: err.Error()}
	}
	return Check{Name: CheckRepoLayout, OK: true, Detail: base}
}

// checkChartPaths validates that every chart-mode app's on-disk chart
// directory (App.ChartPath, resolved against ChartsBase when set, else
// Base — see internal/appdiff.chartPath/internal/backup's own copy of the
// same resolution) actually exists, so a misconfigured or missing chart
// checkout is caught here instead of surfacing mid-run as a "could not
// find <path>" error from the first chart-mode apps diff/backup/migrate.
func checkChartPaths(cfg *config.Config, base string) Check {
	chartsBase := cfg.ChartsBase
	if chartsBase == "" {
		chartsBase = base
	}
	var missing []string
	for _, app := range cfg.Apps {
		if app.ChartPath == "" {
			continue
		}
		path := filepath.Join(chartsBase, app.ChartPath)
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			missing = append(missing, fmt.Sprintf("%s (%s)", app.ID, path))
		}
	}
	if len(missing) > 0 {
		return Check{Name: CheckChartPaths, OK: false, Detail: fmt.Sprintf("chart directory not found for: %s", strings.Join(missing, ", "))}
	}
	return Check{Name: CheckChartPaths, OK: true, Detail: chartsBase}
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

func checkTenantFile(base, cluster, tenant string) Check {
	path := tenantfile.Path(base, cluster, tenant)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return Check{Name: CheckTenant, OK: false, Detail: fmt.Sprintf("%s not found (run `flux stratio tenant import` first)", path)}
	}
	return Check{Name: CheckTenant, OK: true, Detail: path}
}
