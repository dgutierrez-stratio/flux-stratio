package prepare

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Stratio/flux-stratio/internal/log"
)

func TestGenaiSatisfied_AlwaysFalse(t *testing.T) {
	ok, err := genaiSatisfied(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Satisfied = true, want false — this precondition can never be auto-verified")
	}
}

func TestRunGenai_NeverErrorsAndPrintsSQL(t *testing.T) {
	var buf bytes.Buffer
	opts := Options{Log: log.New(&buf, false)}
	if err := runGenai(context.Background(), opts); err != nil {
		t.Fatalf("runGenai returned error: %v", err)
	}
	if !strings.Contains(buf.String(), "UPDATE") || !strings.Contains(buf.String(), "chain_params") {
		t.Errorf("expected the SQL to be logged; got:\n%s", buf.String())
	}
}

func TestRunGenai_NilLoggerDoesNotPanic(t *testing.T) {
	if err := runGenai(context.Background(), Options{}); err != nil {
		t.Fatalf("runGenai returned error: %v", err)
	}
}
