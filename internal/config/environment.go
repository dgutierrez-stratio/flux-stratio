package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvEnvironmentFile is the environment variable that overrides the
// default environment file path (see ResolveEnvironment).
const EnvEnvironmentFile = "FLUX_STRATIO_ENV"

// EnvironmentFile is the environment file's name under UserDir().
const EnvironmentFile = "environment.yaml"

// The repositories flux-stratio reads, by the name each one is configured
// under (environment.yaml's repos keys, --repo) and defaults to under Base.
const (
	RepoApps           = "keos-apps"
	RepoUseCases       = "keos-use-cases"
	RepoFleet          = "keos-fleet"
	RepoSystemServices = "keos-system-services"
	RepoCharts         = "charts"
)

// RepoNames lists every repository name, in the order errors report them.
var RepoNames = []string{RepoApps, RepoUseCases, RepoFleet, RepoSystemServices, RepoCharts}

// Environment is where the GitOps repositories are checked out and which
// cluster/tenant to operate on — everything the component catalog
// deliberately leaves out because it differs per workstation or per
// target.
type Environment struct {
	// Base is the default parent directory of every repository: one not
	// listed in Repos is expected at Base/<name>. Optional when Repos
	// lists them all.
	Base string `yaml:"base,omitempty"`
	// Repos points a repository (keyed by one of RepoNames) straight at
	// its checkout — for one that isn't at Base/<name>, e.g. a git
	// worktree or a clone under another name.
	Repos map[string]string `yaml:"repos,omitempty"`
	// Cluster is the cluster name to operate on.
	Cluster string `yaml:"cluster"`
	// Tenant is the tenant name to operate on.
	Tenant string `yaml:"tenant"`
}

// RepoPaths is where each repository is checked out, fully resolved.
type RepoPaths struct {
	Apps, UseCases, Fleet, SystemServices string
	// Charts is the chart-source repository's root, which a chart-mode
	// type's Chart.Path is relative to.
	Charts string
}

// ReposUnder is the default layout: every repository a sibling checkout
// under base.
func ReposUnder(base string) RepoPaths {
	return RepoPaths{
		Apps:           filepath.Join(base, RepoApps),
		UseCases:       filepath.Join(base, RepoUseCases),
		Fleet:          filepath.Join(base, RepoFleet),
		SystemServices: filepath.Join(base, RepoSystemServices),
		Charts:         filepath.Join(base, RepoCharts),
	}
}

// Repo is where the repository called name is checked out: its Repos
// entry, else Base/<name>, else "" when neither is set.
func (e Environment) Repo(name string) string {
	if p := e.Repos[name]; p != "" {
		return p
	}
	if e.Base == "" {
		return ""
	}
	return filepath.Join(e.Base, name)
}

// RepoPaths resolves every repository through Repo.
func (e Environment) RepoPaths() RepoPaths {
	return RepoPaths{
		Apps:           e.Repo(RepoApps),
		UseCases:       e.Repo(RepoUseCases),
		Fleet:          e.Repo(RepoFleet),
		SystemServices: e.Repo(RepoSystemServices),
		Charts:         e.Repo(RepoCharts),
	}
}

// ResolveEnvironment determines which environment file path to load, in
// priority order: the --env-config flag, $FLUX_STRATIO_ENV,
// ~/.fluxcd/flux-stratio/environment.yaml (if it exists),
// ./flux-stratio-env.yaml (if it exists) — the same ladder, and the same
// explicit-path-used-as-given rule, as Resolve.
func ResolveEnvironment(flagValue string) (string, error) {
	return resolvePath(flagValue, EnvEnvironmentFile, EnvironmentFile, "flux-stratio-env.yaml", "environment", "--env-config")
}

// LoadEnvironment reads the environment file, applies every non-empty
// field of overrides on top (the root command's --base/--repo/--cluster/
// --tenant flags, which take precedence over the file), and validates that
// every repository resolves and cluster and tenant are set. No environment file at all is fine as
// long as overrides supply every required field; a file that exists but
// can't be read or parsed is always an error.
func LoadEnvironment(flagValue string, overrides Environment) (Environment, error) {
	var env Environment

	path, err := ResolveEnvironment(flagValue)
	var nf *notFoundError
	switch {
	case errors.As(err, &nf):
		// No file anywhere on the ladder — overrides must carry it all.
	case err != nil:
		return Environment{}, err
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return Environment{}, fmt.Errorf("reading %s: %w", path, err)
		}
		if err := rejectChartsBase(data); err != nil {
			return Environment{}, fmt.Errorf("%s: %w", path, err)
		}
		if err := decodeStrict(data, &env); err != nil {
			return Environment{}, fmt.Errorf("parsing %s: %w", path, err)
		}
		// A relative path in the file is relative to the file itself, not
		// to wherever a later command happens to run from.
		if env, err = env.AbsPaths(filepath.Dir(path)); err != nil {
			return Environment{}, fmt.Errorf("%s: %w", path, err)
		}
	}

	overrides, err = overrides.AbsPaths("")
	if err != nil {
		return Environment{}, err
	}
	env = env.Override(overrides)
	if err := env.Validate(); err != nil {
		if path == "" {
			return Environment{}, fmt.Errorf("%w (no environment file found: run `flux stratio config init`, or pass --base (or --repo), --cluster and --tenant)", err)
		}
		return Environment{}, fmt.Errorf("%s: %w", path, err)
	}
	return env, nil
}

// ParseEnvironmentRepos reads just the repos entries of an environment
// file's content, made absolute against dir (the file's directory).
func ParseEnvironmentRepos(data []byte, dir string) (map[string]string, error) {
	var env struct {
		Repos map[string]string `yaml:"repos"`
	}
	if err := yaml.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	abs, err := Environment{Repos: env.Repos}.AbsPaths(dir)
	if err != nil {
		return nil, err
	}
	return abs.Repos, nil
}

// AbsPaths returns e with Base and every Repos path made absolute: a
// leading ~ expands to the home directory, and a relative path is taken
// relative to dir — or to the working directory when dir is "", for paths
// given as flags.
func (e Environment) AbsPaths(dir string) (Environment, error) {
	var err error
	if e.Base, err = absPath(e.Base, dir); err != nil {
		return Environment{}, err
	}
	if len(e.Repos) > 0 {
		repos := make(map[string]string, len(e.Repos))
		for name, p := range e.Repos {
			if repos[name], err = absPath(p, dir); err != nil {
				return Environment{}, err
			}
		}
		e.Repos = repos
	}
	return e, nil
}

func absPath(p, dir string) (string, error) {
	switch {
	case p == "":
		return "", nil
	case p == "~" || strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expanding %s: %w", p, err)
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	case filepath.IsAbs(p):
		return filepath.Clean(p), nil
	case dir != "":
		// dir may itself be relative (a --dir ./cfg): resolve the result
		// against the working directory too, or what is written down would
		// be read back relative to a different directory.
		abs, err := filepath.Abs(filepath.Join(dir, p))
		if err != nil {
			return "", fmt.Errorf("resolving %s: %w", p, err)
		}
		return abs, nil
	default:
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", fmt.Errorf("resolving %s: %w", p, err)
		}
		return abs, nil
	}
}

// Override returns e with every non-empty field of o applied on top; o's
// Repos entries override e's one by one.
func (e Environment) Override(o Environment) Environment {
	if o.Base != "" {
		e.Base = o.Base
	}
	if len(o.Repos) > 0 {
		repos := make(map[string]string, len(e.Repos)+len(o.Repos))
		for name, p := range e.Repos {
			repos[name] = p
		}
		for name, p := range o.Repos {
			repos[name] = p
		}
		e.Repos = repos
	}
	if o.Cluster != "" {
		e.Cluster = o.Cluster
	}
	if o.Tenant != "" {
		e.Tenant = o.Tenant
	}
	return e
}

// Validate checks that every repository resolves (through Base or a Repos
// entry), that Repos names only known repositories, and that Cluster and
// Tenant are set.
func (e Environment) Validate() error {
	var problems []string
	for name := range e.Repos {
		if !slices.Contains(RepoNames, name) {
			problems = append(problems, fmt.Sprintf("unknown repo %q (known: %s)", name, strings.Join(RepoNames, ", ")))
		}
	}
	var missing []string
	if e.Base == "" {
		var unset []string
		for _, name := range RepoNames {
			if e.Repo(name) == "" {
				unset = append(unset, name)
			}
		}
		if len(unset) > 0 {
			missing = append(missing, fmt.Sprintf("base (or repos %s)", strings.Join(unset, ", ")))
		}
	}
	if e.Cluster == "" {
		missing = append(missing, "cluster")
	}
	if e.Tenant == "" {
		missing = append(missing, "tenant")
	}
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf("%v required", missing))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid environment: %s", strings.Join(problems, "; "))
}

// rejectChartsBase fails an environment file that still sets chartsBase,
// which repos.charts replaced: it pointed at the directory above the
// charts repo, and a catalog chart.path started with that repo's own
// directory name (charts/litellm). Both now point at the repo itself.
func rejectChartsBase(data []byte) error {
	var keys map[string]any
	if err := yaml.Unmarshal(data, &keys); err != nil {
		return nil // decodeStrict reports it
	}
	old, ok := keys["chartsBase"]
	if !ok {
		return nil
	}
	return fmt.Errorf("chartsBase is no longer supported: set repos.charts to the charts repository checkout itself "+
		"(%v/charts for the old value), and re-run `flux stratio config init --force` so the catalog's chart paths are "+
		"relative to it", old)
}
