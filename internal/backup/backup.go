// Package backup captures an app's live legacy state to disk before it is
// migrated: exactly the same state internal/appdiff would otherwise fetch
// fresh and compare on the spot, written to
// <dir>/<app-id>/<UTC-timestamp>/ so it survives the live object changing
// or disappearing later, and so `apps diff --baseline` can diff against it
// instead of the live cluster.
//
// What gets captured is decided the same way diff mode is: a manifest-mode
// app (App.ChartPath == "") writes cr.yaml, the live custom resource's
// full manifest; a chart-mode app (App.ChartPath != "") writes
// deployment.yaml (its primary live workload's manifest) and
// env-vars.env (every live workload's resolved env vars, merged). Nothing
// is written to disk unless the live object was actually found — Run
// creates the timestamped directory lazily, right before its first
// successful write, so a failed backup never leaves an empty directory
// behind.
package backup

import (
	"context"
	"path/filepath"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/appdiff"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
)

// timestampFormat matches the Python client's own backup directory naming
// (BackupManager.create_backup_dir): UTC, colon-free so it's a valid path
// component on every OS.
const timestampFormat = "2006-01-02T15-04-05Z"

// Options configures backing up one app.
type Options struct {
	Base, Cluster, Tenant string
	App                   config.App
	Runner                runner.Runner
	Client                client.Client
	// Dir is the backups root directory; Run writes into
	// Dir/<App.ID>/<timestamp>/.
	Dir string
	Log *log.Logger
	// Clock returns the current time; defaults to time.Now. Tests inject
	// a fixed clock for a deterministic directory name.
	Clock func() time.Time
}

// Result reports what a backup run captured.
type Result struct {
	// Dir is the timestamped directory written to, or "" if nothing was
	// captured.
	Dir string
	// Files are the file names written, relative to Dir.
	Files []string
}

// Run backs up opts.App's live state.
func Run(ctx context.Context, opts Options) (*Result, error) {
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	dir := filepath.Join(opts.Dir, opts.App.ID, clock().UTC().Format(timestampFormat))

	var files []string
	var err error
	if opts.App.ChartPath == "" {
		files, err = backupManifest(ctx, opts, dir)
	} else {
		files, err = backupChart(ctx, opts, dir)
	}
	if err != nil {
		return nil, err
	}
	return &Result{Dir: dir, Files: files}, nil
}

func toAppdiffOptions(opts Options) appdiff.Options {
	return appdiff.Options{
		Base: opts.Base, Cluster: opts.Cluster, Tenant: opts.Tenant,
		App: opts.App, Runner: opts.Runner, Client: opts.Client, Log: opts.Log,
	}
}

func backupManifest(ctx context.Context, opts Options, dir string) ([]string, error) {
	live, err := appdiff.LiveManifestObject(ctx, toAppdiffOptions(opts))
	if err != nil {
		return nil, err
	}
	if err := writeYAMLFile(dir, "cr.yaml", live.Object); err != nil {
		return nil, err
	}
	return []string{"cr.yaml"}, nil
}

func backupChart(ctx context.Context, opts Options, dir string) ([]string, error) {
	aopts := toAppdiffOptions(opts)
	workloads, err := appdiff.LiveChartWorkloads(ctx, aopts)
	if err != nil {
		return nil, err
	}

	if err := writeYAMLFile(dir, "deployment.yaml", workloads[0].Object); err != nil {
		return nil, err
	}

	env, err := appdiff.MergeLiveEnv(ctx, aopts, workloads)
	if err != nil {
		return nil, err
	}
	if err := writeEnvFile(dir, "env-vars.env", env); err != nil {
		return nil, err
	}

	return []string{"deployment.yaml", "env-vars.env"}, nil
}
