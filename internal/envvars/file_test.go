package envvars

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// TestEncodeDecodeFile_RoundTrips: every value a workload can carry comes
// back exactly — multi-line ones (a PEM certificate) and ones larger than
// bufio.Scanner's 64 KiB default line limit included.
func TestEncodeDecodeFile_RoundTrips(t *testing.T) {
	env := map[string]string{
		"PLAIN":   "value=with=equals",
		"EMPTY":   "",
		"PEM":     "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
		"CRLF":    "a\r\nb",
		"QUOTED":  `"already quoted"`,
		"UNICODE": "ñandú",
		"HUGE":    strings.Repeat("x", 100*1024),
	}
	got, err := DecodeFile(bytes.NewReader(EncodeFile(env)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, env) {
		for k := range env {
			if got[k] != env[k] {
				t.Errorf("%s = %.40q, want %.40q", k, got[k], env[k])
			}
		}
	}
}

// TestDecodeFile_ReadsOldPlainBackups: a backup written before quoting
// still reads back as it was written, without a trailing newline too.
func TestDecodeFile_ReadsOldPlainBackups(t *testing.T) {
	got, err := DecodeFile(strings.NewReader("A=1\n\nB=\"unterminated\nC=last"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": `"unterminated`, "C": "last"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
