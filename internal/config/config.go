// Package config loads flux-stratio's two external, operator-owned files:
//
//   - the component catalog (catalog.yaml, see Catalog): a typed list of
//     every kind of component `apps diff/backup/migrate` supports — static
//     facts only (ResourceSet template, chart, excludes, prepare step) plus
//     the selectors that recognize a live legacy object as an instance of
//     that type. Nothing in it is environment-specific, so the same file
//     works against any cluster.
//   - the environment (environment.yaml, see Environment): where the GitOps
//     repositories are checked out and which cluster/tenant to operate on.
//
// Both are seeded by `flux stratio config init`. Instance-specific facts a
// catalog type can't know in advance — which live object, under which name
// and namespace, maps to which tenant-file entry — are never stored in
// either file: internal/components derives them from the live cluster and
// the tenant file on every run, into a resolved App.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// EnvConfigFile is the environment variable that overrides the default
// catalog file path (see Resolve).
const EnvConfigFile = "FLUX_STRATIO_CONFIG"

// legacyConfigFile is the pre-catalog config file name, a single file that
// mixed base/cluster/tenant with a flat, per-instance apps list. Only used
// to point an operator still holding one at `config init`.
const legacyConfigFile = "config.yaml"

// Resolve determines which catalog file path to load, in priority order:
// the --config flag, $FLUX_STRATIO_CONFIG, ~/.fluxcd/flux-stratio/catalog.yaml
// (if it exists), ./flux-stratio.yaml (if it exists). An explicit flag or
// env var is used as given, even if the file doesn't exist yet, so the
// resulting error names the exact path the operator asked for.
//
// ~/.fluxcd/flux-stratio/ is a sibling of ~/.fluxcd/plugins/ (where the
// plugin binary itself lives, per RFC 0013), not a subdirectory of it —
// plugins/ is reserved for binaries, never data. Placing the catalog here
// (and, since apps backup's default --dir is always "next to the resolved
// catalog file," backups too) keeps both permanently outside any
// flux-stratio source checkout, safe from `make clean`, `git clean`, or a
// fresh clone.
func Resolve(flagValue string) (string, error) {
	return resolvePath(flagValue, EnvConfigFile, CatalogFile, "flux-stratio.yaml", "catalog", "--config")
}

// UserDir is ~/.fluxcd/flux-stratio, the default home of both the catalog
// and the environment file (and `config init`'s default --dir).
func UserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".fluxcd", "flux-stratio"), nil
}

// resolvePath implements the lookup ladder shared by Resolve and
// ResolveEnvironment: flag, then env var (both used as given), then
// UserDir()/userFile and ./cwdFile, whichever exists first.
func resolvePath(flagValue, envVar, userFile, cwdFile, what, flagName string) (string, error) {
	if flagValue != "" {
		return filepath.Abs(flagValue)
	}
	if env := os.Getenv(envVar); env != "" {
		return filepath.Abs(env)
	}

	userPath := "~/.fluxcd/flux-stratio/" + userFile
	if dir, err := UserDir(); err == nil {
		userPath = filepath.Join(dir, userFile)
		if _, err := os.Stat(userPath); err == nil {
			return userPath, nil
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolving current directory: %w", err)
	}
	cwdPath := filepath.Join(cwd, cwdFile)
	if _, err := os.Stat(cwdPath); err == nil {
		return cwdPath, nil
	}

	return "", &notFoundError{what: what, flag: flagName, tried: []string{userPath, cwdPath}, envVar: envVar}
}

// notFoundError is resolvePath's "no candidate exists" error, kept typed
// so LoadEnvironment can tell "no environment file at all" (fine, when
// flags supply every field) apart from a real read or parse failure.
type notFoundError struct {
	what   string
	flag   string
	tried  []string
	envVar string
}

func (e *notFoundError) Error() string {
	msg := fmt.Sprintf(
		"no flux-stratio %s file found (tried %s and %s); run `flux stratio config init`, pass %s, or set %s",
		e.what, e.tried[0], e.tried[1], e.flag, e.envVar,
	)
	if e.what == "catalog" {
		if dir, err := UserDir(); err == nil {
			if _, err := os.Stat(filepath.Join(dir, legacyConfigFile)); err == nil {
				msg += fmt.Sprintf("; %s is a pre-catalog config file this version no longer reads — "+
					"re-run `flux stratio config init` to replace it with catalog.yaml + environment.yaml",
					filepath.Join(dir, legacyConfigFile))
			}
		}
	}
	return msg
}

// Load resolves and parses the catalog file, rejecting unknown keys at
// every level (a schema typo should fail loudly, not be silently ignored)
// and validating every type.
func Load(flagValue string) (*Catalog, error) {
	path, err := Resolve(flagValue)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if isLegacyConfig(data) {
		return nil, fmt.Errorf(
			"%s is a pre-catalog config file (top-level apps: list); this version reads a typed component "+
				"catalog instead — run `flux stratio config init --force` to replace it", path)
	}

	var cat Catalog
	if err := decodeStrict(data, &cat); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := cat.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cat, nil
}

// isLegacyConfig reports whether data is the old single-file config shape
// (a top-level apps: list), so Load can say "re-run config init" instead
// of a bare unknown-field error.
func isLegacyConfig(data []byte) bool {
	var probe map[string]any
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return false
	}
	_, hasApps := probe["apps"]
	return hasApps
}

func decodeStrict(data []byte, into any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(into); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Marshal renders v as 2-space-indented YAML — matching every other YAML
// file this plugin writes — instead of yaml.v3's own 4-space Marshal
// default. It's what `flux stratio config init` writes to disk.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
