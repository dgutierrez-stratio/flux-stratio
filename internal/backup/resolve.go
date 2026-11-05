package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// backupFileMarkers are the files that mean "this directory is itself a
// captured backup," not a parent of one — one per shape Run can produce.
var backupFileMarkers = []string{"cr.yaml", "env-vars.env", "helmrelease.yaml"}

// timestampDirPattern matches a Run-produced backup directory's name
// (timestampFormat) exactly, so a stray unrelated subdirectory under a
// backups root is never mistaken for one.
var timestampDirPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}Z$`)

// ResolveBaseline finds the actual timestamped backup directory a
// --baseline or --drift value refers to, so the operator never has to
// type the full path Run wrote. root may be:
//   - a specific timestamped backup directory already (contains one of
//     backupFileMarkers directly) — used as-is;
//   - an app's own backup root, Dir/<App.ID> — the most recent timestamped
//     subdirectory is picked;
//   - the overall backups root, Dir — root/appID is resolved the same way.
func ResolveBaseline(root, appID string) (string, error) {
	if isBackupDir(root) {
		return root, nil
	}

	dir := root
	if candidate := filepath.Join(root, appID); isDir(candidate) {
		dir = candidate
	}

	latest, err := latestTimestampSubdir(dir)
	if err != nil {
		return "", fmt.Errorf("locating a backup for %q under %s: %w", appID, dir, err)
	}
	return filepath.Join(dir, latest), nil
}

func isBackupDir(dir string) bool {
	for _, marker := range backupFileMarkers {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func latestTimestampSubdir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var latest string
	for _, e := range entries {
		if !e.IsDir() || !timestampDirPattern.MatchString(e.Name()) {
			continue
		}
		if e.Name() > latest {
			latest = e.Name()
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no backup found (looked for a %s-named directory)", timestampFormat)
	}
	return latest, nil
}
