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
	if n, err := strconv.Atoi(v); err == nil && -maxPlainInt < n && n < maxPlainInt {
		return n
	}
	return v
}

// maxPlainInt bounds the integers CoerceValue writes as numbers. helm-
// controller hands a HelmRelease's values to Helm as JSON, so every
// number arrives as a float64, and a template prints a float64 of a
// million or more in exponent form: facadeCacheSize: 1000000 rendered
// rocket's SPARTA_PLUGIN_FACADE_CACHE_SIZE as "1e+06". Kept a string, it
// renders unchanged (charts quote such defaults for the same reason).
const maxPlainInt = 1_000_000
