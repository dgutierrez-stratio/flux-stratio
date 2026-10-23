package appdiff

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Stratio/flux-stratio/internal/envvars"
	"github.com/Stratio/flux-stratio/internal/yamldocs"
)

// readBaselineYAML reads name (e.g. "cr.yaml") from a backup directory —
// the same format internal/backup's yamlFile writer produces — and decodes
// it into an Unstructured object through internal/yamldocs.Decode, the
// same int64-for-whole-numbers convention the rendered and live sides use.
// A plain map decode here made every integer a float64, so a --baseline
// diff reported every unchanged integer field (probe thresholds,
// minAvailable, ...) as a difference and back-ported it into the patch.
func readBaselineYAML(dir, name string) (*unstructured.Unstructured, error) {
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading baseline %s: %w", path, err)
	}
	docs, err := yamldocs.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("parsing baseline %s: %w", path, err)
	}
	if len(docs) != 1 {
		return nil, fmt.Errorf("parsing baseline %s: want exactly one object, found %d", path, len(docs))
	}
	return docs[0], nil
}

// mergedEnvFile is a chart-mode backup's merged env vars of every live
// workload the chart declares.
const mergedEnvFile = "env-vars.env"

// Affixes of a chart-mode backup's per-workload env file names.
const (
	workloadEnvFilePrefix = "env-vars."
	workloadEnvFileSuffix = ".env"
)

// WorkloadEnvFile names the backup file holding one live workload's env
// vars, next to the merged env-vars.env — e.g.
// "env-vars.deployment.genai-ui.env" — so a --baseline diff can compare
// each sibling of a multi-workload chart against its own rendered
// workload, and a drift check can tell which sibling changed. Workload
// names are DNS-1123 labels, safe as file name parts.
func WorkloadEnvFile(kind, name string) string {
	return workloadEnvFilePrefix + strings.ToLower(kind) + "." + name + workloadEnvFileSuffix
}

// WorkloadOfEnvFile is the workload name a WorkloadEnvFile name was made for
// ("env-vars.deployment.web.env" -> "web"), or name itself when it isn't one.
func WorkloadOfEnvFile(name string) string {
	if !IsWorkloadEnvFile(name) {
		return name
	}
	_, workload, _ := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(name, workloadEnvFilePrefix), workloadEnvFileSuffix), ".")
	return workload
}

// IsWorkloadEnvFile reports whether name is a WorkloadEnvFile name.
func IsWorkloadEnvFile(name string) bool {
	return name != mergedEnvFile &&
		strings.HasPrefix(name, workloadEnvFilePrefix) && strings.HasSuffix(name, workloadEnvFileSuffix) &&
		strings.Count(strings.TrimSuffix(strings.TrimPrefix(name, workloadEnvFilePrefix), workloadEnvFileSuffix), ".") >= 1
}

// readBaselineWorkloadEnv reads one WorkloadEnvFile from a backup
// directory; ok is false, with no error, when the backup has none.
func readBaselineWorkloadEnv(dir, name string) (env map[string]string, ok bool, err error) {
	env, err = readBaselineEnvFile(dir, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return env, true, nil
}

// readBaselineEnvFile reads name (e.g. "env-vars.env") from a backup
// directory — envvars.EncodeFile's format, which internal/backup writes —
// into a map.
func readBaselineEnvFile(dir, name string) (map[string]string, error) {
	path := filepath.Join(dir, name)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading baseline %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	out, err := envvars.DecodeFile(f)
	if err != nil {
		return nil, fmt.Errorf("reading baseline %s: %w", path, err)
	}
	return out, nil
}

// Affixes of a backup's sibling workload manifest names; internal/backup
// writes them (WorkloadManifestFile) but imports this package, so they are
// repeated here. The first workload is always deployment.yaml.
const (
	workloadManifestPrefix = "workload."
	workloadManifestSuffix = ".yaml"
)

// readBaselineManifests reads every workload manifest of a backup directory
// (deployment.yaml and the sibling workload.<kind>.<name>.yaml files), keyed
// "Kind/name" by what each one declares: deployment.yaml is the app's first
// workload whatever its kind or name, so the file name can't be matched
// against a target. A backup taken before manifests were kept has none.
func readBaselineManifests(dir string) (map[string]*unstructured.Unstructured, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading baseline %s: %w", dir, err)
	}
	out := map[string]*unstructured.Unstructured{}
	for _, e := range entries {
		name := e.Name()
		sibling := strings.HasPrefix(name, workloadManifestPrefix) && strings.HasSuffix(name, workloadManifestSuffix)
		if e.IsDir() || (name != "deployment.yaml" && !sibling) {
			continue
		}
		obj, err := readBaselineYAML(dir, name)
		if err != nil {
			return nil, err
		}
		if obj.GetName() == "" {
			continue
		}
		out[obj.GetKind()+"/"+obj.GetName()] = obj
	}
	return out, nil
}
