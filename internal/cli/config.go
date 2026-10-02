package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

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
	"# cluster/tenant to operate on. --base/--repo/--cluster/--tenant override it per run.\n"

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

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write the component catalog (seeded with the known Stratio components) and an environment file",
		Long: `Writes two files into --dir:

  catalog.yaml      the component catalog: every supported component type, with the
                    selectors that recognize its live legacy instances — static, the
                    same for every environment
  environment.yaml  --base (the parent directory of the keos-* GitOps repositories and
                    the charts repository), any --repo pointing one of them elsewhere,
                    --cluster and --tenant — this workstation's target`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigInit(cmd, dir, force)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory to write catalog.yaml and environment.yaml into (default: ~/.fluxcd/flux-stratio)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite catalog.yaml/environment.yaml if they already exist, keeping a timestamped .bak copy of each and the existing environment file's repos entries no --repo replaces")
	return cmd
}

func runConfigInit(cmd *cobra.Command, dir string, force bool) error {
	// Written absolute: a relative --base would otherwise resolve against
	// wherever a later command runs from.
	env, err := config.SeedEnvironment(baseFlag, clusterFlag, tenantFlag, repoFlag).AbsPaths("")
	if err != nil {
		return err
	}
	if err := env.Validate(); err != nil {
		return fmt.Errorf("%w (there is no environment file yet to read them from)", err)
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
		{config.EnvironmentFile, environmentHeader, env},
	}

	logger := rootLogger(cmd)
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if !force {
			return fmt.Errorf("%s already exists; pass --force to overwrite", path)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	// --force never loses what it replaces: each existing file is copied
	// aside first, and an existing environment file's repos entries (a
	// charts worktree, say) carry over unless a --repo flag replaces them.
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for i, f := range files {
		path := filepath.Join(dir, f.name)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		backup := path + ".bak-" + stamp
		if err := os.WriteFile(backup, data, 0o644); err != nil {
			return fmt.Errorf("backing up %s: %w", path, err)
		}
		logger.Successf("backed up %s to %s", path, backup)
		if f.name == config.EnvironmentFile {
			if existing, err := config.ParseEnvironmentRepos(data, dir); err == nil && len(existing) > 0 {
				kept := config.Environment{Repos: existing}.Override(config.Environment{Repos: env.Repos})
				env.Repos = kept.Repos
				files[i].value = env
			}
		}
	}

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
