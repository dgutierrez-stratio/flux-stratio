package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Stratio/flux-stratio/internal/components"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/discovery"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

// session is everything an apps subcommand loads before doing its own
// work: the catalog, the environment, a cluster client, and one
// cluster-wide discovery scan covering every kind the catalog selects on.
type session struct {
	cat    *config.Catalog
	env    config.Environment
	client client.Client
	index  *discovery.Index
	log    *log.Logger
}

func openSession(cmd *cobra.Command) (*session, error) {
	logger := rootLogger(cmd)
	cat, err := config.Load(configFlag)
	if err != nil {
		return nil, err
	}
	env, err := loadEnvironment()
	if err != nil {
		return nil, err
	}
	c, err := kubeclient.New(kubeconfigArgs)
	if err != nil {
		return nil, fmt.Errorf("connecting to the cluster: %w", err)
	}

	logger.Actionf("scanning the live cluster")
	idx, err := discovery.Scan(cmd.Context(), c, logger, cat.Kinds()...)
	if err != nil {
		return nil, fmt.Errorf("scanning the live cluster: %w", err)
	}
	logger.Successf("scan complete")
	return &session{cat: cat, env: env, client: c, index: idx, log: logger}, nil
}

// resolveOptions builds components.Options for this session. withTenant
// loads the tenant file so every instance is checked against it (needed
// whenever desired state gets rendered); interactive picks a terminal
// prompter over stdin/stderr instead of failing on the first question.
func (s *session) resolveOptions(cmd *cobra.Command, withTenant, interactive bool, as string) (components.Options, error) {
	opts := components.Options{
		Catalog: s.cat, Objects: s.index.Objects(), Tenant: s.env.Tenant, As: as, Log: s.log,
		Prompter: components.NonInteractive{},
	}
	if interactive {
		opts.Prompter = components.NewTerminal(cmd.InOrStdin(), cmd.ErrOrStderr())
	}
	if withTenant {
		doc, err := tenantfile.Load(tenantfile.Path(s.env.Repo(config.RepoFleet), s.env.Cluster, s.env.Tenant))
		if err != nil {
			return components.Options{}, err
		}
		opts.Doc = doc
	}
	return opts, nil
}

// addAsFlag registers --as on cmd.
func addAsFlag(cmd *cobra.Command, as *string) {
	cmd.Flags().StringVar(as, "as", "",
		"answer the type/entry question up front: <type> picks which catalog type the live object is, "+
			"<type>/<entry> also pins which components.<key> entry in the tenant file it migrates into")
}
