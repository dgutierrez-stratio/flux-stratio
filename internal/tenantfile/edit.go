package tenantfile

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The additions below are made on the file's text, not on the node tree:
// yaml.v3 re-encodes a document with its own indentation (it indents the
// "- " of a sequence under its key, where tenant files conventionally don't),
// so re-encoding would rewrite lines the operator wrote, and `git diff`
// of a completed file would stop being a list of added lines. The node tree
// is only read, for where to insert and what is already there. Each method
// records an insertion; Bytes then returns the original text plus them.
//
// Insertions are pure additions of a key, a dependency or an entry: what is
// already there — value, position, comments — is never touched. The node
// tree does not see them, so each entry (or the top level) is edited by one
// call per run.

// textEdit is lines inserted after line `after` (1-based; 0 is the top of
// the file). Edits at the same line keep the order they were recorded in.
type textEdit struct {
	after int
	text  string
}

// ComponentEntry is one active (uncommented) components.<Key>[] entry.
type ComponentEntry struct {
	Key  string
	Name string
	Node *yaml.Node
}

// Field is one key to add and its value.
type Field struct {
	Key   string
	Value any
}

// DependencyRef is one config.dependencies.<Key> to add: its name, and
// `enabled: true` when Enabled.
type DependencyRef struct {
	Key, Name string
	Enabled   bool
}

// ActiveEntries returns every uncommented components entry that has a name,
// in file order. A commented-out block is not an entry: see CommentedOut.
func (d *Doc) ActiveEntries() ([]ComponentEntry, error) {
	components, err := d.components()
	if err != nil {
		return nil, err
	}
	var out []ComponentEntry
	if components.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(components.Content); i += 2 {
		seq := components.Content[i+1]
		if seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, entry := range seq.Content {
			if name := mapGet(entry, "name"); name != nil && name.Value != "" {
				out = append(out, ComponentEntry{Key: components.Content[i].Value, Name: name.Value, Node: entry})
			}
		}
	}
	return out, nil
}

// ConfigValue returns entry.config.<key>'s node, or nil when absent.
func ConfigValue(entry *yaml.Node, key string) *yaml.Node {
	return mapGet(mapGet(entry, "config"), key)
}

// HasDependency reports whether entry.config.dependencies declares depKey,
// whatever its name (even an empty one: the operator's value wins).
func HasDependency(entry *yaml.Node, depKey string) bool {
	return mapGet(ConfigValue(entry, "dependencies"), depKey) != nil
}

// DependencyField returns entry.config.dependencies.<depKey>.<field>'s node,
// or nil when absent.
func DependencyField(entry *yaml.Node, depKey, field string) *yaml.Node {
	return mapGet(mapGet(mapGet(mapGet(entry, "config"), "dependencies"), depKey), field)
}

// HasTopLevel reports whether spec.defaultValues.<key> is set.
func (d *Doc) HasTopLevel(key string) bool {
	return mapGet(mapGet(mapGet(documentRoot(d.root), "spec"), "defaultValues"), key) != nil
}

// AddToEntry adds fields to entry.config and deps to
// entry.config.dependencies, creating either when absent. The caller has
// checked none of them is declared yet. A config (or dependencies) written
// in flow style ({...}) cannot be extended textually and is an error.
func (d *Doc) AddToEntry(entry *yaml.Node, fields []Field, deps []DependencyRef) error {
	if len(fields) == 0 && len(deps) == 0 {
		return nil
	}
	if entry.Kind != yaml.MappingNode {
		return fmt.Errorf("component entry is not a mapping")
	}
	entryIndent := entry.Column - 1

	configKey, config := mapPair(entry, "config")
	switch {
	case config == nil:
		var b strings.Builder
		b.WriteString(indent(entryIndent) + "config:\n")
		writeConfigBody(&b, entryIndent+2, fields, deps)
		d.insert(lastLine(entry), b.String())
		return nil
	case config.Kind == yaml.ScalarNode && config.Tag == "!!null":
		var b strings.Builder
		writeConfigBody(&b, configKey.Column-1+2, fields, deps)
		d.insert(configKey.Line, b.String())
		return nil
	case config.Kind != yaml.MappingNode || config.Style&yaml.FlowStyle != 0:
		return fmt.Errorf("config of the entry is not a block mapping (line %d); edit it by hand", configKey.Line)
	}

	childIndent := config.Column - 1
	if len(config.Content) > 0 {
		childIndent = config.Content[0].Column - 1
	}
	depsKey, depsNode := mapPair(config, "dependencies")

	// Dependencies joining an existing block go in first: when that block
	// is the config's last key, both insertions land on the same line and
	// are applied in the order they were recorded.
	if len(deps) > 0 && depsNode != nil {
		switch {
		case depsNode.Kind == yaml.ScalarNode && depsNode.Tag == "!!null":
			var db strings.Builder
			writeDeps(&db, depsKey.Column-1+2, deps)
			d.insert(depsKey.Line, db.String())
		case depsNode.Kind != yaml.MappingNode || depsNode.Style&yaml.FlowStyle != 0:
			return fmt.Errorf("config.dependencies of the entry is not a block mapping (line %d); edit it by hand", depsKey.Line)
		default:
			pad := depsKey.Column - 1 + 2
			if len(depsNode.Content) > 0 {
				pad = depsNode.Content[0].Column - 1
			}
			var db strings.Builder
			writeDeps(&db, pad, deps)
			d.insert(lastLine(depsNode), db.String())
		}
	}

	var b strings.Builder
	writeFields(&b, childIndent, fields)
	if len(deps) > 0 && depsNode == nil {
		b.WriteString(indent(childIndent) + "dependencies:\n")
		writeDeps(&b, childIndent+2, deps)
	}
	d.insert(lastLine(config), b.String())
	return nil
}

// AddToDependency adds fields to the existing entry.config.dependencies.<depKey>
// block (a dependency the entry already declares). The caller has checked
// none is declared yet. A dependency written in flow style ({name: x}) or
// as a bare null cannot be extended textually and is an error. Record it
// before an AddToEntry on the same entry: both may land on the same line,
// and the dependency's own fields must come first.
func (d *Doc) AddToDependency(entry *yaml.Node, depKey string, fields []Field) error {
	if len(fields) == 0 {
		return nil
	}
	key, dep := mapPair(ConfigValue(entry, "dependencies"), depKey)
	if dep == nil {
		return fmt.Errorf("dependency %q is not declared", depKey)
	}
	if dep.Kind != yaml.MappingNode || dep.Style&yaml.FlowStyle != 0 || len(dep.Content) == 0 {
		return fmt.Errorf("config.dependencies.%s is not a block mapping (line %d); edit it by hand", depKey, key.Line)
	}
	var b strings.Builder
	writeFields(&b, dep.Content[0].Column-1, fields)
	d.insert(lastLine(dep), b.String())
	return nil
}

// AddTopLevel adds fields to spec.defaultValues, before its components
// block when it has one (so the scalars stay together above it), otherwise
// at the end. The caller has checked none is declared yet.
func (d *Doc) AddTopLevel(fields []Field) error {
	if len(fields) == 0 {
		return nil
	}
	defaults := mapGet(mapGet(documentRoot(d.root), "spec"), "defaultValues")
	if defaults == nil || defaults.Kind != yaml.MappingNode || defaults.Style&yaml.FlowStyle != 0 || len(defaults.Content) == 0 {
		return fmt.Errorf("spec.defaultValues is not a block mapping")
	}
	pad := defaults.Content[0].Column - 1
	// After the last pair before "components"; the end when there is none.
	after := lastLine(defaults)
	for i := 0; i+1 < len(defaults.Content); i += 2 {
		if defaults.Content[i].Value == "components" {
			if i == 0 {
				after = defaults.Content[0].Line - 1 - headCommentLines(defaults.Content[0])
			} else {
				after = lastLine(defaults.Content[i-1])
			}
			break
		}
	}
	var b strings.Builder
	writeFields(&b, pad, fields)
	d.insert(after, b.String())
	return nil
}

// AppendEntry appends a components.<componentKey>[] entry named name with
// the given config (omitted when empty), creating the key's sequence when
// the file does not declare it. It never touches an existing entry: the
// caller checks the name is not declared first.
func (d *Doc) AppendEntry(componentKey, name string, config map[string]any) error {
	components, err := d.components()
	if err != nil {
		return err
	}
	if components.Kind != yaml.MappingNode || components.Style&yaml.FlowStyle != 0 || len(components.Content) == 0 {
		return fmt.Errorf("spec.defaultValues.components is not a block mapping with entries")
	}
	keyIndent := components.Content[0].Column - 1

	var item strings.Builder
	dash := func(pad int) {
		item.WriteString(indent(pad) + "- name: " + scalar(name) + "\n")
		if len(config) > 0 {
			item.WriteString(indent(pad+2) + "config:\n")
			item.WriteString(mappingText(config, pad+4))
		}
	}

	keyNode, seq := mapPair(components, componentKey)
	if seq == nil {
		var b strings.Builder
		b.WriteString(indent(keyIndent) + componentKey + ":\n")
		item.Reset()
		dash(d.dashIndent(components, keyIndent))
		b.WriteString(item.String())
		d.insert(lastLine(components), b.String())
		return nil
	}
	if seq.Kind != yaml.SequenceNode || seq.Style&yaml.FlowStyle != 0 || len(seq.Content) == 0 {
		return fmt.Errorf("components.%s (line %d) is not a block sequence with entries", componentKey, keyNode.Line)
	}
	dash(d.dashPad(seq.Content[0]))
	d.insert(lastLine(seq), item.String())
	return nil
}

// dashIndent is the indentation of a sequence's "- " under a components key
// at keyIndent: what the file's first existing sequence does, else two
// deeper.
func (d *Doc) dashIndent(components *yaml.Node, keyIndent int) int {
	for i := 0; i+1 < len(components.Content); i += 2 {
		if seq := components.Content[i+1]; seq.Kind == yaml.SequenceNode && len(seq.Content) > 0 {
			return d.dashPad(seq.Content[0]) - (components.Content[i].Column - 1) + keyIndent
		}
	}
	return keyIndent + 2
}

// dashPad is the indentation of the "- " that opens item, read from its line.
func (d *Doc) dashPad(item *yaml.Node) int {
	lines := strings.Split(string(d.source), "\n")
	if item.Line >= 1 && item.Line <= len(lines) {
		line := lines[item.Line-1]
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "- ") {
			return len(line) - len(trimmed)
		}
	}
	return item.Column - 1 - 2
}

func (d *Doc) insert(after int, text string) {
	if text == "" {
		return
	}
	d.edits = append(d.edits, textEdit{after: after, text: text})
}

// applyEdits returns src with every recorded insertion in place.
func applyEdits(src []byte, edits []textEdit) []byte {
	lines := strings.SplitAfter(string(src), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].after < edits[j].after })
	var out strings.Builder
	next := 0
	emit := func(upTo int) {
		for ; next < upTo && next < len(lines); next++ {
			out.WriteString(lines[next])
		}
	}
	for _, e := range edits {
		emit(e.after)
		if next > 0 && !strings.HasSuffix(lines[next-1], "\n") {
			out.WriteString("\n") // the file's last line had no newline
		}
		out.WriteString(e.text)
	}
	emit(len(lines))
	return []byte(out.String())
}

// lastLine is the last line of n's content: the deepest line any of its
// descendants starts on, extended over a block scalar's body.
func lastLine(n *yaml.Node) int {
	last := n.Line
	if n.Kind == yaml.ScalarNode && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		last += strings.Count(strings.TrimRight(n.Value, "\n"), "\n") + 1
	} else if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "\n") {
		last += strings.Count(strings.TrimRight(n.Value, "\n"), "\n")
	}
	for _, c := range n.Content {
		if l := lastLine(c); l > last {
			last = l
		}
	}
	return last
}

// headCommentLines is how many lines a key's head comment takes above it.
func headCommentLines(n *yaml.Node) int {
	if n.HeadComment == "" {
		return 0
	}
	return strings.Count(n.HeadComment, "\n") + 1
}

func mapPair(mapping *yaml.Node, key string) (k, v *yaml.Node) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i], mapping.Content[i+1]
		}
	}
	return nil, nil
}

func writeConfigBody(b *strings.Builder, pad int, fields []Field, deps []DependencyRef) {
	writeFields(b, pad, fields)
	if len(deps) > 0 {
		b.WriteString(indent(pad) + "dependencies:\n")
		writeDeps(b, pad+2, deps)
	}
}

func writeFields(b *strings.Builder, pad int, fields []Field) {
	for _, f := range fields {
		switch v := f.Value.(type) {
		case map[string]any:
			b.WriteString(indent(pad) + f.Key + ":\n")
			b.WriteString(mappingText(v, pad+2))
		default:
			b.WriteString(indent(pad) + f.Key + ": " + scalar(v) + "\n")
		}
	}
}

func writeDeps(b *strings.Builder, pad int, deps []DependencyRef) {
	for _, dep := range deps {
		b.WriteString(indent(pad) + dep.Key + ":\n")
		if dep.Enabled {
			b.WriteString(indent(pad+2) + "enabled: true\n")
		}
		b.WriteString(indent(pad+2) + "name: " + scalar(dep.Name) + "\n")
	}
}

// mappingText renders m (keys sorted) as a block mapping at pad.
func mappingText(m map[string]any, pad int) string {
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return ""
	}
	_ = enc.Close()
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		out.WriteString(indent(pad) + line + "\n")
	}
	return out.String()
}

// scalar renders one scalar the way the YAML encoder would.
func scalar(v any) string {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	out, err := yaml.Marshal(&n)
	if err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimRight(string(out), "\n")
}

func indent(n int) string {
	if n < 0 {
		n = 0
	}
	return strings.Repeat(" ", n)
}
