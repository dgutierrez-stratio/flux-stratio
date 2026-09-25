package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Stratio/flux-stratio/internal/config"
)

const seedHeader = "" +
	"# Seeded by `flux stratio config init` from the known Stratio application catalog.\n" +
	"# Review before use: an environment may run a subset of these applications, or run\n" +
	"# ones this catalog doesn't know about yet. See docs/config-reference.md.\n"

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the flux-stratio config file",
	}
	cmd.AddCommand(newConfigInitCommand())
	return cmd
}

func newConfigInitCommand() *cobra.Command {
	var output string
	var force bool
	var charts string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starting config file, seeded with the known Stratio application catalog",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigInit(cmd, output, force, charts)
		},
	}
	cmd.Flags().StringVar(&output, "output", "flux-stratio.yaml", "file to write the seeded config to")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite --output if it already exists")
	cmd.Flags().StringVar(&charts, "charts", "", "optional: parent directory holding chart-mode apps' Helm chart sources, if it isn't a sibling of --base's keos-* repos")
	return cmd
}

func runConfigInit(cmd *cobra.Command, output string, force bool, charts string) error {
	if baseFlag == "" || clusterFlag == "" || tenantFlag == "" {
		return fmt.Errorf("--base, --cluster and --tenant are all required (there is no config file yet to read them from)")
	}
	if !force {
		if _, err := os.Stat(output); err == nil {
			return fmt.Errorf("%s already exists; pass --force to overwrite", output)
		}
	}

	seeded := config.Seed(baseFlag, clusterFlag, tenantFlag, charts)
	body, err := config.Marshal(seeded)
	if err != nil {
		return fmt.Errorf("marshaling seeded config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(output), err)
	}
	if err := os.WriteFile(output, append([]byte(seedHeader), body...), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", output, err)
	}

	logger := rootLogger(cmd)
	logger.Successf("wrote %s (%d apps)", output, len(seeded.Apps))
	logger.Actionf("review it, then run `flux stratio doctor` to check it against your cluster")
	return nil
}
