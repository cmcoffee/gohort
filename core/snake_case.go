// SnakeFromDisplay normalizes a display name into a snake_case
// identifier. Used wherever a user-authored label needs a
// machine-stable slug (admin tool-group names, exposed-agent URL
// slugs, etc.). Lowercase + word-character-only + underscore-
// separated; trims leading / trailing / repeated separators so the
// output is round-trip-safe across renames.
//
// (Lived in tool_group_rewriter.go before that file was retired.
// Kept as a standalone helper because exposed-agent slugging in
// orchestrate still uses it.)

package core

import (
	"strings"
	"unicode"
)

// SnakeFromDisplay turns "My Cool Tool Group" → "my_cool_tool_group".
// Non-alphanumerics collapse to underscores; runs collapse to one;
// leading / trailing separators are trimmed. Empty input returns "".
func SnakeFromDisplay(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSep := true // treat the leading edge as separator so we trim
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			prevSep = false
		default:
			if !prevSep {
				b.WriteByte('_')
				prevSep = true
			}
		}
	}
	return strings.TrimRight(b.String(), "_")
}
