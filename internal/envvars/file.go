package envvars

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// EncodeFile renders env as a backup's env-vars file: sorted "KEY=VALUE"
// lines. A value a plain line can't carry — one with a newline or carriage
// return (a PEM certificate, multi-line JSON), or one starting with a
// double quote, which would read back as quoted — is written Go-quoted
// (strconv.Quote) instead, so DecodeFile gives back exactly env.
func EncodeFile(env map[string]string) []byte {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	for _, k := range keys {
		v := env[k]
		if strings.ContainsAny(v, "\n\r") || strings.HasPrefix(v, `"`) {
			v = strconv.Quote(v)
		}
		fmt.Fprintf(&buf, "%s=%s\n", k, v)
	}
	return buf.Bytes()
}

// DecodeFile reads what EncodeFile wrote. A value that starts with a
// double quote is unquoted; one that doesn't unquote cleanly (a backup
// written before values were quoted) is kept as written. Lines have no
// length limit.
func DecodeFile(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		if key, value, ok := strings.Cut(line, "="); ok && line != "" {
			if strings.HasPrefix(value, `"`) {
				if unquoted, uerr := strconv.Unquote(value); uerr == nil {
					value = unquoted
				}
			}
			out[key] = value
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}
