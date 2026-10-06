package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
	"github.com/Stratio/flux-stratio/internal/tenantimport"
	"github.com/Stratio/flux-stratio/internal/ui"
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
	var force, dryRun bool

	cmd := &cobra.Command{
		Use:     "import",
		Short:   "Scan the live cluster and create the tenant file, or complete an existing one",
		Long:    tenantImportLong,
		Example: tenantImportExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTenantImport(cmd, tenantImportFlags{
				size: size, output: output, force: force, dryRun: dryRun,
			})
		},
	}
	cmd.Flags().StringVar(&size, "size", "S", "default size of every component: S, M or L (the keos-apps size overlays)")
	cmd.Flags().StringVar(&output, "output", "", "tenant file to write (default: print to stdout); if it exists it is completed, not replaced")
	cmd.Flags().BoolVar(&force, "force", false, "regenerate --output from scratch even if it exists (drops anything written by hand)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "when completing, print what would change as a diff and write nothing")
	return cmd
}

type tenantImportFlags struct {
	size, output  string
	force, dryRun bool
}

func runTenantImport(cmd *cobra.Command, f tenantImportFlags) error {
	completing := false
	if f.output != "" && !f.force {
		if _, err := os.Stat(f.output); err == nil {
			completing = true
		}
	}
	if f.dryRun && !completing {
		return fmt.Errorf("--dry-run only applies when completing an existing --output file")
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
	// The component catalog tells which config keys switch sibling
	// workloads on (virtualizer's uiEnabled). Without it those keys fall
	// back to their template default, which is exactly how a live
	// monitor/ui gets dropped, so say so rather than skip silently.
	cfg, err := config.Load(configFlag)
	if err != nil {
		logger.Warningf("component catalog not loaded (%v): config keys derived from live sibling workloads (virtualizer's uiEnabled) are not set", err)
		cfg = nil
	}
	c, err := kubeclient.New(kubeconfigArgs)
	if err != nil {
		return fmt.Errorf("connecting to the cluster: %w", err)
	}

	opts := tenantimport.Options{
		TenantName: tenant,
		Size:       f.size,
		Catalog:    cat,
		Config:     cfg,
		Client:     c,
		Log:        logger,
	}
	logger.Actionf("scanning the live cluster for tenant %q", tenant)
	if completing {
		return completeTenantFile(cmd, opts, f, logger)
	}
	out, err := tenantimport.Run(cmd.Context(), opts)
	if err != nil {
		return err
	}
	logger.Successf("scan complete")

	if f.output == "" {
		_, err := cmd.OutOrStdout().Write(out)
		return err
	}
	if err := os.WriteFile(f.output, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", f.output, err)
	}
	logger.Successf("wrote %s", f.output)
	return nil
}

// completeTenantFile adds to an existing tenant file what the cluster says it
// lacks (tenantimport.Complete), leaving everything else byte-for-byte as it
// is, and writes it back atomically unless dry-run or nothing was added.
func completeTenantFile(cmd *cobra.Command, opts tenantimport.Options, f tenantImportFlags, logger *log.Logger) error {
	doc, err := tenantfile.Load(f.output)
	if err != nil {
		return err
	}
	before, err := os.ReadFile(f.output)
	if err != nil {
		return fmt.Errorf("reading %s: %w", f.output, err)
	}

	// The scan's own warnings concern what it found, not what the file has;
	// Complete reports on the file's entries itself.
	scanOpts := opts
	scanOpts.Log = nil
	scanned, live, err := tenantimport.Scan(cmd.Context(), scanOpts, false)
	if err != nil {
		return err
	}
	logger.Successf("scan complete")

	report, err := tenantimport.Complete(doc, scanned, live, opts.Catalog, tenantimport.CompleteOptions{
		TenantName: opts.TenantName, Size: opts.Size,
	})
	if err != nil {
		return err
	}
	reportTenantComplete(logger, report)

	if report.Empty() {
		logger.Successf("nothing to add to %s", f.output)
		return nil
	}
	if f.dryRun {
		return ui.FileDiffWith(cmd.OutOrStdout(), string(before), string(doc.Text()), diffOptions(cmd, tenantFileCurrent, tenantFileWithNew, ui.SectionsYAML))
	}
	if err := doc.Save(f.output); err != nil {
		return err
	}
	logger.Successf("completed %s: %d addition(s), nothing removed or changed", f.output, len(report.Added))
	return nil
}

func reportTenantComplete(logger *log.Logger, r tenantimport.Report) {
	for _, a := range r.Added {
		logger.Generatef("added %s", a)
	}
	for _, l := range r.LeftAlone {
		logger.Actionf("left alone: %s", l)
	}
	for _, w := range r.Warnings {
		logger.Warningf("%s", w)
	}
}
