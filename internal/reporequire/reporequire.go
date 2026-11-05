// Package reporequire validates the one repository assumption every
// migration command shares: keos-apps, keos-use-cases, keos-fleet and
// keos-system-services are all checked out — as siblings under base, or
// wherever the environment's repos entries point — matching the Python
// client's flux_renderer.py check.
package reporequire

import (
	"fmt"
	"os"
	"strings"

	"github.com/Stratio/flux-stratio/internal/config"
)

// Dirs are the GitOps repositories every migration command expects
// checked out — under base by default, as <base>/<name>.
var Dirs = []string{config.RepoApps, config.RepoUseCases, config.RepoFleet, config.RepoSystemServices}

// Validate checks that every GitOps repository in repos is a directory,
// returning a single actionable error naming exactly which are missing
// and where they were looked for — nil if they're all there. The charts
// repository isn't checked: only chart-mode types need it, and doctor
// checks their chart directories themselves.
func Validate(repos config.RepoPaths) error {
	var missing []string
	for _, r := range []struct{ name, path string }{
		{config.RepoApps, repos.Apps},
		{config.RepoUseCases, repos.UseCases},
		{config.RepoFleet, repos.Fleet},
		{config.RepoSystemServices, repos.SystemServices},
	} {
		if r.path == "" {
			missing = append(missing, r.name+" (not set: pass --base or --repo, or set base or repos in the environment file)")
			continue
		}
		if info, err := os.Stat(r.path); err != nil || !info.IsDir() {
			missing = append(missing, fmt.Sprintf("%s (%s)", r.name, r.path))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("repository checkout(s) not found: %s", strings.Join(missing, ", "))
	}
	return nil
}
