package diff

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// chartValueLineRe matches a config/*_env_vars.yaml-style line mapping an
// env var name to a single .Values dot path, e.g.
// `DEPLOYMENT_ENVIRONMENT: {{ .Values.general.deploymentEnvironment | quote }}`.
var chartValueLineRe = regexp.MustCompile(`^([A-Za-z0-9_-]+):\s*['"]?\{\{\s*\.Values\.([\w.]+)\s*(?:\|[^}]*)?\}\}['"]?\s*$`)

// BuildChartValuesMap scans every .yaml/.yml/.tpl file under chartPath
// (excluding Chart.yaml, Chart.lock and values.yaml) for lines shaped
// like chartValueLineRe, mapping each env-var-style key to its .Values dot
// path — the map internal/diff uses to translate a live env var's value
// back into a HelmRelease values patch.
//
// When preferredRoot is set (multi-flavor charts like bdl-datarest, whose
// flavors' env-vars files share one values tree with different top-level
// roots), a mapping whose path starts with that root always wins over one
// that doesn't; any other root only fills a key nothing has claimed yet.
// When preferredRoot is empty, the walk order decides ties (last file
// scanned wins for a given key) — deterministic per filepath.WalkDir's own
// lexical ordering, though not meaningful across different chart layouts.
func BuildChartValuesMap(chartPath, preferredRoot string) (map[string]string, error) {
	result := map[string]string{}
	err := filepath.WalkDir(chartPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort scan: an unreadable file/dir just contributes nothing
		}
		if d.IsDir() || !isScannableChartFile(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil //nolint:nilerr // best-effort scan, matching the Python client's own bare except
		}
		scanChartValueLines(string(data), preferredRoot, result)
		return nil
	})
	return result, err
}

func isScannableChartFile(path string) bool {
	base := filepath.Base(path)
	if base == "Chart.yaml" || base == "Chart.lock" || base == "values.yaml" {
		return false
	}
	switch filepath.Ext(path) {
	case ".yaml", ".yml", ".tpl":
		return true
	default:
		return false
	}
}

func scanChartValueLines(content, preferredRoot string, result map[string]string) {
	for _, line := range strings.Split(content, "\n") {
		m := chartValueLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		key, valuesPath := m[1], m[2]
		root, _, _ := strings.Cut(valuesPath, ".")

		switch {
		case preferredRoot == "":
			result[key] = valuesPath
		case root == preferredRoot:
			result[key] = valuesPath
		default:
			if _, claimed := result[key]; !claimed {
				result[key] = valuesPath
			}
		}
	}
}
