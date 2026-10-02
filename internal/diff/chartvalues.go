package diff

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// chartValueLineRe matches an unindented config/*_env_vars.yaml-style
// line mapping an env var name to a single .Values dot path, e.g.
// `DEPLOYMENT_ENVIRONMENT: {{ .Values.general.deploymentEnvironment | quote }}`.
//
// Only pipelines that render the value unchanged are accepted — quote,
// squote, toString, and default with a literal (which only applies when
// the value is empty; ChartDiff's re-render check catches that case). A
// transforming one (printf "https://%s", b64enc, upper) would make the
// live value the function's output, and patching it back into the path
// that feeds the function would apply it twice.
var chartValueLineRe = regexp.MustCompile(`^([A-Za-z0-9_-]+):\s*['"]?\{\{-?\s*\.Values\.([\w.]+)\s*` +
	`(?:\|\s*(?:quote|squote|toString|default\s+(?:"[^"]*"|'[^']*'|[\w.-]+))\s*)*` +
	`-?\}\}['"]?\s*$`)

// topLevelKeyRe matches an unindented `KEY:` line — one top-level key of
// a key-value env-vars file, whatever its value is.
var topLevelKeyRe = regexp.MustCompile(`^([A-Za-z0-9_-]+):`)

// ChartFile is one scannable file of a chart (see ScanChartFiles): the
// top-level keys it declares and, for those whose value is a single
// `{{ .Values.path }}` reference, that path — the map chart mode uses to
// translate a live env var's value back into a HelmRelease values patch.
type ChartFile struct {
	// Path is the file's path relative to the chart directory.
	Path string
	// Keys are the file's top-level keys, whatever their values.
	Keys map[string]bool
	// Values maps a key to its .Values dot path, for every line shaped
	// like chartValueLineRe.
	Values map[string]string
}

// ScanChartFiles scans every .yaml/.yml/.tpl file under chartPath
// (excluding Chart.yaml, Chart.lock and values.yaml) — in
// filepath.WalkDir's lexical order — for top-level keys and lines shaped
// like chartValueLineRe, returning each file that has either.
//
// The files are kept apart rather than merged into one name → path map:
// sibling workloads' env-vars files routinely reuse a name (genai's
// genai-api, genai-ui and genai-developer-proxy all set VAULT_ROLE, each
// from its own .Values root), and only the file a rendered ConfigMap came
// from (see AttributeConfigMaps) says which of those paths a given value
// belongs to. A merged map had to pick one by scan order — the Python
// client's filesystem order, flux-stratio's lexical one — and wrote the
// other sibling's value into it.
func ScanChartFiles(chartPath string) ([]ChartFile, error) {
	var files []ChartFile
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
		file := scanChartFile(string(data))
		if len(file.Keys) == 0 && len(file.Values) == 0 {
			return nil
		}
		file.Path, _ = filepath.Rel(chartPath, path)
		files = append(files, file)
		return nil
	})
	return files, err
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

func scanChartFile(content string) ChartFile {
	file := ChartFile{Keys: map[string]bool{}, Values: map[string]string{}}
	for _, line := range strings.Split(content, "\n") {
		if m := topLevelKeyRe.FindStringSubmatch(line); m != nil {
			file.Keys[m[1]] = true
		}
		// Unindented only: an indented line is a nested key (a .tpl's
		// template body), never one of a key-value env file's variables.
		if m := chartValueLineRe.FindStringSubmatch(strings.TrimRight(line, " \t\r")); m != nil {
			file.Values[m[1]] = m[2]
		}
	}
	return file
}

// AttributeConfigMaps maps each rendered ConfigMap in docs to the chart
// file it was built from — the one file (of files) whose top-level keys
// are exactly the ConfigMap's data keys, as bjw-s configMapsFromFile
// renders a key-value file into a ConfigMap. When several files have the
// same keys (flavors of one multi-flavor chart), the one whose .Values
// paths all start with preferredRoot wins; if that still doesn't single
// one out, the ConfigMap is left unattributed, and its values fall back
// to the chart-wide lookup in pathsFor.
//
// Matching on keys rather than on the ConfigMap's name keeps this
// independent of the chart library's naming rules (release-name prefix,
// templated names, templated file paths picking a flavor's file).
func AttributeConfigMaps(docs []*unstructured.Unstructured, files []ChartFile, preferredRoot string) map[string]*ChartFile {
	out := map[string]*ChartFile{}
	for _, d := range docs {
		if d.GetKind() != "ConfigMap" {
			continue
		}
		data, _, _ := unstructured.NestedStringMap(d.Object, "data")
		if len(data) == 0 {
			continue
		}
		var matches []*ChartFile
		for i := range files {
			if sameKeys(files[i].Keys, data) {
				matches = append(matches, &files[i])
			}
		}
		if len(matches) > 1 && preferredRoot != "" {
			matches = filterByRoot(matches, preferredRoot)
		}
		if len(matches) == 1 {
			out[d.GetName()] = matches[0]
		}
	}
	return out
}

func sameKeys(keys map[string]bool, data map[string]string) bool {
	if len(keys) != len(data) {
		return false
	}
	for k := range data {
		if !keys[k] {
			return false
		}
	}
	return true
}

func filterByRoot(files []*ChartFile, root string) []*ChartFile {
	var out []*ChartFile
	for _, f := range files {
		all := true
		for _, p := range f.Values {
			if valuesRoot(p) != root {
				all = false
				break
			}
		}
		if all {
			out = append(out, f)
		}
	}
	return out
}

func valuesRoot(path string) string {
	root, _, _ := strings.Cut(path, ".")
	return root
}

// candidatePaths is every distinct .Values path any of files maps key to,
// sorted — narrowed to those under preferredRoot when any are (multi-flavor
// charts like bdl-datarest, whose flavors' files share one values tree
// with different top-level roots).
func candidatePaths(files []ChartFile, key, preferredRoot string) []string {
	seen := map[string]bool{}
	for _, f := range files {
		if p, ok := f.Values[key]; ok {
			seen[p] = true
		}
	}
	var all, preferred []string
	for p := range seen {
		all = append(all, p)
		if preferredRoot != "" && valuesRoot(p) == preferredRoot {
			preferred = append(preferred, p)
		}
	}
	if len(preferred) > 0 {
		all = preferred
	}
	sort.Strings(all)
	return all
}
