package prepare

import (
	"strings"
	"testing"
)

func TestGenaiInstructionsForTenant(t *testing.T) {
	sql := stepGenAI.InstructionsFor("stratio")
	for _, want := range []string{`UPDATE "genai-api.stratio-genai".chain`, "chain_params", `"genai-gateway.stratio-genai".endpoint`} {
		if !strings.Contains(sql, want) {
			t.Errorf("instructions lack %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "<tenant>") {
		t.Errorf("instructions still carry the <tenant> placeholder:\n%s", sql)
	}
}
