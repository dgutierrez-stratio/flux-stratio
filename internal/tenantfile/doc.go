package tenantfile

import (
	"bytes"
	"fmt"
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
	return &Doc{root: &root}, nil
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
// observes a truncated tenant file.
func (d *Doc) Save(path string) error {
	data, err := d.Bytes()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tenantfile-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming %s into place at %s: %w", tmpPath, path, err)
	}
	return nil
}
