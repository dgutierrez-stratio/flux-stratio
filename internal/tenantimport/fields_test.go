package tenantimport

import (
	"reflect"
	"testing"

	"github.com/Stratio/flux-stratio/internal/catalog"
	"github.com/Stratio/flux-stratio/internal/config"
)

func TestFieldPlan(t *testing.T) {
	local := []string{"local"}
	chat := []catalog.ExtraConfigField{{Key: "chat", HasDefault: true}}
	for name, tc := range map[string]struct {
		field       catalog.ExtraConfigField
		storageType string
		required    map[string]bool
		want        fieldAction
	}{
		"scalar default is written":             {catalog.ExtraConfigField{Key: "a", HasDefault: true, Default: "false", Bare: true}, "", nil, fieldDefault},
		"no default is reported":                {catalog.ExtraConfigField{Key: "a"}, "", nil, fieldWarn},
		"dict default is optional":              {catalog.ExtraConfigField{Key: "agent", Container: "dict"}, "", nil, fieldSkip},
		"list default is optional":              {catalog.ExtraConfigField{Key: "indices", Container: "list"}, "", nil, fieldSkip},
		"required container is reported":        {catalog.ExtraConfigField{Key: "llmModels", Container: "dict"}, "", map[string]bool{"llmModels": true}, fieldWarn},
		"required dict with keys is scaffolded": {catalog.ExtraConfigField{Key: "llmModels", Container: "dict", Leaves: chat}, "", map[string]bool{"llmModels": true}, fieldScaffold},
		"required list is reported":             {catalog.ExtraConfigField{Key: "models", Container: "list"}, "", map[string]bool{"models": true}, fieldWarn},
		"optional dict with keys is skipped":    {catalog.ExtraConfigField{Key: "backup", Container: "dict", Leaves: chat}, "", nil, fieldSkip},
		"other storage type is skipped":         {catalog.ExtraConfigField{Key: "storageClassName", StorageTypes: local}, "s3", nil, fieldSkip},
		"no storage type is skipped":            {catalog.ExtraConfigField{Key: "storageClassName", StorageTypes: local}, "", nil, fieldSkip},
		"matching storage type is reported":     {catalog.ExtraConfigField{Key: "storageClassName", StorageTypes: local}, "local", nil, fieldWarn},
		"storage type beats a required list":    {catalog.ExtraConfigField{Key: "x", Container: "dict", StorageTypes: local}, "s3", map[string]bool{"x": true}, fieldSkip},
	} {
		if got := fieldPlan(tc.field, tc.storageType, tc.required); got != tc.want {
			t.Errorf("%s: fieldPlan = %v, want %v", name, got, tc.want)
		}
	}
}

func TestRequiredConfig_SeedFallback(t *testing.T) {
	for name, cfg := range map[string]*config.Catalog{
		"no catalog": nil,
		"old catalog without the field": {Types: []config.ComponentType{
			{Type: "genai", Component: "genai"}, {Type: "litellm", Component: "litellm"},
		}},
	} {
		got := requiredConfig(cfg)
		if !got["genai"]["llmModels"] || !got["litellm"]["models"] {
			t.Errorf("%s: requiredConfig = %v, want the seed's genai/llmModels and litellm/models", name, got)
		}
	}
	own := requiredConfig(&config.Catalog{Types: []config.ComponentType{{Type: "genai", Component: "genai", RequiredConfig: []string{"other"}}}})
	if !own["genai"]["other"] || own["genai"]["llmModels"] {
		t.Errorf("requiredConfig = %v, want the catalog's own list to win over the seed's", own)
	}
}

func TestScaffoldValue(t *testing.T) {
	field := catalog.ExtraConfigField{Key: "backup", Container: "dict", Leaves: []catalog.ExtraConfigField{
		{Key: "enabled", HasDefault: true, Default: "false", Bare: true},
		{Key: "type", HasDefault: true, Default: "s3"},
		{Key: "storageClassName"},
	}}
	want := map[string]any{"enabled": false, "type": "s3", "storageClassName": ""}
	if got := scaffoldValue(field); !reflect.DeepEqual(got, want) {
		t.Errorf("scaffoldValue = %#v, want %#v", got, want)
	}
}
