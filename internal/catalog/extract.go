package catalog

import (
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	depRe         = regexp.MustCompile(`\$(\w+Dependency)\s*:=\s*get\s+\$dependencies\s+"([^"]+)"`)
	depVarTokenRe = regexp.MustCompile(`\$(\w+Dependency)\b`)
	storageIfRe   = regexp.MustCompile(`(?s)<<-?\s*if\s+eq\s+\$storageType\s+"([^"]+)"\s*>>(.*?)<<-?\s*end\s*>>`)
	extraConfigRe = regexp.MustCompile(`get\s+\$componentConfig\s+"([^"]+)"(?:\s*\|\s*default\s+"([^"]*)")?`)

	docSepRe    = regexp.MustCompile(`(?m)^\s*---\s*$`)
	nameRe      = regexp.MustCompile(`name:\s*apps-(\S.*)`)
	chartPathRe = regexp.MustCompile(`path:\s*(?:\./)?components/([^/\s]+)/app/`)

	patchesEmptyRe   = regexp.MustCompile(`patches:\s*\[\]`)
	patchesDefaultRe = regexp.MustCompile(`patches:\s*<<\s*get\s+\$component\s+"patches"`)
	patchesNestedRe  = regexp.MustCompile(`patches:\s*<<\s*get\s+\$(\w+)\s+"patches"`)

	// agentAssignRe finds a nested-config variable assignment shaped like
	// `$agentConfig := get $componentConfig "agent"` — the one nested
	// sub-config shape currently in use (see the package doc). Any
	// Kustomization in the same block whose patches come from that
	// variable is this component's own nested sub-resource, not a
	// dependsOn cross-reference.
	agentAssignRe = regexp.MustCompile(`\$(\w+)\s*:=\s*get\s+\$componentConfig\s+"(\w+)"`)

	nameDefaultRe  = regexp.MustCompile(`^<<\s*get\s+\$component\s+"name"\s*>>(-[\w-]+)?$`)
	nameVarRe      = regexp.MustCompile(`^<<\s*\$(\w+)\s*>>$`)
	printfSuffixRe = regexp.MustCompile(`\$(\w+)\s*:=\s*printf\s*"%s-([\w-]+)"\s*\(get\s+\$component\s+"name"\)`)

	healthCheckRe = regexp.MustCompile(`healthCheckExprs:\s*\n\s+-\s+apiVersion:\s*(\S+)\s*\n\s+kind:\s*(\S+)`)

	// specPathRe and sourceRefNameRe read a Kustomization's spec.path and
	// its sourceRef's name (the next `name:` after `sourceRef:`, whichever
	// order kind/name/namespace appear in).
	specPathRe      = regexp.MustCompile(`(?m)^\s*path:\s*(\S.*?)\s*$`)
	sourceRefNameRe = regexp.MustCompile(`sourceRef:\s*\n(?:\s+(?:kind|namespace):.*\n)*\s+name:\s*(\S+)`)
)

var extraConfigIgnore = map[string]bool{"size": true, "dependencies": true, "type": true}

// extractSchema parses one component block (the text of one top-level
// `range $component := $<key>` loop) into a Schema.
func extractSchema(key, block string) Schema {
	s := Schema{Key: key}

	seenDep := map[string]bool{}
	for _, m := range depRe.FindAllStringSubmatch(block, -1) {
		depKey := m[2]
		if seenDep[depKey] {
			continue
		}
		seenDep[depKey] = true
		s.Dependencies = append(s.Dependencies, Dependency{Key: depKey})
	}

	for _, m := range storageIfRe.FindAllStringSubmatch(block, -1) {
		storageType, inner := m[1], m[2]
		for _, vm := range depVarTokenRe.FindAllStringSubmatch(inner, -1) {
			depKey := strings.TrimSuffix(vm[1], "Dependency")
			for i := range s.Dependencies {
				if s.Dependencies[i].Key == depKey && !slices.Contains(s.Dependencies[i].StorageTypes, storageType) {
					s.Dependencies[i].StorageTypes = append(s.Dependencies[i].StorageTypes, storageType)
				}
			}
		}
	}

	seenExtra := map[string]bool{}
	for _, m := range extraConfigRe.FindAllStringSubmatch(block, -1) {
		fieldKey := m[1]
		if extraConfigIgnore[fieldKey] || seenExtra[fieldKey] {
			continue
		}
		seenExtra[fieldKey] = true
		field := ExtraConfigField{Key: fieldKey}
		if strings.Contains(m[0], "| default") {
			field.HasDefault = true
			field.Default = m[2]
		}
		s.ExtraConfig = append(s.ExtraConfig, field)
	}

	s.Kustomizations, s.ChartName = extractKustomizations(block)
	s.SourcePaths = extractSourcePaths(block)
	return s
}

// extractKustomizations splits block into per-document segments (on YAML
// "---" separators) and, for every segment whose `name:` field references
// this component's own instance name, records its patches anchor.
func extractKustomizations(block string) ([]KustomizationAnchor, string) {
	// nestedFieldByVar maps every `$X := get $componentConfig "Y"`
	// assignment in the block to its config key Y, keyed by variable name
	// X — a block has several (e.g. "$dependencies", "$agentConfig"), and
	// only the one a `patches:` line actually references identifies a
	// nested sub-resource; the rest are ordinary config reads. Not
	// hardcoded to "agent": any future `get $componentConfig "<key>"`
	// sub-config assignment paired with its own `patches:` line is picked
	// up the same way, with Field becoming "config.<key>".
	nestedFieldByVar := map[string]string{}
	for _, m := range agentAssignRe.FindAllStringSubmatch(block, -1) {
		nestedFieldByVar[m[1]] = m[2]
	}

	var anchors []KustomizationAnchor
	chartName := ""
	for _, segment := range docSepRe.Split(block, -1) {
		nameMatch := nameRe.FindStringSubmatch(segment)
		if nameMatch == nil {
			continue
		}
		suffix, ok := resolveSuffix(nameMatch[1], block)
		if !ok {
			// This Kustomization's name doesn't reference the component's
			// own instance at all (a dependsOn cross-reference, or a
			// file-level static Kustomization) — not ours to classify.
			continue
		}

		anchor := KustomizationAnchor{Suffix: suffix}
		switch {
		case patchesEmptyRe.MatchString(segment):
			anchor.Kind = AnchorNotPatchable
		case patchesDefaultRe.MatchString(segment):
			anchor.Kind = AnchorDefault
		default:
			anchor.Kind = AnchorUnknown
			if pm := patchesNestedRe.FindStringSubmatch(segment); pm != nil {
				if field, ok := nestedFieldByVar[pm[1]]; ok {
					anchor.Kind = AnchorNested
					anchor.Field = "config." + field
				}
			}
		}
		anchors = append(anchors, anchor)

		if suffix == "" {
			if cm := chartPathRe.FindStringSubmatch(segment); cm != nil {
				chartName = cm[1]
			}
		}
	}
	return anchors, chartName
}

// resolveSuffix turns a Kustomization's name-template expression (the text
// following "apps-" on its `name:` line, e.g. `<< get $component "name"
// >>-postrequisites` or `<< $agentName >>`) into the suffix appended to
// the component's own instance name, or ("", false) when the expression
// doesn't reference this component's own instance at all.
func resolveSuffix(nameExpr, block string) (string, bool) {
	nameExpr = strings.TrimSpace(nameExpr)
	if m := nameDefaultRe.FindStringSubmatch(nameExpr); m != nil {
		return m[1], true
	}
	if m := nameVarRe.FindStringSubmatch(nameExpr); m != nil {
		varName := m[1]
		for _, pm := range printfSuffixRe.FindAllStringSubmatch(block, -1) {
			if pm[1] == varName {
				return "-" + pm[2], true
			}
		}
	}
	return "", false
}

// extractCRDs finds every healthCheckExprs entry in block and returns the
// CRD plural(s) it derives, mapped to their GVK and compKey.
func extractCRDs(block, compKey string) map[string]CRDInfo {
	out := map[string]CRDInfo{}
	for _, m := range healthCheckRe.FindAllStringSubmatch(block, -1) {
		apiVersion, kind := m[1], m[2]
		group, version, _ := strings.Cut(apiVersion, "/")
		plural := deriveCRDPlural(strings.ToLower(kind), group)
		out[plural] = CRDInfo{
			GVK:          schema.GroupVersionKind{Group: group, Version: version, Kind: kind},
			ComponentKey: compKey,
		}
	}
	return out
}

// deriveCRDPlural pluralizes a lower-cased CRD kind and appends its API
// group, following the same rules Kubernetes' own CRD naming convention
// (and the Python client's _derive_crd_plural) uses: kinds ending in "s"
// get "es" (OSDashboards -> osdashboardses); kinds ending in a consonant
// + "y" replace "y" with "ies" (PGBackupRepository -> pgbackuprepositories);
// everything else just gets "s".
func deriveCRDPlural(kindLower, group string) string {
	var plural string
	switch {
	case strings.HasSuffix(kindLower, "s"):
		plural = kindLower + "es"
	case strings.HasSuffix(kindLower, "y") && len(kindLower) >= 2 && isConsonant(rune(kindLower[len(kindLower)-2])):
		plural = kindLower[:len(kindLower)-1] + "ies"
	default:
		plural = kindLower + "s"
	}
	return plural + "." + group
}

func isConsonant(r rune) bool {
	switch r {
	case 'a', 'e', 'i', 'o', 'u':
		return false
	default:
		return true
	}
}

// extractSourcePaths returns the spec.path and sourceRef of every
// document in block that declares both — every Kustomization the
// component's template renders, including its postrequisites. A document
// with a path but no sourceRef (an HTTPRoute, a probe) isn't one.
func extractSourcePaths(block string) []SourcePath {
	var paths []SourcePath
	for _, segment := range docSepRe.Split(block, -1) {
		pm := specPathRe.FindStringSubmatch(segment)
		sm := sourceRefNameRe.FindStringSubmatch(segment)
		if pm == nil || sm == nil {
			continue
		}
		paths = append(paths, SourcePath{Source: sm[1], Path: pm[1]})
	}
	return paths
}
