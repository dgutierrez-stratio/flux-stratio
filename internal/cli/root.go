// Package cli wires flux-stratio's cobra command tree. It holds no business
// logic: every RunE resolves flags into an Options struct and delegates to
// one function in an internal/<pkg>, mirroring flux-keos's internal/cli.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/Stratio/flux-stratio/internal/log"
)

// version is overridden at build time via -ldflags "-X .../cli.version=...".
var version = "dev"

// configFlag holds the --config value shared by all subcommands that read
// the app catalog. It is resolved through internal/config.Resolve.
var configFlag string

// baseFlag, clusterFlag and tenantFlag override the config file's base,
// cluster and tenant fields, shared by all subcommands that operate on a
// specific tenant.
var (
	baseFlag    string
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

// rootLogger returns a Logger for cmd, writing to its stderr (so tests can
// capture it via cmd.SetErr, and production defaults to os.Stderr).
func rootLogger(cmd *cobra.Command) *log.Logger {
	return log.New(cmd.ErrOrStderr(), verboseFlag)
}

// NewRootCommand builds the `flux stratio` command tree: tenant import,
// application backup/diff/migrate, and preflight diagnostics.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "stratio",
		Short:         "Migrate Stratio applications from Ansible-based clusters onto Flux/GitOps tenants",
		SilenceUsage:  true,
		SilenceErrors: false,
	}

	root.PersistentFlags().StringVar(&configFlag, "config", "", "path to the flux-stratio config file (default: $FLUX_STRATIO_CONFIG, persisted default, or ./flux-stratio.yaml)")
	root.PersistentFlags().StringVar(&baseFlag, "base", "", "path to the parent directory holding keos-apps, keos-use-cases, keos-fleet and keos-system-services (overrides the config file's base)")
	root.PersistentFlags().StringVar(&clusterFlag, "cluster", "", "the cluster name to operate on (overrides the config file's cluster)")
	root.PersistentFlags().StringVar(&tenantFlag, "tenant", "", "the tenant name to operate on (overrides the config file's tenant)")
	root.PersistentFlags().StringVar(kubeconfigArgs.KubeConfig, "kubeconfig", "", "path to the kubeconfig file to use for cluster access")
	root.PersistentFlags().StringVar(kubeconfigArgs.Context, "kube-context", "", "the name of the kubeconfig context to use")
	root.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "print diagnostic detail (e.g. external command chatter)")

	root.AddCommand(newVersionCommand())
	root.AddCommand(newDoctorCommand())
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
