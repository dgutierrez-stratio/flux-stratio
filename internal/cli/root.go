// Package cli wires flux-stratio's cobra command tree. It holds no business
// logic: every RunE resolves flags into an Options struct and delegates to
// one function in an internal/<pkg>, mirroring flux-keos's internal/cli.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
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
	out := cmd.ErrOrStderr()
	logger := log.New(out, verboseFlag)
	// Tables want the terminal's width, and styling only on a real terminal
	// that hasn't opted out (https://no-color.org).
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		width, _, _ := term.GetSize(int(f.Fd()))
		_, noColor := os.LookupEnv("NO_COLOR")
		return logger.WithTerminal(width, !noColor && os.Getenv("TERM") != "dumb")
	}
	return logger
}

// NewRootCommand builds the `flux stratio` command tree: tenant import,
// application backup/diff/migrate, and preflight diagnostics.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:          "stratio",
		Short:        "Migrate Stratio applications from CCT onto Flux/GitOps tenants",
		Long:         rootLong,
		Example:      rootExample,
		SilenceUsage: true,
		// SilenceErrors: cmd/flux-stratio/main.go already prints "Error:
		// <err>" once itself after Execute returns — leaving cobra's own
		// default error-printing on would print the same line a second
		// time, on top of whatever a command already narrated via its
		// own logger.Failuref.
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&configFlag, "config", "", "component catalog file (default: $FLUX_STRATIO_CONFIG, else ~/.fluxcd/flux-stratio/catalog.yaml, else ./flux-stratio.yaml)")
	root.PersistentFlags().StringVar(&envConfigFlag, "env-config", "", "environment file (default: $FLUX_STRATIO_ENV, else ~/.fluxcd/flux-stratio/environment.yaml, else ./flux-stratio-env.yaml)")
	root.PersistentFlags().StringVar(&baseFlag, "base", "", "parent directory of the repositories; each is <base>/<name> unless --repo overrides it (default: the environment file's)")
	root.PersistentFlags().StringToStringVar(&repoFlag, "repo", nil, "name=path of one repository's checkout, repeatable (names: "+strings.Join(config.RepoNames, ", ")+"); overrides the environment file")
	root.PersistentFlags().StringVar(&clusterFlag, "cluster", "", "cluster to operate on (default: the environment file's)")
	root.PersistentFlags().StringVar(&tenantFlag, "tenant", "", "tenant to operate on (default: the environment file's)")
	root.PersistentFlags().Lookup("repo").DefValue = "" // pflag would print "(default [])"
	root.PersistentFlags().StringVar(kubeconfigArgs.KubeConfig, "kubeconfig", "", "kubeconfig file for cluster access")
	root.PersistentFlags().StringVar(kubeconfigArgs.Context, "kube-context", "", "kubeconfig context to use")
	root.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "print diagnostics, list the notes of a review and show its values whole")

	// The binary is flux-stratio, run as `flux stratio ...`: show that in
	// every Usage line instead of the bare command name.
	usage := strings.NewReplacer("{{.UseLine}}", "flux {{.UseLine}}", "{{.CommandPath}}", "flux {{.CommandPath}}").Replace(root.UsageTemplate())
	root.SetUsageTemplate(usage)

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
