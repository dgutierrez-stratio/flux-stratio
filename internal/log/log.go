// Package log provides flux-stratio's terminal progress output, matching
// the verb/symbol vocabulary of the flux CLI's own logger
// (github.com/fluxcd/flux2 cmd/flux/log.go) and copied from flux-keos's
// internal/log so the two plugins behave identically: a symbol followed by
// a lowercase, present-tense message, one line per call, written to stderr
// so it never mixes with a command's actual result data on stdout. Two
// things are added on top that flux2's logger has no need for: step/command
// tagging (flux2 has no multi-step commands) and a Debugf verb gated behind
// --verbose (flux2's own --verbose flag means something unrelated).
package log

import (
	"fmt"
	"io"
)

// Logger writes one tagged, symbol-prefixed line per call. Construct with
// New; a nil *Logger is also valid and discards everything, so callers that
// don't care about progress output (most tests) can leave a Logger field
// unset instead of wiring up a discard writer.
type Logger struct {
	out     io.Writer
	prefix  string // "[name] ", or "" when no step/command is active
	verbose bool
}

// New returns a Logger writing to out. verbose gates Debugf.
func New(out io.Writer, verbose bool) *Logger {
	return &Logger{out: out, verbose: verbose}
}

// WithStep returns a copy of l whose messages are prefixed "[name] ",
// identifying which step or command produced them.
func (l *Logger) WithStep(name string) *Logger {
	if l == nil {
		return nil
	}
	cp := *l
	cp.prefix = "[" + name + "] "
	return &cp
}

func (l *Logger) printf(symbol, format string, a ...any) {
	if l == nil {
		return
	}
	_, _ = fmt.Fprintln(l.out, symbol, l.prefix+fmt.Sprintf(format, a...))
}

// Actionf reports the start of a unit of work.
func (l *Logger) Actionf(format string, a ...any) { l.printf("►", format, a...) }

// Generatef reports that something was created or written.
func (l *Logger) Generatef(format string, a ...any) { l.printf("✚", format, a...) }

// Waitingf reports polling/blocking on external state. Pair every Waitingf
// call with a later Successf or Failuref once the wait resolves.
func (l *Logger) Waitingf(format string, a ...any) { l.printf("◎", format, a...) }

// Successf reports that a unit of work finished.
func (l *Logger) Successf(format string, a ...any) { l.printf("✔", format, a...) }

// Warningf reports something noteworthy but non-fatal.
func (l *Logger) Warningf(format string, a ...any) { l.printf("⚠️", format, a...) }

// Failuref reports that a unit of work failed. The caller still returns the
// error itself; Failuref only logs it.
func (l *Logger) Failuref(format string, a ...any) { l.printf("✗", format, a...) }

// Debugf prints diagnostic detail (e.g. external command output), only when
// the Logger was constructed with verbose=true. No symbol, matching Helm's
// own debug output style.
func (l *Logger) Debugf(format string, a ...any) {
	if l == nil || !l.verbose {
		return
	}
	_, _ = fmt.Fprintln(l.out, l.prefix+fmt.Sprintf(format, a...))
}
