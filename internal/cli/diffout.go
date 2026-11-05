package cli

import (
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Stratio/flux-stratio/internal/ui"
)

// diffOptions styles a diff written to cmd's stdout: the two sides named in
// its header, and colour only when stdout is a terminal that hasn't opted out
// (https://no-color.org) — a diff piped to a file or a pager stays plain text.
func diffOptions(cmd *cobra.Command, oldLabel, newLabel string, sections ui.Sections) ui.DiffOptions {
	return ui.DiffOptions{OldLabel: oldLabel, NewLabel: newLabel, Color: stdoutColor(cmd), Sections: sections}
}

func stdoutColor(cmd *cobra.Command) bool {
	f, ok := cmd.OutOrStdout().(*os.File)
	if !ok {
		return false
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	return colorWanted(term.IsTerminal(int(f.Fd())), noColor, os.Getenv("TERM"))
}

// colorWanted: styling only on a real terminal, that hasn't opted out with
// NO_COLOR (set, even empty: https://no-color.org) and isn't a dumb one.
func colorWanted(isTerminal, noColor bool, termName string) bool {
	return isTerminal && !noColor && termName != "dumb"
}

// desiredLabel names the desired side of a comparison: what the tenant file
// renders, its patches included, as Flux applies it.
const desiredLabel = ui.LabelDesired + " (tenant file as rendered)"

// Sides of a tenant-file edit, for the preview of what a command would write.
const (
	tenantFileCurrent = "tenant file (current)"
	tenantFileWithNew = "tenant file (with the change)"
)

// sectionsFor is how an app's comparison names where a change is: by
// workload for a chart (one line per variable), by YAML path for a manifest.
func sectionsFor(chartPath string) ui.Sections {
	if chartPath != "" {
		return ui.SectionsWorkload
	}
	return ui.SectionsYAML
}
