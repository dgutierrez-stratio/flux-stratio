package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Stratio/flux-stratio/internal/config"
)

const catalogHeader = "" +
	"# Seeded by `flux stratio config init` from the known Stratio component catalog.\n" +
	"# Static, environment-independent component types only: each type's match selectors\n" +
	"# recognize its live legacy instances; instance names are derived at run time.\n" +
	"# Review before use — see docs/config-reference.md.\n"

const environmentHeader = "" +
	"# Seeded by `flux stratio config init`: where the GitOps repositories live and which\n" +
	"# cluster/tenant to operate on. --base/--cluster/--tenant override it per run.\n"

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the flux-stratio catalog and environment files",
	}
	cmd.AddCommand(newConfigInitCommand())
	return cmd
}

func newConfigInitCommand() *cobra.Command {
	var dir string
	var force bool
	var charts string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write the component catalog (seeded with the known Stratio components) and an environment file",
		Long: `Writes two files into --dir:

  catalog.yaml      the component catalog: every supported component type, with the
                    selectors that recognize its live legacy instances — static, the
                    same for every environment
  environment.yaml  --base (where the keos-* GitOps repositories are checked out),
                    --charts, --cluster and --tenant — this workstation's target`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigInit(cmd, dir, force, charts)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory to write catalog.yaml and environment.yaml into (default: ~/.fluxcd/flux-stratio)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite catalog.yaml/environment.yaml if they already exist")
	cmd.Flags().StringVar(&charts, "charts", "", "optional: parent directory holding chart-mode components' Helm chart sources, if it isn't a sibling of --base's keos-* repos")
	return cmd
}

func runConfigInit(cmd *cobra.Command, dir string, force bool, charts string) error {
	if baseFlag == "" || clusterFlag == "" || tenantFlag == "" {
		return fmt.Errorf("--base, --cluster and --tenant are all required (there is no environment file yet to read them from)")
	}
	if dir == "" {
		var err error
		if dir, err = config.UserDir(); err != nil {
			return err
		}
	}

	catalog := config.SeedCatalog()
	files := []struct {
		name, header string
		value        any
	}{
		{config.CatalogFile, catalogHeader, catalog},
		{config.EnvironmentFile, environmentHeader, config.SeedEnvironment(baseFlag, clusterFlag, tenantFlag, charts)},
	}

	if !force {
		for _, f := range files {
			path := filepath.Join(dir, f.name)
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists; pass --force to overwrite", path)
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	logger := rootLogger(cmd)
	for _, f := range files {
		body, err := config.Marshal(f.value)
		if err != nil {
			return fmt.Errorf("marshaling %s: %w", f.name, err)
		}
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, append([]byte(f.header), body...), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		logger.Successf("wrote %s", path)
	}
	logger.Actionf("catalog has %d component types; review it, then run `flux stratio doctor` to check it against your cluster", len(catalog.Types))
	return nil
}
