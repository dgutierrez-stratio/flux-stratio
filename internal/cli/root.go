// Package cli wires flux-stratio's cobra command tree. It holds no business
// logic: every RunE resolves flags into an Options struct and delegates to
// one function in an internal/<pkg>, mirroring flux-keos's internal/cli.
package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/log"
)

// version is overridden at build time via -ldflags "-X .../cli.version=...".
var version = "dev"

// configFlag holds the --config value shared by all subcommands that read
// the component catalog. It is resolved through internal/config.Resolve.
var configFlag string

// envConfigFlag holds the --env-config value shared by all subcommands that
// read the environment file. It is resolved through
// internal/config.ResolveEnvironment.
var envConfigFlag string

// baseFlag, repoFlag, clusterFlag and tenantFlag override the environment
// file's base, repos, cluster and tenant fields, shared by all subcommands
// that operate on a specific tenant.
var (
	baseFlag    string
	repoFlag    map[string]string
	clusterFlag string
	tenantFlag  string
)

// kubeconfigArgs holds the --kubeconfig/--kube-context flags shared by all
// subcommands that talk to a live cluster, following flux-keos's and
// flux-operator's naming convention (kubectl-style flag names, prefixed
// with kube- to distinguish them from `flux`'s own --context/--namespace,
// per RFC-0013).
var kubeconfigArgs = genericclioptions.NewConfigFlags(false)

// verboseFlag holds the --verbose flag shared by all subcommands, gating
// internal/log.Logger.Debugf output (e.g. external command chatter).
var verboseFlag bool

// envOverrides returns the --base/--repo/--cluster/--tenant flags as an
// Environment to apply on top of the environment file.
func envOverrides() config.Environment {
	return config.Environment{Base: baseFlag, Repos: repoFlag, Cluster: clusterFlag, Tenant: tenantFlag}
}

// loadEnvironment loads the environment file with the root flags applied.
func loadEnvironment() (config.Environment, error) {
	return config.LoadEnvironment(envConfigFlag, envOverrides())
}

// rootLogger returns a Logger for cmd, writing to its stderr (so tests can
// capture it via cmd.SetErr, and production defaults to os.Stderr).
func rootLogger(cmd *cobra.Command) *log.Logger {
	return log.New(cmd.ErrOrStderr(), verboseFlag)
}

// NewRootCommand builds the `flux stratio` command tree: tenant import,
// application backup/diff/migrate, and preflight diagnostics.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:          "stratio",
		Short:        "Migrate Stratio applications from Ansible-based clusters onto Flux/GitOps tenants",
		SilenceUsage: true,
		// SilenceErrors: cmd/flux-stratio/main.go already prints "Error:
		// <err>" once itself after Execute returns — leaving cobra's own
		// default error-printing on would print the same line a second
		// time, on top of whatever a command already narrated via its
		// own logger.Failuref.
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&configFlag, "config", "", "path to the component catalog file (default: $FLUX_STRATIO_CONFIG, ~/.fluxcd/flux-stratio/catalog.yaml, or ./flux-stratio.yaml)")
	root.PersistentFlags().StringVar(&envConfigFlag, "env-config", "", "path to the environment file (default: $FLUX_STRATIO_ENV, ~/.fluxcd/flux-stratio/environment.yaml, or ./flux-stratio-env.yaml)")
	root.PersistentFlags().StringVar(&baseFlag, "base", "", "default parent directory of the repositories: keos-apps, keos-use-cases, keos-fleet, keos-system-services and charts, each expected at <base>/<name> unless --repo says otherwise (overrides the environment file's base)")
	root.PersistentFlags().StringToStringVar(&repoFlag, "repo", nil, "point one repository straight at its checkout, e.g. --repo charts=/path/to/worktree (repeatable; names: "+strings.Join(config.RepoNames, ", ")+"; overrides the environment file's repos)")
	root.PersistentFlags().StringVar(&clusterFlag, "cluster", "", "the cluster name to operate on (overrides the environment file's cluster)")
	root.PersistentFlags().StringVar(&tenantFlag, "tenant", "", "the tenant name to operate on (overrides the environment file's tenant)")
	root.PersistentFlags().StringVar(kubeconfigArgs.KubeConfig, "kubeconfig", "", "path to the kubeconfig file to use for cluster access")
	root.PersistentFlags().StringVar(kubeconfigArgs.Context, "kube-context", "", "the name of the kubeconfig context to use")
	root.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "print diagnostic detail (e.g. external command chatter)")

	root.AddCommand(newVersionCommand())
	root.AddCommand(newDoctorCommand())
	root.AddCommand(newConfigCommand())
	root.AddCommand(newAppsCommand())
	root.AddCommand(newTenantCommand())

	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the flux-stratio version",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version)
			return err
		},
	}
}
