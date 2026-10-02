package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// rsetFile is the shape of one apps/components/resourceset-apps-*.yaml
// file that this package actually reads: the resourcesTemplate string. The
// rest of the ResourceSet manifest (spec.resources, spec.inputsFrom, ...)
// is irrelevant to catalog extraction.
type rsetFile struct {
	Spec struct {
		ResourcesTemplate string `yaml:"resourcesTemplate"`
	} `yaml:"spec"`
}

// Load parses every apps/components/resourceset-apps-*.yaml file under
// keosUseCasesDir into a Catalog.
func Load(keosUseCasesDir string) (*Catalog, error) {
	pattern := filepath.Join(keosUseCasesDir, "apps", "components", "resourceset-apps-*.yaml")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("globbing %s: %w", pattern, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no resourceset-apps-*.yaml templates found under %s", pattern)
	}
	sort.Strings(files)

	schemas := map[string]Schema{}
	chartOwners := map[string][]string{}
	crds := map[string]CRDInfo{}

	for _, f := range files {
		if err := parseFile(f, schemas, chartOwners, crds); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", f, err)
		}
	}

	charts := map[string]ChartMapping{}
	for chart, owners := range chartOwners {
		if len(owners) == 1 {
			charts[chart] = ChartMapping{Key: owners[0]}
		} else {
			charts[chart] = ChartMapping{Ambiguous: owners}
		}
	}

	anchors, err := buildAnchorIndex(schemas)
	if err != nil {
		return nil, err
	}

	return &Catalog{Schemas: schemas, Charts: charts, CRDs: crds, anchorsBySuffix: anchors}, nil
}

func parseFile(path string, schemas map[string]Schema, chartOwners map[string][]string, crds map[string]CRDInfo) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading: %w", err)
	}
	var doc rsetFile
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decoding YAML: %w", err)
	}
	if doc.Spec.ResourcesTemplate == "" {
		return nil
	}

	for _, block := range splitComponentBlocks(doc.Spec.ResourcesTemplate) {
		s := extractSchema(block.key, block.text)
		mergeSchema(schemas, s)

		if s.ChartName != "" && !contains(chartOwners[s.ChartName], s.Key) {
			chartOwners[s.ChartName] = append(chartOwners[s.ChartName], s.Key)
		}
		for plural, info := range extractCRDs(block.text, s.Key) {
			if _, exists := crds[plural]; !exists {
				crds[plural] = info
			}
		}
	}
	return nil
}

// mergeSchema registers s under its key, unless a schema is already
// registered there and is at least as populated as s — so a later, empty
// re-registration of a key (a secondary, unrelated mention in another
// file) never blanks an earlier populated one. This mirrors a defensive
// rule the Python client applied for the same reason.
func mergeSchema(schemas map[string]Schema, s Schema) {
	existing, ok := schemas[s.Key]
	if !ok || (existing.isEmpty() && !s.isEmpty()) {
		schemas[s.Key] = s
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
