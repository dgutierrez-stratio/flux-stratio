package appdiff

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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
