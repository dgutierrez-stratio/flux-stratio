package diff

import "testing"

func envValues(env any) map[string]any {
	return map[string]any{"controllers": map[string]any{"rocket": map[string]any{"containers": map[string]any{"app": map[string]any{"env": env}}}}}
}

func TestShadowedDefault(t *testing.T) {
	defaults := envValues([]any{
		map[string]any{"name": "SERVICE_ACCOUNT_NAME", "value": "x"},
		map[string]any{"name": "ROCKET_POD_NAME"},
	})
	listed := envValues([]any{map[string]any{"name": "SERVICE_ACCOUNT_NAME", "value": "x"}})
	w := workload("a", nil, container("app", "r/i:1", nil, nil))

	for name, tt := range map[string]struct {
		defaults, merged map[string]any
		workload         bool
		varName, want    string
	}{
		"hidden by a replacing list":      {defaults, listed, true, "ROCKET_POD_NAME", "controllers.rocket.containers.app.env"},
		"still in the merged list":        {defaults, defaults, true, "ROCKET_POD_NAME", ""},
		"a variable the chart never sets": {defaults, listed, true, "OTHER", ""},
		"map-form defaults":               {envValues(map[string]any{"ROCKET_POD_NAME": "x"}), envValues(map[string]any{}), true, "ROCKET_POD_NAME", "controllers.rocket.containers.app.env"},
		"flat live side":                  {defaults, listed, false, "ROCKET_POD_NAME", ""},
		"no defaults":                     {nil, listed, true, "ROCKET_POD_NAME", ""},
	} {
		t.Run(name, func(t *testing.T) {
			wl := w
			if !tt.workload {
				wl = nil
			}
			if got := shadowedDefault(tt.defaults, tt.merged, wl, tt.varName); got != tt.want {
				t.Errorf("shadowedDefault = %q, want %q", got, tt.want)
			}
		})
	}
}
