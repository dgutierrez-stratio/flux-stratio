package config

import (
	"errors"
	"fmt"
	"os"
)

// EnvEnvironmentFile is the environment variable that overrides the
// default environment file path (see ResolveEnvironment).
const EnvEnvironmentFile = "FLUX_STRATIO_ENV"

// EnvironmentFile is the environment file's name under UserDir().
const EnvironmentFile = "environment.yaml"

// Environment is where the GitOps repositories are checked out and which
// cluster/tenant to operate on — everything the component catalog
// deliberately leaves out because it differs per workstation or per
// target.
type Environment struct {
	// Base is the parent directory holding keos-apps, keos-use-cases,
	// keos-fleet and keos-system-services as sibling checkouts.
	Base string `yaml:"base"`
	// ChartsBase, if set, overrides Base for resolving a chart-mode
	// type's on-disk Helm chart directory (ComponentType.Chart.Path) — for
	// the case where the chart-source repo isn't checked out as a sibling
	// of the keos-* repos under Base.
	ChartsBase string `yaml:"chartsBase,omitempty"`
	// Cluster is the cluster name to operate on.
	Cluster string `yaml:"cluster"`
	// Tenant is the tenant name to operate on.
	Tenant string `yaml:"tenant"`
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
// field of overrides on top (the root command's --base/--cluster/--tenant
// flags, which take precedence over the file), and validates that base,
// cluster and tenant are all set. No environment file at all is fine as
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
		if err := decodeStrict(data, &env); err != nil {
			return Environment{}, fmt.Errorf("parsing %s: %w", path, err)
		}
	}

	env = env.Override(overrides)
	if err := env.validate(); err != nil {
		if path == "" {
			return Environment{}, fmt.Errorf("%w (no environment file found: run `flux stratio config init`, or pass --base, --cluster and --tenant)", err)
		}
		return Environment{}, fmt.Errorf("%s: %w", path, err)
	}
	return env, nil
}

// Override returns e with every non-empty field of o applied on top.
func (e Environment) Override(o Environment) Environment {
	if o.Base != "" {
		e.Base = o.Base
	}
	if o.ChartsBase != "" {
		e.ChartsBase = o.ChartsBase
	}
	if o.Cluster != "" {
		e.Cluster = o.Cluster
	}
	if o.Tenant != "" {
		e.Tenant = o.Tenant
	}
	return e
}

// ChartsRoot is the directory a chart-mode type's Chart.Path resolves
// against: ChartsBase when set, Base otherwise.
func (e Environment) ChartsRoot() string {
	if e.ChartsBase != "" {
		return e.ChartsBase
	}
	return e.Base
}

func (e Environment) validate() error {
	var missing []string
	if e.Base == "" {
		missing = append(missing, "base")
	}
	if e.Cluster == "" {
		missing = append(missing, "cluster")
	}
	if e.Tenant == "" {
		missing = append(missing, "tenant")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("invalid environment: %v required", missing)
}
