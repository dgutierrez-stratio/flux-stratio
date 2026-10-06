package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Stratio/flux-stratio/internal/config"
	"github.com/Stratio/flux-stratio/internal/diff"
	"github.com/Stratio/flux-stratio/internal/log"
	"github.com/Stratio/flux-stratio/internal/tenantfile"
)

// reportChartReview prints everything a chart-mode diff/migrate leaves out
// of its patch, so none of it is lost silently, laid out by what the
// operator has to do about it (see renderReview).
func reportChartReview(logger *log.Logger, r chartReview) {
	st := styleFor(logger)
	logger.Block(renderReview(append(r.sections(st), r.Extra...), st))
}

// sections builds the sections for what r holds, each one table. Findings of
// one workload don't repeat its name on every row. With -v every table of
// variables gains a WHY column, in the same format, saying why each one is
// there.
func (r chartReview) sections(st reviewStyle) []reviewSection {
	one := r.singleWorkload()
	name := func(workload, n string) string {
		if workload == "" || one {
			return n
		}
		return workload + "/" + n
	}
	// table is a section's header and rows, with the WHY column added when
	// -v asked for it: whys run parallel to rows.
	table := func(header []string, rows [][]string, whys []string) ([]string, [][]string) {
		if !st.Verbose || len(whys) == 0 {
			return header, rows
		}
		for i := range rows {
			rows[i] = append(rows[i], whys[i])
		}
		return append(header, "WHY"), rows
	}
	var out []reviewSection

	if len(r.Missing) > 0 {
		rows := make([][]string, len(r.Missing))
		for i, w := range r.Missing {
			rows[i] = []string{w}
		}
		out = append(out, reviewSection{
			Level: levelReview, Title: fmt.Sprintf("%d rendered workload(s) not found in the %s", len(r.Missing), r.Source),
			Hint: "Nothing of them is carried into the patch.", Rows: rows,
		})
	}

	if len(r.Unmapped) > 0 {
		rows := make([][]string, len(r.Unmapped))
		whys := make([]string, len(r.Unmapped))
		for i, u := range r.Unmapped {
			gitops, live := st.pair(u.Rendered, u.Live)
			rows[i] = []string{name(u.Workload, u.Name), quoted(gitops), quoted(live)}
			whys[i] = unmappedWhy(u)
		}
		header, rows := table([]string{"NAME", "GITOPS", "LIVE"}, rows, whys)
		hint := "The patch leaves them as GitOps renders them. If the live value is the right one, set it by hand."
		if !st.Verbose {
			hint += " -v says why for each."
		}
		out = append(out, reviewSection{
			Level: levelReview, Title: fmt.Sprintf("%d variable(s) differ and can't be written to the patch", len(r.Unmapped)),
			Hint: hint, Header: header, Rows: rows,
		})
	}

	if len(r.LiveOnly) > 0 {
		rows := make([][]string, len(r.LiveOnly))
		whys := make([]string, len(r.LiveOnly))
		for i, v := range r.LiveOnly {
			rows[i] = []string{name(v.Workload, v.Name), quoted(v.Live)}
			whys[i] = "not rendered by the chart"
			if v.ShadowedBy != "" {
				whys[i] = "the chart's default is hidden by the env list at " + v.ShadowedBy
			}
		}
		header, rows := table([]string{"NAME", "LIVE VALUE"}, rows, whys)
		out = append(out, reviewSection{
			Level: levelReview, Title: fmt.Sprintf("%d variable(s) in the %s aren't rendered by the chart", len(r.LiveOnly), r.Source),
			Hint: "Their values are lost after migration.", Header: header, Rows: rows,
		})
	}

	out = append(out, r.carriedSections()...)
	out = append(out, r.specSections(st)...)

	if len(r.Excluded) > 0 {
		rows := make([][]string, len(r.Excluded))
		whys := make([]string, len(r.Excluded))
		for i, e := range r.Excluded {
			gitops, live := st.pair(e.Rendered, e.Live)
			rows[i] = []string{name(e.Workload, e.Name), quoted(gitops), quoted(live)}
			whys[i] = "excluded by the catalog: " + e.Path
		}
		header, rows := table([]string{"NAME", "GITOPS", "LIVE"}, rows, whys)
		out = append(out, reviewSection{
			Level: levelNote, Title: fmt.Sprintf("%d value(s) excluded by the catalog", len(r.Excluded)),
			Hint: "The GitOps value stays on purpose: the catalog says it is authoritative.", Header: header, Rows: rows,
		})
	}
	return out
}

// singleWorkload reports whether every finding is about one workload (or
// none): its name is then in the section's context, not on each row.
func (r chartReview) singleWorkload() bool {
	seen := map[string]bool{}
	for _, u := range r.Unmapped {
		seen[u.Workload] = true
	}
	for _, v := range r.LiveOnly {
		seen[v.Workload] = true
	}
	for _, e := range r.Excluded {
		seen[e.Workload] = true
	}
	delete(seen, "")
	return len(seen) <= 1
}

// unmappedWhy says why a difference couldn't be written to the patch. For a
// variable with no single path, that is what the chart's line for it is
// made of: a value nothing sets, one that follows the release's name, or one
// built from other values.
func unmappedWhy(u diff.UnmappedDiff) string {
	switch u.Reason {
	case diff.UnmappedNoPath:
		switch u.Origin {
		case diff.OriginFixed:
			return "fixed in the chart: no value sets it"
		case diff.OriginRelease:
			return "follows the release's name or namespace"
		case diff.OriginComposed:
			if len(u.Sources) == 0 {
				return "built by a helper this scan couldn't follow"
			}
			return "built from " + compressPaths(u.Sources)
		}
		return "no chart config file the scan reads defines it"
	case diff.UnmappedAmbiguous:
		return "could be set by " + strings.Join(u.Candidates, ", ")
	case diff.UnmappedNotReproduced:
		return "its path, " + strings.Join(u.Candidates, ", ") + ", doesn't reproduce it"
	case diff.UnmappedConflict:
		s := "its path, " + strings.Join(u.Candidates, ", ")
		if len(u.Shared) > 0 {
			s += ", also sets " + strings.Join(u.Shared, ", ")
		}
		return s
	case diff.UnmappedLiveUnknown:
		return "the live value is a Secret or couldn't be read"
	case diff.UnmappedInline:
		return "set in the container's own env, not through values"
	}
	return string(u.Reason)
}

// carriedSections says which containers' live resources the patch carries.
func (r chartReview) carriedSections() []reviewSection {
	if len(r.Carried) == 0 {
		return nil
	}
	var rows [][]string
	var paths []string
	for _, e := range r.Carried {
		container := e.Workload
		if e.Container != "" && e.Container != e.Workload {
			container += "/" + e.Container
		}
		for _, d := range e.Diffs {
			rows = append(rows, []string{container, strings.TrimPrefix(d.Field, "resources."), orUnset(d.Live), orUnset(d.Rendered)})
		}
		paths = append(paths, e.Path)
	}
	label := "Patch path: "
	if len(paths) > 1 {
		label = "Patch paths: "
	}
	return []reviewSection{{
		Level: levelNote, Title: fmt.Sprintf("legacy resources of %d container(s) carried into the patch", len(r.Carried)),
		Hint:   "So the patch doesn't change them silently. " + label + strings.Join(paths, "; "),
		Header: []string{"CONTAINER", "SETTING", "LIVE", "GITOPS"}, Rows: rows,
	}}
}

func orUnset(v string) string {
	if v == "" {
		return "unset"
	}
	return v
}

// specSections splits the workload-setting differences by severity and kind:
// an immutable selector or a variable asking for more memory than its
// container's limit needs action, any other risk a review, and the rest are
// notes. Each is one table.
func (r chartReview) specSections(st reviewStyle) []reviewSection {
	var selectors, envLimits, risks, infos []diff.SpecDiff
	for _, d := range r.SpecDiffs {
		switch {
		case d.Severity == diff.SpecRisk && d.Field == diff.SelectorField:
			selectors = append(selectors, d)
		case d.Severity == diff.SpecRisk && strings.HasPrefix(d.Field, diff.EnvVsLimitField):
			envLimits = append(envLimits, d)
		case d.Severity == diff.SpecRisk:
			risks = append(risks, d)
		default:
			infos = append(infos, d)
		}
	}
	// settingTable is WORKLOAD / SETTING / GITOPS / LIVE, without the
	// WORKLOAD column when the whole review is about one workload.
	workloads := map[string]bool{}
	for _, d := range r.SpecDiffs {
		workloads[specWorkload(d)] = true
	}
	settingTable := func(list []diff.SpecDiff) ([]string, [][]string) {
		withWorkload := len(workloads) > 1
		header := []string{"SETTING", "GITOPS", "LIVE"}
		if withWorkload {
			header = append([]string{"WORKLOAD"}, header...)
		}
		rows := make([][]string, len(list))
		for i, d := range list {
			gitops, live := st.pair(d.Rendered, d.Live)
			row := []string{d.Field, orUnset(gitops), orUnset(live)}
			if withWorkload {
				row = append([]string{specWorkload(d)}, row...)
			}
			rows[i] = row
		}
		return header, rows
	}

	var out []reviewSection
	if len(selectors) > 0 {
		rows := make([][]string, len(selectors))
		for i, d := range selectors {
			rows[i] = []string{d.Workload, orUnset(shortSelector(d.Rendered)), orUnset(shortSelector(d.Live))}
		}
		out = append(out, reviewSection{
			Level: levelAction, Title: fmt.Sprintf("%d workload(s) can't be updated in place: their selector is immutable", len(selectors)),
			Hint:   "Delete the legacy workload before Flux installs the release. Only an app with a prepare step does it for you.",
			Header: []string{"WORKLOAD", "GITOPS SELECTOR", "LIVE SELECTOR"}, Rows: rows,
		})
	}
	if len(envLimits) > 0 {
		rows := make([][]string, len(envLimits))
		for i, d := range envLimits {
			rows[i] = []string{d.Workload, strings.TrimPrefix(d.Field, diff.EnvVsLimitField), d.Live, d.Rendered}
		}
		out = append(out, reviewSection{
			Level: levelAction, Title: fmt.Sprintf("%d variable(s) ask for more memory than the container's limit", len(envLimits)),
			Hint:   "The migrated pod would be OOM-killed. Raise the limit or lower the variable in the patch.",
			Header: []string{"WORKLOAD", "VARIABLE", "ASKS FOR", "LIMIT"}, Rows: rows,
		})
	}
	if len(risks) > 0 {
		header, rows := settingTable(risks)
		out = append(out, reviewSection{
			Level: levelReview, Title: fmt.Sprintf("%d workload setting(s) differ from the %s and can break the migrated workload", len(risks), r.Source),
			Hint: "Not written to the patch.", Header: header, Rows: rows,
		})
	}
	if len(infos) > 0 {
		header, rows := settingTable(infos)
		out = append(out, reviewSection{
			Level: levelNote, Title: fmt.Sprintf("%d other workload setting(s) differ from the %s", len(infos), r.Source),
			Header: header, Rows: rows,
		})
	}
	return out
}

// specWorkload names the workload (and container, when it isn't the same
// name) a setting belongs to.
func specWorkload(d diff.SpecDiff) string {
	if d.Container != "" && d.Container != d.Workload {
		return d.Workload + "/" + d.Container
	}
	return d.Workload
}

// shortSelector drops the standard label prefix, which every key shares.
func shortSelector(s string) string {
	return strings.ReplaceAll(s, "app.kubernetes.io/", "")
}

// quoted shows a value as it is, or says it is empty.
func quoted(v string) string {
	if v == "" {
		return "(empty)"
	}
	return v
}

// fluxManagedSection warns that the live object is already managed by
// Flux: it may no longer hold the legacy values.
func fluxManagedSection(app config.App, managedBy string) []reviewSection {
	if managedBy == "" {
		return nil
	}
	return []reviewSection{{
		Level: levelReview, Title: fmt.Sprintf("live %s/%s is already managed by Flux (%s)", app.LiveNamespace(), app.LiveName(), managedBy),
		Hint: "It may no longer hold the legacy values, so a patch computed from it can miss them: compare against a backup taken before cutover instead (--baseline latest, see apps backup).",
	}}
}

// sizeSection warns when the tenant file's size isn't the one the legacy
// application reports: the GitOps size overlay it is rendered with then
// differs from what ran. Nothing is inferred or rewritten.
func sizeSection(repos config.RepoPaths, cluster, tenant, app, liveSize string) []reviewSection {
	if liveSize == "" {
		return nil
	}
	doc, err := tenantfile.Load(tenantfile.Path(repos.Fleet, cluster, tenant))
	if err != nil {
		return nil // the diff already read the tenant file; nothing useful to add
	}
	size := doc.DefaultSize()
	if size == "" || size == liveSize {
		return nil
	}
	return []reviewSection{{
		Level: levelReview, Title: fmt.Sprintf("tenant size %s, but live %s reports CUSTOMER_SIZE=%s", size, app, liveSize),
		Hint: "The overlay sizing the workload is rendered with will differ from what ran.",
	}}
}

// droppedSection warns that replacing the app's existing patch with the
// computed one loses what only the existing one sets.
func droppedSection(dropped []string) []reviewSection {
	if len(dropped) == 0 {
		return nil
	}
	rows := make([][]string, len(dropped))
	for i, p := range dropped {
		rows[i] = []string{p}
	}
	return []reviewSection{{
		Level: levelReview, Title: fmt.Sprintf("the patch replaces the tenant file's existing one and drops %d path(s)", len(dropped)),
		Hint:   "Only the existing patch sets them: a hand edit, or a value this diff cannot see.",
		Header: []string{"DROPPED PATH"}, Rows: rows,
	}}
}

// unresolvedDepsSection lists the dependencies naming an entry the tenant
// file doesn't declare. The patch is still written (the missing entry may be
// added in the same change), but Flux holds the app back until the name is fixed.
func unresolvedDepsSection(app config.App, deps []tenantfile.UnresolvedDependency) []reviewSection {
	if len(deps) == 0 {
		return nil
	}
	rows := make([][]string, len(deps))
	for i, d := range deps {
		declared := "none declared"
		if len(d.Declared) > 0 {
			declared = strings.Join(d.Declared, ", ")
		}
		rows[i] = []string{d.Key, d.Name, declared}
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a][0] < rows[b][0] })
	return []reviewSection{{
		Level: levelReview, Title: fmt.Sprintf("%s depends on %d entr(y/ies) the tenant file doesn't declare", app.Object, len(deps)),
		Hint:   fmt.Sprintf("Flux holds %s back until each name is fixed.", app.Kustomization),
		Header: []string{"DEPENDENCY", "NAME", "DECLARED"}, Rows: rows,
	}}
}

// legacyAgentSection warns of gosec-agent patches the legacy client wrote
// to the parent entry's patches, where nothing reads them.
func legacyAgentSection(app config.App, n int) []reviewSection {
	if n == 0 {
		return nil
	}
	return []reviewSection{{
		Level: levelReview, Title: fmt.Sprintf("%d patch(es) for %s sit in the parent entry's patches and are ignored", n, app.Object),
		Hint: "The legacy client put them there. The agent reads config.agent.patches: move or remove them.",
	}}
}

// compressPaths writes dot paths compactly: a path that another is under is
// dropped (its children say more), and paths sharing a parent are grouped as
// "parent.(a, b, c)".
func compressPaths(paths []string) string {
	var kept []string
	for _, p := range paths {
		parent := false
		for _, q := range paths {
			if strings.HasPrefix(q, p+".") {
				parent = true
				break
			}
		}
		if !parent {
			kept = append(kept, p)
		}
	}
	var order []string
	children := map[string][]string{}
	for _, p := range kept {
		dir, leaf := "", p
		if i := strings.LastIndex(p, "."); i >= 0 {
			dir, leaf = p[:i], p[i+1:]
		}
		if _, ok := children[dir]; !ok {
			order = append(order, dir)
		}
		children[dir] = append(children[dir], leaf)
	}
	parts := make([]string, 0, len(order))
	for _, dir := range order {
		leaves := children[dir]
		switch {
		case dir == "":
			parts = append(parts, strings.Join(leaves, ", "))
		case len(leaves) == 1:
			parts = append(parts, dir+"."+leaves[0])
		default:
			parts = append(parts, dir+".("+strings.Join(leaves, ", ")+")")
		}
	}
	return strings.Join(parts, ", ")
}
