// Package config loads flux-stratio's external app catalog — the plan's
// sole source for which applications can be migrated, where their
// ResourceSet templates live, and any per-app quirks the generic pipeline
// needs (see the "Config file" section of the project plan). Unlike
// flux-keos's internal/fleetrepo, whose persisted config is a one-field
// pointer to another repo, this file *is* the data flux-stratio operates
// on: base/cluster/tenant plus the full application catalog.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// EnvConfigFile is the environment variable that overrides the default
// config file path (see Resolve).
const EnvConfigFile = "FLUX_STRATIO_CONFIG"

// Config is flux-stratio's external, version-controlled app catalog.
type Config struct {
	// Base is the parent directory holding keos-apps, keos-use-cases,
	// keos-fleet and keos-system-services as sibling checkouts.
	Base string `yaml:"base"`
	// Cluster is the cluster name to operate on.
	Cluster string `yaml:"cluster"`
	// Tenant is the tenant name to operate on.
	Tenant string `yaml:"tenant"`
	// ChartsBase, if set, overrides Base for resolving a chart-mode app's
	// on-disk Helm chart directory (App.ChartPath) — for the case where
	// the chart-source repo isn't checked out as a sibling of
	// keos-apps/keos-use-cases/keos-fleet/keos-system-services under
	// Base. Leave unset to resolve ChartPath relative to Base, as before.
	ChartsBase string `yaml:"chartsBase,omitempty"`
	// Apps is the catalog of migratable applications, in no particular
	// order — `apps migrate --all` orders them topologically at runtime
	// from their dependency schemas, not from this list's order.
	Apps []App `yaml:"apps"`
}

// App describes one application's migration: where its ResourceSet
// template and Kustomization live, which live object to compare against,
// and any per-app quirks the generic pipeline needs to know about.
type App struct {
	// ID is this app's unique identifier, used on the command line
	// (apps diff <id>, apps migrate <id>) and as its backup directory name.
	ID string `yaml:"id"`
	// Name is a human-readable label, shown in progress/log output.
	Name string `yaml:"name"`
	// Rset is the path, relative to keos-use-cases, to the ResourceSet
	// template that declares this app's Kustomization.
	Rset string `yaml:"rset"`
	// Kustomization is the name of the rendered Kustomization to inspect.
	Kustomization string `yaml:"kustomization"`
	// Object is the name of the HelmRelease or custom resource inside that
	// Kustomization to diff against the live cluster.
	Object string `yaml:"object"`
	// Anchor overrides where, relative to this app's own component
	// entry, its Kustomization's patches are read from — a dotted field
	// path, e.g. "config.agent" for a postgres/opensearch gosec agent —
	// when it isn't the entry's own top-level "patches" key. Which
	// components.<key>[] entry is "its own" never needs stating: every
	// component key is searched for one named Object (see design
	// decision 2). internal/catalog derives this automatically from the
	// templates for every case currently in use, so Anchor is normally
	// left unset; when set, internal/tenantfile.Splice validates it
	// against what the catalog independently derives and fails loudly on
	// a mismatch rather than writing to the wrong place.
	Anchor string `yaml:"anchor,omitempty"`
	// ChartPath, relative to Base (or ChartsBase, when set), selects
	// env-var/chart diff mode (internal/diff's Helm-values comparison)
	// instead of manifest diff mode. Empty means this app is a
	// CRD/manifest object compared directly against the live resource.
	ChartPath string `yaml:"chartPath,omitempty"`
	// ValuesRoot pins which .Values root to prefer when a chart mixes more
	// than one flavor's values under the same directory tree (e.g.
	// bdl-datarest's pginternal/pgmd5/pgtls flavors).
	ValuesRoot string `yaml:"valuesRoot,omitempty"`
	// Renamed is the live cluster object's name, when it differs from
	// Object because the GitOps redesign renamed it.
	Renamed string `yaml:"renamed,omitempty"`
	// PreviousNamespace is the fallback namespace to look for the live
	// object in when it is not found in the namespace the current
	// convention implies.
	PreviousNamespace string `yaml:"previousNamespace,omitempty"`
	// Prepare, if set, names a step in internal/prepare that must hold
	// before this app can be migrated. `apps migrate` satisfies it
	// automatically as its first pipeline stage (see design decision 5).
	Prepare string `yaml:"prepare,omitempty"`
	// Exclude lists dot-paths to drop from the computed diff/patch —
	// fields the GitOps side is authoritative for and must never be
	// back-ported from the live cluster (identity/vault/governance values).
	Exclude []string `yaml:"exclude,omitempty"`
	// Notes is a free-text hint shown to the operator, e.g. alongside a
	// Prepare step that blocks migration.
	Notes string `yaml:"notes,omitempty"`
}

// Resolve determines which config file path to load, in priority order:
// the --config flag, $FLUX_STRATIO_CONFIG, ~/.fluxcd/flux-stratio/config.yaml
// (if it exists), ./flux-stratio.yaml (if it exists). An explicit flag or
// env var is used as given, even if the file doesn't exist yet, so the
// resulting error names the exact path the operator asked for.
//
// ~/.fluxcd/flux-stratio/ is a sibling of ~/.fluxcd/plugins/ (where the
// plugin binary itself lives, per RFC 0013), not a subdirectory of it —
// plugins/ is reserved for binaries, never data. Placing a config here
// (and, since apps backup's default --dir is always "next to the
// resolved config file," backups too) keeps both permanently outside any
// flux-stratio source checkout, safe from `make clean`, `git clean`, or a
// fresh clone.
func Resolve(flagValue string) (string, error) {
	if flagValue != "" {
		return filepath.Abs(flagValue)
	}
	if env := os.Getenv(EnvConfigFile); env != "" {
		return filepath.Abs(env)
	}

	userPath, userPathErr := userConfigPath()
	if userPathErr == nil {
		if _, err := os.Stat(userPath); err == nil {
			return userPath, nil
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolving current directory: %w", err)
	}
	cwdPath := filepath.Join(cwd, "flux-stratio.yaml")
	if _, err := os.Stat(cwdPath); err == nil {
		return cwdPath, nil
	}

	return "", fmt.Errorf(
		"no flux-stratio config file found (tried %s and %s); pass --config, set %s, "+
			"or create one at either location",
		userPath, cwdPath, EnvConfigFile,
	)
}

func userConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".fluxcd", "flux-stratio", "config.yaml"), nil
}

// Load resolves and parses the config file, rejecting unknown top-level or
// app-level keys (a schema typo should fail loudly, not be silently
// ignored) and validating every required field.
func Load(flagValue string) (*Config, error) {
	path, err := Resolve(flagValue)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// Marshal renders cfg as 2-space-indented YAML — matching every other
// YAML file this plugin writes — instead of yaml.v3's own 4-space
// Marshal default. It's what `flux stratio config init` writes to disk.
func Marshal(cfg Config) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (c Config) validate() error {
	var problems []string
	if c.Base == "" {
		problems = append(problems, "base is required")
	}
	if c.Cluster == "" {
		problems = append(problems, "cluster is required")
	}
	if c.Tenant == "" {
		problems = append(problems, "tenant is required")
	}

	seen := make(map[string]bool, len(c.Apps))
	for i, app := range c.Apps {
		label := app.ID
		if label == "" {
			label = fmt.Sprintf("apps[%d]", i)
		}
		if app.ID == "" {
			problems = append(problems, fmt.Sprintf("%s: id is required", label))
		} else if seen[app.ID] {
			problems = append(problems, fmt.Sprintf("apps[%d]: duplicate id %q", i, app.ID))
		}
		seen[app.ID] = true
		if app.Name == "" {
			problems = append(problems, fmt.Sprintf("%s: name is required", label))
		}
		if app.Rset == "" {
			problems = append(problems, fmt.Sprintf("%s: rset is required", label))
		}
		if app.Kustomization == "" {
			problems = append(problems, fmt.Sprintf("%s: kustomization is required", label))
		}
		if app.Object == "" {
			problems = append(problems, fmt.Sprintf("%s: object is required", label))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	msg := "invalid config:"
	for _, p := range problems {
		msg += "\n  - " + p
	}
	return fmt.Errorf("%s", msg)
}

// Effective returns base/cluster/tenant after applying any non-empty
// override — the semantics of the root command's --base/--cluster/--tenant
// flags, which take precedence over the config file's own values.
func (c Config) Effective(baseOverride, clusterOverride, tenantOverride string) (base, cluster, tenant string) {
	base, cluster, tenant = c.Base, c.Cluster, c.Tenant
	if baseOverride != "" {
		base = baseOverride
	}
	if clusterOverride != "" {
		cluster = clusterOverride
	}
	if tenantOverride != "" {
		tenant = tenantOverride
	}
	return base, cluster, tenant
}

// Find returns the app with the given id, or nil if none matches.
func (c Config) Find(id string) *App {
	for i := range c.Apps {
		if c.Apps[i].ID == id {
			return &c.Apps[i]
		}
	}
	return nil
}

// LiveName is the name to look this app up as on the live cluster:
// Renamed when the GitOps redesign renamed the object, otherwise Object
// itself. internal/appdiff and internal/backup both need this exact same
// fallback to agree on which live object an app maps to — kept here as
// the one shared definition instead of duplicated copies that could
// silently drift apart.
func (a App) LiveName() string {
	if a.Renamed != "" {
		return a.Renamed
	}
	return a.Object
}
