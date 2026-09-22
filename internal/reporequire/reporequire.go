// Package reporequire validates the one directory-layout assumption every
// migration command shares: --base must hold keos-apps, keos-use-cases,
// keos-fleet and keos-system-services as sibling checkouts, matching the
// Python client's flux_renderer.py base-directory check.
package reporequire

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dirs are the sibling checkouts every migration command expects under
// --base.
var Dirs = []string{"keos-apps", "keos-use-cases", "keos-fleet", "keos-system-services"}

// Validate checks that base contains every directory in Dirs, returning a
// single actionable error naming exactly what's missing (or that base
// itself isn't set) — nil if the layout is complete.
func Validate(base string) error {
	if base == "" {
		return fmt.Errorf("base is not set (pass --base or set it in the config file)")
	}
	var missing []string
	for _, dir := range Dirs {
		info, err := os.Stat(filepath.Join(base, dir))
		if err != nil || !info.IsDir() {
			missing = append(missing, dir)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s is missing %s as sibling checkout(s) of base", base, strings.Join(missing, ", "))
	}
	return nil
}
