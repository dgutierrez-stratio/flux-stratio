package tenantimport

import (
	"strings"
	"unicode"
)

// camelToKebab converts camelCase to kebab-case, e.g. "pgbackuprepository"
// stays "pgbackuprepository" and "gosecAgentPostgres" becomes
// "gosec-agent-postgres" — used to name a skeleton entry after its
// component key when nothing discovered gives it a real name.
func camelToKebab(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			if unicode.IsLower(prev) || unicode.IsDigit(prev) {
				b.WriteByte('-')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// containsStr reports whether s is present in list.
func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
