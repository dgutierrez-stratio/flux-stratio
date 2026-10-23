package runner

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Binary describes one external command flux-stratio depends on.
type Binary struct {
	// Name is the executable name, looked up on PATH (e.g. "flux").
	Name string
	// VersionArgs are the arguments that print this binary's version
	// without contacting a live Kubernetes cluster — Preflight must never
	// depend on cluster access being configured.
	VersionArgs []string
	// MinVersion is the lowest acceptable "major.minor.patch" version, or
	// "" to only check presence and never fail on version.
	MinVersion string
	// InstallHint is one line telling the operator how to get this binary,
	// shown when it's missing or below MinVersion.
	InstallHint string
}

// Required lists every binary flux-stratio shells out to (see design
// decision 6, "shell out to flux-operator + flux"), and the flags used to
// query each one's version without touching a cluster. flux-operator's
// minimum matches the version the Python client this plugin replaces was
// built against (previously hardcoded into a help string at
// flux_renderer.py:117); flux's matches flux-keos's own stated requirement
// ("Flux CLI 2.9+"). helm and kubectl have no known minimum, so only their
// presence is enforced.
var Required = []Binary{
	{
		Name:        "flux-operator",
		VersionArgs: []string{"--version"},
		MinVersion:  "0.45.1",
		InstallHint: "https://github.com/controlplaneio-fluxcd/flux-operator/releases",
	},
	{
		Name:        "flux",
		VersionArgs: []string{"--version"},
		MinVersion:  "2.9.0",
		InstallHint: "https://fluxcd.io/flux/installation/",
	},
	{
		Name:        "helm",
		VersionArgs: []string{"version", "--short"},
		InstallHint: "https://helm.sh/docs/intro/install/",
	},
	{
		Name:        "kubectl",
		VersionArgs: []string{"version", "--client"},
		InstallHint: "https://kubernetes.io/docs/tasks/tools/",
	},
}

// Status is one binary's preflight result.
type Status struct {
	Binary  Binary
	Found   bool
	Version string // "" if not found or its version couldn't be parsed
	Problem string // "" if this binary passed preflight
}

// Report is the result of Preflight: one Status per Required binary, in
// the same order.
type Report struct {
	Statuses []Status
}

// OK reports whether every required binary was found and, where a minimum
// version is declared, met it.
func (r Report) OK() bool {
	for _, s := range r.Statuses {
		if s.Problem != "" {
			return false
		}
	}
	return true
}

// Err returns a single actionable error summarizing every problem in the
// report, one line per failing binary, or nil if OK().
func (r Report) Err() error {
	if r.OK() {
		return nil
	}
	var lines []string
	for _, s := range r.Statuses {
		if s.Problem != "" {
			lines = append(lines, fmt.Sprintf("  - %s: %s", s.Binary.Name, s.Problem))
		}
	}
	return fmt.Errorf("preflight failed:\n%s", strings.Join(lines, "\n"))
}

var versionPattern = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// Preflight checks every binary in Required for presence on PATH and, where
// a minimum version is declared, that its reported version meets it. It
// never contacts a live cluster: every Binary.VersionArgs above is chosen
// specifically to avoid that, so Preflight is safe to run before --kubeconfig
// or --kube-context have even been validated.
func Preflight(ctx context.Context, r Runner) Report {
	statuses := make([]Status, 0, len(Required))
	for _, b := range Required {
		statuses = append(statuses, checkBinary(ctx, r, b))
	}
	return Report{Statuses: statuses}
}

func checkBinary(ctx context.Context, r Runner, b Binary) Status {
	s := Status{Binary: b}
	stdout, stderr, err := r.Run(ctx, b.Name, b.VersionArgs...)
	if err != nil {
		s.Problem = fmt.Sprintf("not found or failed to run (%v); install it: %s", err, b.InstallHint)
		return s
	}
	s.Found = true

	m := versionPattern.FindStringSubmatch(string(stdout) + string(stderr))
	if m == nil {
		// Found and ran cleanly but the version couldn't be parsed out of
		// its output — treat as OK rather than fail preflight over an
		// output-format change we don't recognize.
		return s
	}
	s.Version = m[0]

	if b.MinVersion == "" {
		return s
	}
	atLeast, err := versionAtLeast(m[1:4], b.MinVersion)
	if err != nil {
		// The declared MinVersion is malformed, not the binary's fault.
		return s
	}
	if !atLeast {
		s.Problem = fmt.Sprintf("version %s is below the minimum %s; upgrade: %s", s.Version, b.MinVersion, b.InstallHint)
	}
	return s
}

// versionAtLeast reports whether gotParts (major, minor, patch, as decimal
// strings) is >= min ("major.minor.patch", an optional leading "v" allowed).
func versionAtLeast(gotParts []string, min string) (bool, error) {
	minParts := strings.SplitN(strings.TrimPrefix(min, "v"), ".", 3)
	if len(minParts) != 3 {
		return false, fmt.Errorf("invalid minimum version %q", min)
	}
	for i := range 3 {
		got, err := strconv.Atoi(gotParts[i])
		if err != nil {
			return false, fmt.Errorf("parsing version part %q: %w", gotParts[i], err)
		}
		want, err := strconv.Atoi(minParts[i])
		if err != nil {
			return false, fmt.Errorf("parsing minimum version part %q: %w", minParts[i], err)
		}
		if got != want {
			return got > want, nil
		}
	}
	return true, nil
}
