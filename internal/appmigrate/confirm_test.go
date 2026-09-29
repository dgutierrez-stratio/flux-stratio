package appmigrate

import (
	"strings"
	"testing"
)

func TestConfirm(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"  yes  \n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false},
		{"", false}, // EOF, no newline at all
		{"garbage\n", false},
	}
	for _, c := range cases {
		var out strings.Builder
		got, err := Confirm(strings.NewReader(c.input), &out, "Proceed? ")
		if out.String() != "Proceed? " {
			t.Errorf("Confirm(%q) wrote %q to out, want the prompt", c.input, out.String())
		}
		if err != nil {
			t.Errorf("Confirm(%q) returned error: %v", c.input, err)
			continue
		}
		if got != c.want {
			t.Errorf("Confirm(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}
