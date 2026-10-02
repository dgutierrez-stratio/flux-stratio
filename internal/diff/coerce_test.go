package diff

import "testing"

func TestCoerceValue(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"", ""},
		{"true", true},
		{"True", "True"}, // a bool would render back as "true"
		{"TRUE", "TRUE"},
		{"false", false},
		{"False", "False"},
		{"3", 3},
		{"0", 0},
		{"-5", -5},
		{"0755", "0755"}, // an int would render back as 755
		{"0022", "0022"},
		{"007", "007"},
		{"+1", "+1"},
		{"-0", "-0"},
		{"1.5", "1.5"},
		{"yes", "yes"},
		{"null", "null"},
		{"~", "~"},
		{"hello", "hello"},
		{"999999", 999999},
		{"1000000", "1000000"}, // helm-controller would render it 1e+06
		{"-1000000", "-1000000"},
	}
	for _, c := range cases {
		got := CoerceValue(c.in)
		if got != c.want {
			t.Errorf("CoerceValue(%q) = %v (%T), want %v (%T)", c.in, got, got, c.want, c.want)
		}
	}
}
