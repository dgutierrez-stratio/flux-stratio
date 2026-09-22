package diff

import "testing"

func TestCoerceValue(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"", ""},
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"false", false},
		{"False", false},
		{"3", 3},
		{"0", 0},
		{"-5", -5},
		{"0755", 755}, // matches the documented Python quirk faithfully, not "fixed" into octal
		{"1.5", "1.5"},
		{"yes", "yes"},
		{"null", "null"},
		{"~", "~"},
		{"hello", "hello"},
	}
	for _, c := range cases {
		got := CoerceValue(c.in)
		if got != c.want {
			t.Errorf("CoerceValue(%q) = %v (%T), want %v (%T)", c.in, got, got, c.want, c.want)
		}
	}
}
