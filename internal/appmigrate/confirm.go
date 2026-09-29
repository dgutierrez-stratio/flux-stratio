package appmigrate

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Confirm prints prompt to out — the narration stream, never stdout, which
// carries data (diffs, manifests) a caller may be redirecting — and reads a
// line from in, returning true only for
// an explicit y/yes answer (case-insensitive). Any other input — including
// a blank line or EOF — is treated as "no", so an unattended run never
// silently proceeds past this gate. Copied from flux-keos's own
// internal/migrate.Confirm so both plugins share the same confirmation
// semantics.
func Confirm(in io.Reader, out io.Writer, prompt string) (bool, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return false, fmt.Errorf("writing confirmation prompt: %w", err)
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF { //nolint:errorlint // matching flux-keos's own Confirm exactly
		return false, fmt.Errorf("reading confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
