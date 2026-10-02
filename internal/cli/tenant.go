package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/tenantimport"
)

func newTenantCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tenant",
		Short: "Create tenant artifacts",
		Long:  tenantLong,
	}
	cmd.AddCommand(newTenantImportCommand())
	return cmd
}

func newTenantImportCommand() *cobra.Command {
	var size string
	var output string
	var force bool

	cmd := &cobra.Command{
		Use:     "import",
		Short:   "Scan the live cluster and render the tenant file skeleton",
		Long:    tenantImportLong,
		Example: tenantImportExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTenantImport(cmd, size, output, force)
		},
	}
	cmd.Flags().StringVar(&size, "size", "S", "default size of every component: S, M or L (the keos-apps size overlays)")
	cmd.Flags().StringVar(&output, "output", "", "file to write the tenant file to (default: print to stdout)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite --output if it exists (drops anything written by hand)")
	return cmd
}

func runTenantImport(cmd *cobra.Command, size, output string, force bool) error {
	if output != "" && !force {
		if _, err := os.Stat(output); err == nil {
			return fmt.Errorf(
				"%s already exists; pass --force to overwrite (regenerating drops any hand-authored fields, comments or patches — see apps migrate for adding patches safely)",
				output,
			)
		}
	}

	logger := rootLogger(cmd)
	env, err := loadEnvironment()
	if err != nil {
		return err
	}
	tenant := env.Tenant

	cat, err := catalog.Load(env.Repo(config.RepoUseCases))
	if err != nil {
		return err
	}
	c, err := kubeclient.New(kubeconfigArgs)
	if err != nil {
		return fmt.Errorf("connecting to the cluster: %w", err)
	}

	logger.Actionf("scanning the live cluster for tenant %q", tenant)
	out, err := tenantimport.Run(cmd.Context(), tenantimport.Options{
		TenantName: tenant,
		Size:       size,
		Catalog:    cat,
		Client:     c,
		Log:        logger,
	})
	if err != nil {
		return err
	}
	logger.Successf("scan complete")

	if output == "" {
		_, err := cmd.OutOrStdout().Write(out)
		return err
	}
	if err := os.WriteFile(output, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", output, err)
	}
	logger.Successf("wrote %s", output)
	return nil
}
