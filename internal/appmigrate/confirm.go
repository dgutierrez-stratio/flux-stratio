package appmigrate

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Confirm prints prompt and reads a line from in, returning true only for
// an explicit y/yes answer (case-insensitive). Any other input — including
// a blank line or EOF — is treated as "no", so an unattended run never
// silently proceeds past this gate. Copied from flux-keos's own
// internal/migrate.Confirm so both plugins share the same confirmation
// semantics.
func Confirm(in io.Reader, prompt string) (bool, error) {
	fmt.Print(prompt)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF { //nolint:errorlint // matching flux-keos's own Confirm exactly
		return false, fmt.Errorf("reading confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
