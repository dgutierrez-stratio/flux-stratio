package components

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Stratio/flux-stratio/internal/ui"
)

// ErrNoAnswer is returned by a Prompter that can't ask (NonInteractive), or
// whose input ran out before a valid answer was given.
var ErrNoAnswer = errors.New("no answer given")

// ErrSkipped is returned for an instance the operator chose to leave out
// of an `apps ... --all` run. It isn't a failure: the run moves on.
var ErrSkipped = errors.New("skipped at the prompt")

// Prompter asks the operator to choose one of several options, returning
// the chosen index.
type Prompter interface {
	Choose(question string, options []string) (int, error)
}

// NonInteractive never asks: every Choose returns ErrNoAnswer, so a
// resolution that needs an answer fails with an error naming `--as`
// instead of blocking — what `apps migrate --yes` uses.
type NonInteractive struct{}

// Choose implements Prompter.
func (NonInteractive) Choose(string, []string) (int, error) { return 0, ErrNoAnswer }

// Terminal asks on Out (stderr, in production — stdout is reserved for
// result data) and reads a 1-based choice from In, re-asking on an invalid
// answer. EOF, or a blank line, is ErrNoAnswer rather than a default pick,
// matching internal/appmigrate.Confirm's "silence never means yes" rule.
type Terminal struct {
	in  io.Reader
	out io.Writer
}

// NewTerminal builds a Terminal prompter reading in and writing out. It
// reads in unbuffered (ui.ReadLine), so it never takes answers meant for
// a later appmigrate.Confirm on the same stdin.
func NewTerminal(in io.Reader, out io.Writer) *Terminal {
	return &Terminal{in: in, out: out}
}

// maxAttempts bounds how many invalid answers Terminal tolerates before
// giving up, so garbage piped into stdin can't loop forever.
const maxAttempts = 3

// Choose implements Prompter.
func (t *Terminal) Choose(question string, options []string) (int, error) {
	_, _ = fmt.Fprintf(t.out, "%s\n", question)
	for i, o := range options {
		_, _ = fmt.Fprintf(t.out, "  %d) %s\n", i+1, o)
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		_, _ = fmt.Fprintf(t.out, "Choose [1-%d]: ", len(options))
		line, err := ui.ReadLine(t.in)
		answer := strings.TrimSpace(line)
		if answer == "" {
			if err != nil && !errors.Is(err, io.EOF) {
				return 0, fmt.Errorf("reading answer: %w", err)
			}
			return 0, ErrNoAnswer
		}
		if n, convErr := strconv.Atoi(answer); convErr == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		_, _ = fmt.Fprintf(t.out, "%q is not one of 1-%d\n", answer, len(options))
		if err != nil {
			return 0, ErrNoAnswer
		}
	}
	return 0, ErrNoAnswer
}
