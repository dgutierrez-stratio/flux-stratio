package ui

import "io"

// ReadLine reads one line of an operator's answer from in, a byte at a
// time, never reading past its newline: one command asks several
// questions on the same stdin (warnings, apply, a prepare step, a type
// choice), and a buffered reader per question would swallow the answers
// piped in for the later ones, which would then read EOF — a "no". The
// line comes back without its newline; io.EOF comes back with whatever
// was read before it.
func ReadLine(in io.Reader) (string, error) {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := in.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				return string(line), nil
			}
			line = append(line, b[0])
		}
		if err != nil {
			return string(line), err
		}
	}
}
