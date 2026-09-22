package diff

import (
	"strconv"
	"strings"
)

// CoerceValue converts a live cluster's raw string value into the Go type
// it should render as in a patch's YAML: "" stays a string; "true"/"false"
// (case-insensitive) become bool; a base-10-integer-shaped string becomes
// int; everything else (floats, "yes", "null", "~", ...) stays a string.
//
// Leaving floats and YAML-1.1-ambiguous words ("yes", "null", "~") as
// strings is deliberate: gopkg.in/yaml.v3 is YAML-1.2 throughout, so this
// plugin never hits the hazard the Python client's PyYAML (YAML 1.1)
// dumper had with an unquoted "yes" — but staying a plain string keeps
// output identical however the target value is later consumed.
func CoerceValue(v string) any {
	if v == "" {
		return v
	}
	switch strings.ToLower(v) {
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return v
}
