package appdiff

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// readBaselineYAML reads name (e.g. "cr.yaml") from a backup directory —
// the same format internal/backup's yamlFile writer produces — and decodes
// it into an Unstructured object.
func readBaselineYAML(dir, name string) (*unstructured.Unstructured, error) {
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading baseline %s: %w", path, err)
	}
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(data, &obj.Object); err != nil {
		return nil, fmt.Errorf("parsing baseline %s: %w", path, err)
	}
	return obj, nil
}

// readBaselineEnvFile reads name (e.g. "env-vars.env") from a backup
// directory — the same "KEY=VALUE" format internal/backup's envFile writer
// produces — into a map.
func readBaselineEnvFile(dir, name string) (map[string]string, error) {
	path := filepath.Join(dir, name)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading baseline %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	out := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading baseline %s: %w", path, err)
	}
	return out, nil
}
