package catalog

import (
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// defaultTail is the optional `| default` following a config read: a
// quoted string (group 2) or a bare token (group 3: false, true, a number,
// dict or list; anything else is not taken for a default). Group 1 is the
// read's key.
const defaultTail = `(?:\s*\|\s*default\s+(?:"([^"]*)"|(false|true|-?\d+(?:\.\d+)?|dict|list)\b))?`

var (
	depRe         = regexp.MustCompile(`\$(\w+Dependency)\s*:=\s*get\s+\$dependencies\s+"([^"]+)"`)
	depVarTokenRe = regexp.MustCompile(`\$(\w+Dependency)\b`)
	// gatedRe finds a read of a dependency's `enabled`; group 1 is the
	// template variable depRe bound to the dependency key.
	gatedRe = regexp.MustCompile(`\$(\w+Dependency)\s+"enabled"`)
	// blockOpenRe and blockCloseRe count the template actions that open and
	// close a block, to find where an `if ... "enabled"` ends.
	blockOpenRe  = regexp.MustCompile(`<<-?\s*(?:if|range|with|define|block)\b`)
	blockCloseRe = regexp.MustCompile(`<<-?\s*end\b`)
	waitItemRe   = regexp.MustCompile(`^\s*-\s+name:\s+apps-`)
	kindKsRe     = regexp.MustCompile(`^\s*kind:\s*Kustomization\s*$`)
	nameLineRe   = regexp.MustCompile(`^\s*name:\s*(\S.*?)\s*$`)
	actionRe     = regexp.MustCompile(`<<[^>]*>>`)
	storageIfRe  = regexp.MustCompile(`(?s)<<-?\s*if\s+eq\s+\$storageType\s+"([^"]+)"\s*>>(.*?)<<-?\s*end\s*>>`)
	// extraConfigRe finds a read of a config field and its `| default`
	// (see defaultTail).
	extraConfigRe = regexp.MustCompile(`get\s+\$componentConfig\s+"([^"]+)"` + defaultTail)

	// storageElseRe splits a storage-type branch at its `else`.
	storageElseRe = regexp.MustCompile(`<<-?\s*else\s*>>`)

	// hasKeyDefaultRe finds the `hasKey … else <literal>` form of a default:
	// `<< if hasKey $componentConfig "k" >><< get $componentConfig "k" >><< else >>false<< end >>`.
	// The default is the literal between else and end. The key is
	// compared with the get's in code (RE2 has no backreferences).
	hasKeyDefaultRe = regexp.MustCompile(`<<-?\s*if\s+hasKey\s+\$componentConfig\s+"([^"]+)"\s*>>\s*<<-?\s*get\s+\$componentConfig\s+"([^"]+)"[^>]*>>\s*<<-?\s*else\s*>>([^<]*)<<-?\s*end\s*>>`)

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
	// dependsOn cross-reference. extractLeaves also reads it, for the keys
	// the template reads off the variable (genai's llmModels).
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
	varKey := map[string]string{} // template variable -> dependency key
	for _, m := range depRe.FindAllStringSubmatch(block, -1) {
		depKey := m[2]
		varKey[m[1]] = depKey
		if seenDep[depKey] {
			continue
		}
		seenDep[depKey] = true
		s.Dependencies = append(s.Dependencies, Dependency{Key: depKey})
	}

	gatedVars := map[string]bool{}
	for _, m := range gatedRe.FindAllStringSubmatch(block, -1) {
		if depKey, ok := varKey[m[1]]; ok && !gatedVars[m[1]] {
			gatedVars[m[1]] = true
			for i := range s.Dependencies {
				if s.Dependencies[i].Key == depKey {
					s.Dependencies[i].Gated = true
					s.Dependencies[i].Adds = gateAdds(block, m[1])
				}
			}
		}
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

	// Reads of each field in total, and per storage type inside the part of
	// an `if eq $storageType` branch that precedes its else: a field every
	// read of which is so conditioned only applies to those types. A read
	// after an else, or past a nested `end` the lazy match stops at, counts
	// as unconditional — the safe side: the field is then still reported.
	totalReads := map[string]int{}
	for _, m := range extraConfigRe.FindAllStringSubmatch(block, -1) {
		totalReads[m[1]]++
	}
	condReads := map[string]map[string]int{} // field -> storage type -> reads
	for _, m := range storageIfRe.FindAllStringSubmatch(block, -1) {
		then := storageElseRe.Split(m[2], 2)[0]
		for _, fm := range extraConfigRe.FindAllStringSubmatch(then, -1) {
			if condReads[fm[1]] == nil {
				condReads[fm[1]] = map[string]int{}
			}
			condReads[fm[1]][m[1]]++
		}
	}

	seenExtra := map[string]bool{}
	for _, idx := range extraConfigRe.FindAllStringSubmatchIndex(block, -1) {
		fieldKey := block[idx[2]:idx[3]]
		if extraConfigIgnore[fieldKey] || seenExtra[fieldKey] {
			continue
		}
		seenExtra[fieldKey] = true
		field := fieldFromMatch(block, idx)
		if n := totalReads[fieldKey]; n > 0 {
			conditioned := 0
			for _, c := range condReads[fieldKey] {
				conditioned += c
			}
			if conditioned == n {
				for t := range condReads[fieldKey] {
					field.StorageTypes = append(field.StorageTypes, t)
				}
				slices.Sort(field.StorageTypes)
			}
		}
		s.ExtraConfig = append(s.ExtraConfig, field)
	}

	for _, m := range hasKeyDefaultRe.FindAllStringSubmatch(block, -1) {
		if m[1] != m[2] {
			continue
		}
		for i := range s.ExtraConfig {
			f := &s.ExtraConfig[i]
			if f.Key != m[1] || f.HasDefault {
				continue
			}
			literal := strings.TrimSpace(m[3])
			f.HasDefault = true
			if unquoted, ok := unquote(literal); ok {
				f.Default = unquoted
			} else {
				f.Default, f.Bare = literal, true
			}
		}
	}

	extractLeaves(block, s.ExtraConfig)

	s.Kustomizations, s.ChartName = extractKustomizations(block)
	s.SourcePaths = extractSourcePaths(block)
	return s
}

// fieldFromMatch builds the field one extraConfigRe (or leaf regex) match
// describes: its key and its `| default`, if any (see defaultTail).
func fieldFromMatch(block string, idx []int) ExtraConfigField {
	field := ExtraConfigField{Key: block[idx[2]:idx[3]]}
	switch {
	case idx[4] >= 0: // a quoted default, possibly ""
		field.HasDefault, field.Default = true, block[idx[4]:idx[5]]
	case idx[6] >= 0:
		bare := block[idx[6]:idx[7]]
		if bare == "dict" || bare == "list" {
			field.Container = bare
		} else {
			field.HasDefault, field.Default, field.Bare = true, bare, true
		}
	}
	return field
}

// extractLeaves fills the Leaves of every dict field assigned to a variable
// (`$llmModels := get $componentConfig "llmModels" | default dict`) with
// the keys the block reads off that variable (`get $llmModels "chat" |
// default ""`), in first-read order. One level deep: a leaf's own reads
// are not followed.
func extractLeaves(block string, fields []ExtraConfigField) {
	for _, m := range agentAssignRe.FindAllStringSubmatch(block, -1) {
		variable, key := m[1], m[2]
		for i := range fields {
			f := &fields[i]
			if f.Key != key || f.Container != "dict" || f.Leaves != nil {
				continue
			}
			leafRe := regexp.MustCompile(`get\s+\$` + regexp.QuoteMeta(variable) + `\s+"([^"]+)"` + defaultTail)
			seen := map[string]bool{}
			for _, idx := range leafRe.FindAllStringSubmatchIndex(block, -1) {
				leaf := fieldFromMatch(block, idx)
				if seen[leaf.Key] {
					continue
				}
				seen[leaf.Key] = true
				f.Leaves = append(f.Leaves, leaf)
			}
		}
	}
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

// unquote strips one pair of matching double or single quotes.
func unquote(s string) (string, bool) {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1], true
	}
	return s, false
}

// gateAdds reads what turning on the dependency bound to template variable
// v changes, from the template text: the substitution variable that carries
// its `enabled` into the chart (`DISCOVERY_ENABLED: "<< get $v "enabled" ... >>"`)
// and, inside each `if get $v "enabled"` block, a dependsOn item and a
// Kustomization it creates. They are hints for the operator, not a model of the
// ResourceSet: a shape it does not recognise adds nothing.
func gateAdds(block, v string) []string {
	var adds []string
	add := func(a string) {
		if !slices.Contains(adds, a) {
			adds = append(adds, a)
		}
	}

	subst := regexp.MustCompile(`(?m)^\s*(\w+):\s*"?<<-?\s*get\s+\$` + regexp.QuoteMeta(v) + `\s+"enabled"`)
	for _, m := range subst.FindAllStringSubmatch(block, -1) {
		add("sets " + m[1] + " in the app's chart values")
	}

	open := regexp.MustCompile(`<<-?\s*if\s+get\s+\$` + regexp.QuoteMeta(v) + `\s+"enabled"`)
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if !open.MatchString(line) {
			continue
		}
		depth := 1
		for j := i + 1; j < len(lines) && depth > 0; j++ {
			l := lines[j]
			depth += len(blockOpenRe.FindAllString(l, -1)) - len(blockCloseRe.FindAllString(l, -1))
			if depth <= 0 {
				break
			}
			switch {
			case waitItemRe.MatchString(l):
				add("makes the app wait for the dependency (dependsOn)")
			case kindKsRe.MatchString(l):
				for k := j + 1; k < len(lines) && k <= j+8; k++ {
					if m := nameLineRe.FindStringSubmatch(lines[k]); m != nil && strings.HasPrefix(m[1], "apps-") {
						add("creates the Kustomization " + actionRe.ReplaceAllString(m[1], "*"))
						break
					}
				}
			}
		}
	}
	return adds
}
