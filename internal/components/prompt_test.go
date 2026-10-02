package components

import (
	"io"
	"strings"
	"testing"

	"github.com/Stratio/flux-stratio/internal/appmigrate"
)

// TestTerminal_LeavesLaterAnswersOnStdin: a type choice and the confirmations
// after it share one stdin; the Terminal must take only its own line.
func TestTerminal_LeavesLaterAnswersOnStdin(t *testing.T) {
	in := strings.NewReader("2\ny\n")
	got, err := NewTerminal(in, io.Discard).Choose("which?", []string{"a", "b"})
	if err != nil || got != 1 {
		t.Fatalf("Choose = %d, %v; want 1, nil", got, err)
	}
	ok, err := appmigrate.Confirm(in, io.Discard, "Proceed? ")
	if err != nil || !ok {
		t.Errorf("Confirm after Choose = %v, %v; want true (the piped y)", ok, err)
	}
}
