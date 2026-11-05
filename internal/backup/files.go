package backup

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/Stratio/flux-stratio/internal/envvars"
)

// A backup holds live manifests and values that may carry credentials
// (a HelmRelease's inline values, a CR's spec), so only its owner can
// read it: directories 0700, files 0600.
const (
	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

// writeYAMLFile creates dir (if needed) and writes obj as name inside it.
func writeYAMLFile(dir, name string, obj map[string]any) error {
	data, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", name, err)
	}
	return writeFile(dir, name, data)
}

// writeEnvFile creates dir (if needed) and writes env as name inside it,
// in envvars.EncodeFile's format — what internal/appdiff reads back
// (envvars.DecodeFile) for `apps diff --baseline`.
func writeEnvFile(dir, name string, env map[string]string) error {
	return writeFile(dir, name, envvars.EncodeFile(env))
}

func writeFile(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, fileMode); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
