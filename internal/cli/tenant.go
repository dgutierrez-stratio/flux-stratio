package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/components"
	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/kubeclient"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/runner"
	"github.com/Stratio/flux-stratio/internal/tenantcheck"
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
	var force, dryRun, noValidate, yes bool

	cmd := &cobra.Command{
		Use:     "import",
		Short:   "Scan the live cluster and create the tenant file, or complete an existing one",
		Long:    tenantImportLong,
		Example: tenantImportExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTenantImport(cmd, tenantImportFlags{
				size: size, output: output, force: force, dryRun: dryRun, noValidate: noValidate, yes: yes,
			})
		},
	}
	cmd.Flags().StringVar(&size, "size", "S", "default size of every component: S, M or L (the keos-apps size overlays)")
	cmd.Flags().StringVar(&output, "output", "", "tenant file to write (default: print to stdout); if it exists it is completed, not replaced")
	cmd.Flags().BoolVar(&force, "force", false, "regenerate --output from scratch even if it exists (drops anything written by hand)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "when completing, print what would change as a diff and write nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "enable every optional dependency that runs on the cluster without asking, reporting each to review (default: ask)")
	cmd.Flags().BoolVar(&noValidate, "no-validate", false, "skip validating the resulting tenant file (structure, dependencies, and rendering every ResourceSet against it)")
	return cmd
}

type tenantImportFlags struct {
	size, output              string
	force, dryRun, noValidate bool
	// yes answers every dependency proposal with "enable" instead of asking.
	yes bool
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
	// workloads on (virtualizer's uiEnabled). Without it that key falls back
	// to its template default, which is exactly how a live monitor/ui gets
	// dropped, so say so rather than skip silently.
	cfg, err := config.Load(configFlag)
	if err != nil {
		logger.Warningf("component catalog not loaded (%v): config keys derived from live sibling workloads (virtualizer's uiEnabled) are not set", err)
		cfg = nil
	} else {
		for _, d := range cfg.Deprecated() {
			logger.Warningf("%s", d)
		}
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
		Decider:    tenantDecider(cmd, f),
	}
	validate := func([]byte, string) error { return nil }
	if !f.noValidate {
		validate = func(content []byte, path string) error {
			return validateTenantFile(cmd, logger, cat, env.Repo(config.RepoUseCases), content, path)
		}
	}
	logger.Actionf("scanning the live cluster for tenant %q", tenant)
	if completing {
		return completeTenantFile(cmd, opts, f, logger, validate)
	}
	out, err := tenantimport.Run(cmd.Context(), opts)
	if err != nil {
		return err
	}
	logger.Successf("scan complete")

	if f.output == "" {
		if _, err := cmd.OutOrStdout().Write(out); err != nil {
			return err
		}
		return validate(out, "")
	}
	if err := os.WriteFile(f.output, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", f.output, err)
	}
	logger.Successf("wrote %s", f.output)
	return validate(nil, f.output)
}

// tenantDecider is who answers the proposals to enable a dependency that runs
// on the cluster: --yes accepts them all, a dry run never asks, and anything
// else asks on the terminal (stdin that runs out, or a blank answer, leaves the
// proposal undecided and reported: silence never means yes).
func tenantDecider(cmd *cobra.Command, f tenantImportFlags) tenantimport.Decider {
	switch {
	case f.yes:
		return tenantimport.Decider{AssumeYes: true}
	case f.dryRun:
		return tenantimport.Decider{}
	}
	return tenantimport.Decider{Prompter: components.NewTerminal(cmd.InOrStdin(), cmd.ErrOrStderr())}
}

// validateTenantFile checks the tenant file the run produced (tenantcheck):
// the file at path, or content when path is empty (stdout, --dry-run), which
// goes through a scratch file since flux-operator reads its inputs from a
// path. Problems are reported on the log and fail the command; the file is
// already written, as a starting point the operator completes.
func validateTenantFile(cmd *cobra.Command, logger *log.Logger, cat *catalog.Catalog, useCases string, content []byte, path string) error {
	if path == "" {
		tmp, err := os.CreateTemp("", "tenant-*.yaml")
		if err != nil {
			return fmt.Errorf("validating the tenant file: %w", err)
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.Write(content); err != nil {
			tmp.Close()
			return fmt.Errorf("validating the tenant file: %w", err)
		}
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("validating the tenant file: %w", err)
		}
		path = tmp.Name()
	}
	logger.Actionf("validating the tenant file (structure, dependencies, and every ResourceSet rendered against it)")
	res, err := tenantcheck.Validate(cmd.Context(), path, tenantcheck.Options{
		Catalog: cat, UseCases: useCases, Runner: runner.Exec{},
	})
	if err != nil {
		return err
	}
	for _, n := range res.Notes {
		logger.Actionf("%s", n)
	}
	for _, w := range res.Warnings {
		logger.Warningf("%s", w)
	}
	for _, e := range res.Errors {
		logger.Failuref("%s", e)
	}
	if !res.OK() {
		return fmt.Errorf("the tenant file has %d problem(s); fix them (or the cluster) and run `tenant import` again, or use --no-validate to skip this check", len(res.Errors))
	}
	if res.Rendered > 0 {
		logger.Successf("tenant file is valid: the ResourceSets render %d Kustomization(s) from it", res.Rendered)
	} else {
		logger.Successf("tenant file is structurally valid (render check skipped)")
	}
	return nil
}

// completeTenantFile adds to an existing tenant file what the cluster says it
// lacks (tenantimport.Complete), leaving everything else byte-for-byte as it
// is, and writes it back atomically unless dry-run or nothing was added.
func completeTenantFile(cmd *cobra.Command, opts tenantimport.Options, f tenantImportFlags, logger *log.Logger, validate func([]byte, string) error) error {
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
		TenantName: opts.TenantName, Size: opts.Size, Config: opts.Config, Decider: opts.Decider,
	})
	if err != nil {
		return err
	}
	reportTenantComplete(logger, report)

	if report.Empty() {
		logger.Successf("nothing to add to %s", f.output)
		return validate(nil, f.output)
	}
	if f.dryRun {
		if err := ui.FileDiffWith(cmd.OutOrStdout(), string(before), string(doc.Text()), diffOptions(cmd, tenantFileCurrent, tenantFileWithNew, ui.SectionsYAML)); err != nil {
			return err
		}
		return validate(doc.Text(), "")
	}
	if err := doc.Save(f.output); err != nil {
		return err
	}
	logger.Successf("completed %s: %d addition(s), nothing removed or changed", f.output, len(report.Added))
	return validate(nil, f.output)
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
