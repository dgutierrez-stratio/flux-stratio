package diff

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// NoPathOrigin says why a chart's env var has no single .Values path: what
// its line in the chart's env file is made of.
type NoPathOrigin string

const (
	// OriginUnknown means no chart file holds a line for the variable (it
	// comes from a template body, or a file this scan doesn't read).
	OriginUnknown NoPathOrigin = ""
	// OriginFixed means the line has no template action: the chart writes
	// the value itself, and nothing parameterises it.
	OriginFixed NoPathOrigin = "fixed"
	// OriginRelease means the line only reads the release's own identity
	// (.Release.Name, .Release.Namespace ...): it changes with the
	// HelmRelease's name or namespace, not with any value.
	OriginRelease NoPathOrigin = "release"
	// OriginComposed means the line is built from other values, directly or
	// through helpers: there is a knob, but no single one.
	OriginComposed NoPathOrigin = "composed"
)

// ChartOrigin is what a chart env line is made of, as classifyChartLine
// found it.
type ChartOrigin struct {
	Kind NoPathOrigin
	// Values are the .Values paths the line reads, directly or through the
	// helpers it includes (OriginComposed only), sorted.
	Values []string
}

var (
	templateActionRe = regexp.MustCompile(`\{\{-?(.*?)-?\}\}`)
	valuesRefRe      = regexp.MustCompile(`\.Values\.([A-Za-z_][\w]*(?:\.[A-Za-z_][\w]*)*)`)
	includeRefRe     = regexp.MustCompile(`(?:include|template)\s+"([^"]+)"`)
	releaseRefRe     = regexp.MustCompile(`\.(?:Release|Chart)\.\w+`)
	defineRe         = regexp.MustCompile(`\{\{-?\s*define\s+"([^"]+)"\s*-?\}\}`)
)

// classifyChartLine says what an env file line's value (the text after
// `KEY:`) is made of. defines are the chart's named templates (see
// ScanChartDefines), which an include is followed into for the values it
// reads; one that isn't there contributes nothing.
func classifyChartLine(raw string, defines map[string]string) ChartOrigin {
	actions := templateActionRe.FindAllStringSubmatch(raw, -1)
	if len(actions) == 0 {
		return ChartOrigin{Kind: OriginFixed}
	}
	values := map[string]bool{}
	release, helpers := false, false
	for _, a := range actions {
		for _, m := range valuesRefRe.FindAllStringSubmatch(a[1], -1) {
			values[m[1]] = true
		}
		for _, m := range includeRefRe.FindAllStringSubmatch(a[1], -1) {
			helpers = true
			for v := range helperValues(m[1], defines, map[string]bool{}) {
				values[v] = true
			}
		}
		release = release || releaseRefRe.MatchString(a[1])
	}
	if len(values) == 0 && !helpers {
		if release {
			return ChartOrigin{Kind: OriginRelease}
		}
		return ChartOrigin{Kind: OriginFixed} // an action that only writes a literal
	}
	return ChartOrigin{Kind: OriginComposed, Values: sortedKeys(values)}
}

// helperValues are the .Values paths the named template reads, following
// the templates it includes in turn.
func helperValues(name string, defines map[string]string, seen map[string]bool) map[string]bool {
	out := map[string]bool{}
	body, ok := defines[name]
	if !ok || seen[name] {
		return out
	}
	seen[name] = true
	for _, m := range valuesRefRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	for _, m := range includeRefRe.FindAllStringSubmatch(body, -1) {
		for v := range helperValues(m[1], defines, seen) {
			out[v] = true
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ScanChartDefines collects the named templates (`define "name"`) of the
// chart's .tpl files and of the library charts it takes from a local
// directory (Chart.yaml dependencies with a file:// repository), each name
// mapped to its body — the text up to the next define, which over-reaches
// harmlessly. Best-effort: an unreadable file contributes nothing.
func ScanChartDefines(chartPath string) map[string]string {
	defines := map[string]string{}
	dirs := []string{chartPath}
	for _, rel := range localDependencies(chartPath) {
		dirs = append(dirs, filepath.Join(chartPath, rel))
	}
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Ext(path) != ".tpl" {
				return nil //nolint:nilerr // best-effort scan
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil //nolint:nilerr // best-effort scan
			}
			addDefines(defines, string(data))
			return nil
		})
	}
	return defines
}

func addDefines(defines map[string]string, content string) {
	locs := defineRe.FindAllStringSubmatchIndex(content, -1)
	for i, loc := range locs {
		end := len(content)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		defines[content[loc[2]:loc[3]]] = content[loc[1]:end]
	}
}

// localDependencies are the file:// repositories a chart's Chart.yaml
// declares, relative to the chart.
func localDependencies(chartPath string) []string {
	data, err := os.ReadFile(filepath.Join(chartPath, "Chart.yaml"))
	if err != nil {
		return nil
	}
	var chart struct {
		Dependencies []struct {
			Repository string `yaml:"repository"`
		} `yaml:"dependencies"`
	}
	if yaml.Unmarshal(data, &chart) != nil {
		return nil
	}
	var out []string
	for _, d := range chart.Dependencies {
		if rel, ok := strings.CutPrefix(d.Repository, "file://"); ok {
			out = append(out, rel)
		}
	}
	return out
}
