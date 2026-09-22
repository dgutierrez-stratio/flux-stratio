package backup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// writeYAMLFile creates dir (if needed) and writes obj as name inside it,
// mode 0644.
func writeYAMLFile(dir, name string, obj map[string]any) error {
	data, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", name, err)
	}
	return writeFile(dir, name, data)
}

// writeEnvFile creates dir (if needed) and writes env as name inside it,
// as sorted "KEY=VALUE\n" lines — the same format
// internal/appdiff.readBaselineEnvFile reads back for `apps diff
// --baseline`.
func writeEnvFile(dir, name string, env map[string]string) error {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	for _, k := range keys {
		fmt.Fprintf(&buf, "%s=%s\n", k, env[k])
	}
	return writeFile(dir, name, buf.Bytes())
}

func writeFile(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
