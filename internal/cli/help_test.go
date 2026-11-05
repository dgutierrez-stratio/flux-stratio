package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// walk calls fn for every command of the tree except cobra's own
// (completion, help).
func walk(cmd *cobra.Command, fn func(*cobra.Command)) {
	for _, c := range cmd.Commands() {
		if c.Name() == "completion" || c.Name() == "help" {
			continue
		}
		fn(c)
		walk(c, fn)
	}
}

// Every command says what it does in one line and in full, and every leaf
// command shows an example.
func TestHelp_EveryCommandIsDocumented(t *testing.T) {
	root := NewRootCommand()
	if root.Long == "" || root.Example == "" {
		t.Error("root has no Long or Example")
	}
	walk(root, func(c *cobra.Command) {
		path := c.CommandPath()
		if c.Short == "" || len(c.Short) > 90 {
			t.Errorf("%s: Short = %q, want one line of at most 90 characters", path, c.Short)
		}
		if c.Long == "" && c.Name() != "version" {
			t.Errorf("%s has no Long help", path)
		}
		if !c.HasSubCommands() && c.Name() != "version" && c.Example == "" {
			t.Errorf("%s has no Example", path)
		}
	})
}

// Every flag explains itself, briefly: the long explanations belong in the
// command's Long text.
func TestHelp_EveryFlagIsExplainedBriefly(t *testing.T) {
	root := NewRootCommand()
	check := func(path string, fs *pflag.FlagSet) {
		fs.VisitAll(func(f *pflag.Flag) {
			if f.Usage == "" {
				t.Errorf("%s --%s has no usage text", path, f.Name)
			}
			if len(f.Usage) > 170 {
				t.Errorf("%s --%s usage is %d characters, want at most 170: move the detail to the command's Long text", path, f.Name, len(f.Usage))
			}
		})
	}
	check("global", root.PersistentFlags())
	walk(root, func(c *cobra.Command) { check(c.CommandPath(), c.LocalNonPersistentFlags()) })
}

// The Long text of the commands with modes names every one of their flags
// that picks a mode or changes what is written, so the explanation and the
// flag list can't drift apart.
func TestHelp_LongTextNamesTheModeFlags(t *testing.T) {
	want := map[string][]string{
		"apps diff":     {"--baseline", "--drift", "--view", "--as", "--dir"},
		"apps migrate":  {"--dry-run", "--yes", "--accept-warnings", "--skip-prepare", "--continue-on-error", "--baseline", "--all", "--as"},
		"apps backup":   {"--catalog", "--all", "--dir"},
		"tenant import": {"--output", "--force", "--dry-run", "--size"},
	}
	root := NewRootCommand()
	visited := 0
	walk(root, func(c *cobra.Command) {
		names := want[strings.TrimPrefix(c.CommandPath(), "stratio ")]
		if names != nil {
			visited++
		}
		for _, n := range names {
			text := c.Long + "\n" + c.Example
			if !strings.Contains(text, n) {
				t.Errorf("%s: Long/Example never mention %s", c.CommandPath(), n)
			}
		}
	})
	if visited != len(want) {
		t.Errorf("checked %d of %d commands: one was renamed", visited, len(want))
	}
}
