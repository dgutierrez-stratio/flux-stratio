package tenantfile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Doc is a tenant's ResourceSetInputProvider file, decoded as a yaml.Node
// tree so every comment, blank line and commented-out block survives a
// round trip untouched (see design decision 3 in the project plan). The
// Python client's equivalent, apply_patch_to_tenant_yaml, decoded into a
// plain dict and re-dumped the whole file with yaml.dump, destroying all
// of that — the real tenant files this plugin edits are roughly 40%
// comments and commented-out component blocks.
type Doc struct {
	root *yaml.Node // a DocumentNode
	// source is the file's content as Load read it (or Save last wrote
	// it), which Save checks the file still holds before replacing it.
	source []byte
}

// Load reads and parses the tenant file at path.
func Load(path string) (*Doc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &Doc{root: &root, source: data}, nil
}

// Bytes re-encodes the document at indent 2, matching every other YAML
// file this plugin writes.
func (d *Doc) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d.root); err != nil {
		return nil, fmt.Errorf("encoding tenant file: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("closing tenant file encoder: %w", err)
	}
	return buf.Bytes(), nil
}

// Save writes the document to path via a temp file in the same directory
// plus an atomic rename, so a crash or a concurrent read mid-write never
// observes a truncated tenant file. A symlinked path is written through
// to its target, and the file keeps its permissions.
//
// apps migrate loads the file, then asks several questions and may run a
// prepare step before saving: if the file changed on disk in the meantime
// (an editor, a second migrate run, a git checkout), Save refuses rather
// than overwrite that change with a document built from the old content.
func (d *Doc) Save(path string) error {
	data, err := d.Bytes()
	if err != nil {
		return err
	}

	mode := os.FileMode(0o644)
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	if current, err := os.ReadFile(path); err == nil {
		if d.source != nil && !bytes.Equal(current, d.source) {
			return fmt.Errorf("%s changed on disk since it was read; nothing written — run the command again against its current content", path)
		}
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tenantfile-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	fail := func(format string, err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf(format, tmpPath, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail("setting the mode of %s: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail("writing %s: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fail("syncing %s: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming %s into place at %s: %w", tmpPath, path, err)
	}
	d.source = data
	return nil
}
