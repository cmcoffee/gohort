package temptool

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// substituteURL replaces {param} placeholders in a URL template with
// URL-path-encoded arg values. Different from shell quoting —
// placeholders inside path segments must be %-encoded.
func substituteURL(tmpl string, params map[string]ToolParam, required []string, args map[string]any) (string, error) {
	// An OPTIONAL query param whose value wasn't provided should drop out of
	// the URL entirely — otherwise a template like "?sort={sort}&limit={limit}"
	// can never be called without every param, defeating the point of making
	// them optional (observed live: feed's limit/sort couldn't be omitted).
	// Strip whole "[?&]key={name}" segments for a known, NON-required param
	// with no arg; a required or path-position placeholder still errors below.
	reqSet := make(map[string]bool, len(required))
	for _, r := range required {
		reqSet[r] = true
	}
	tmpl = dropAbsentOptionalQuery(tmpl, params, reqSet, args)
	// Placeholders before the first "?" are path-position (keep "/" as a
	// separator); after it they're query-position (fully encoded). qpos is a
	// template offset, so it stays valid as we index into tmpl.
	qpos := strings.IndexByte(tmpl, '?')
	var b strings.Builder
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '{' {
			b.WriteByte(tmpl[i])
			continue
		}
		end := strings.IndexByte(tmpl[i+1:], '}')
		if end < 0 {
			b.WriteByte(tmpl[i])
			continue
		}
		name, modifier := splitPlaceholder(tmpl[i+1 : i+1+end])
		if _, known := params[name]; !known {
			b.WriteByte(tmpl[i])
			continue
		}
		if modifier != "" && !urlEncodeModifiers[modifier] {
			// Refused rather than emitted literally. A template that
			// keeps "{path:enc}" in the URL produces a 404 from the
			// far end, which is the hardest kind of wrong answer to
			// trace back to a typo in a template.
			return "", fmt.Errorf("unknown placeholder modifier %q in {%s:%s} — the only one is \"encoded\" (\"segment\" is a synonym), which percent-encodes the value WHOLE, slashes included, for an API that wants a nested path as one segment",
				modifier, name, modifier)
		}
		val, ok := args[name]
		if !ok {
			return "", fmt.Errorf("missing arg %q", name)
		}
		switch {
		case modifier != "":
			// Explicitly asked for: encode everything, "/" included.
			b.WriteString(urlEscape(stringify(val)))
		case qpos >= 0 && i > qpos:
			b.WriteString(urlEscape(stringify(val)))
		default:
			b.WriteString(urlEscapePath(stringify(val)))
		}
		i = i + 1 + end
	}
	return b.String(), nil
}

// dropAbsentOptionalQuery removes "key={name}" query segments whose {name} is a
// known, non-required param with no provided arg, so an omitted optional query
// param yields a clean URL rather than a "missing arg" error or a literal
// "?limit={limit}". Only touches the query string (after the first "?"), and
// only segments whose value is a single bare "{placeholder}"; anything mixed or
// required is left for the normal substitution/validation to handle.
func dropAbsentOptionalQuery(tmpl string, params map[string]ToolParam, reqSet map[string]bool, args map[string]any) string {
	q := strings.IndexByte(tmpl, '?')
	if q < 0 {
		return tmpl
	}
	path, query := tmpl[:q], tmpl[q+1:]
	segs := strings.Split(query, "&")
	kept := segs[:0]
	for _, seg := range segs {
		if eq := strings.IndexByte(seg, '='); eq >= 0 {
			val := seg[eq+1:]
			if len(val) >= 2 && val[0] == '{' && val[len(val)-1] == '}' &&
				strings.IndexByte(val, '}') == len(val)-1 {
				name, _ := splitPlaceholder(val[1 : len(val)-1])
				if _, known := params[name]; known && !reqSet[name] {
					if _, provided := args[name]; !provided {
						continue // drop this optional, unprovided segment
					}
				}
			}
		}
		kept = append(kept, seg)
	}
	if len(kept) == 0 {
		return path
	}
	return path + "?" + strings.Join(kept, "&")
}

// urlEscape percent-encodes a value for safe inclusion in a URL path
// or query. Encodes everything not in the unreserved set (RFC 3986)
// — conservative, won't double-encode legitimately-encoded values
// because callers should pass raw param values, not pre-encoded ones.
func urlEscape(s string) string {
	return urlEscapeWith(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~")
}

// urlEscapePath is urlEscape but keeps "/" unescaped, so a param that holds a
// multi-segment PATH (e.g. a CalDAV calendar path "/195178399/calendars/home/",
// or "/repos/owner/name") substitutes as real path separators instead of
// "%2F...", which the credential allowlist would then reject as off-base_url.
// Everything else — spaces, "?", "#", unicode — is still encoded, so a value
// can't inject a query string or fragment. Used for path-position placeholders;
// query-position ones stay fully encoded via urlEscape.
func urlEscapePath(s string) string {
	return urlEscapeWith(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~/")
}

func urlEscapeWith(s, safe string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// substituteJSON replaces {param} placeholders in a body template
// with JSON-encoded arg values. Strings get JSON-quoted; numbers/
// bools pass through as-is; objects/arrays serialize structurally.
// This lets the LLM write JSON body templates like
// `{"title": {title}, "labels": {labels}}` and have them serialize
// correctly regardless of the underlying arg type.
//
// Quote-wrap tolerance: if the LLM writes the placeholder wrapped in
// quotes (`"prompt": "{prompt}"`) — the natural JSON shape — strip
// the surrounding quotes from the output, since json.Marshal will
// re-add them for string values. Otherwise the result becomes
// `"prompt": ""hello""` which breaks the JSON. For non-string
// values (numbers, booleans, objects, arrays) the wrap is also
// incorrect on the input side, so we treat the strip as the
// authoritative shape and let json.Marshal produce the right form.
// isJSONContentType reports whether a Content-Type should be treated as JSON
// (the default body path). Empty counts as JSON; anything with "json" in it
// (application/json, application/vnd.api+json) does too. Everything else
// (application/xml, text/xml, text/plain) takes the RAW body path.
func isJSONContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	return ct == "" || strings.Contains(ct, "json")
}

// substituteRaw replaces {name} placeholders with the arg's value inserted
// VERBATIM — no JSON quoting/encoding — for a non-JSON body (XML/SOAP/text).
// A required placeholder with no arg errors; an optional one with no arg drops
// to empty. An unknown {token} is left intact (literal braces in XML survive).
func substituteRaw(tmpl string, params map[string]ToolParam, required []string, args map[string]any) (string, error) {
	reqSet := make(map[string]bool, len(required))
	for _, r := range required {
		reqSet[r] = true
	}
	var out strings.Builder
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '{' {
			out.WriteByte(tmpl[i])
			continue
		}
		end := strings.IndexByte(tmpl[i+1:], '}')
		if end < 0 {
			out.WriteByte(tmpl[i])
			continue
		}
		name := tmpl[i+1 : i+1+end]
		if _, known := params[name]; !known {
			out.WriteByte(tmpl[i]) // not a param — leave the brace, keep scanning
			continue
		}
		val, ok := args[name]
		if !ok {
			if reqSet[name] {
				return "", fmt.Errorf("missing arg %q", name)
			}
			i += end + 1 // optional + absent: drop the whole {name}
			continue
		}
		out.WriteString(rawStringify(val))
		i += end + 1 // advance past the closing '}'
	}
	return out.String(), nil
}

// rawStringify renders an arg value as plain text for a raw body: a string
// as-is, nil as empty, anything else via fmt (a JSON number like 5.0 prints
// "5"). No quoting — the caller controls the surrounding markup.
func rawStringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func substituteJSON(tmpl string, params map[string]ToolParam, required []string, args map[string]any) (string, error) {
	// An OPTIONAL body field whose value wasn't provided should drop out of
	// the JSON entirely — the same guarantee substituteURL gives query params.
	// Without this, a body template like {"title":{title},"url":{url}} can
	// never be posted without a url, so a plain text post fails with a cryptic
	// "body template: missing arg url" (observed live: moltbook's post action,
	// where url is only for link posts). Strip whole "key":{name} properties
	// for a known, NON-required param with no arg; required placeholders still
	// error below.
	reqSet := make(map[string]bool, len(required))
	for _, r := range required {
		reqSet[r] = true
	}
	tmpl = dropAbsentOptionalJSONFields(tmpl, params, reqSet, args)
	var b strings.Builder
	out := []byte{}
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '{' {
			out = append(out, tmpl[i])
			continue
		}
		end := strings.IndexByte(tmpl[i+1:], '}')
		if end < 0 {
			out = append(out, tmpl[i])
			continue
		}
		name := tmpl[i+1 : i+1+end]
		if _, known := params[name]; !known {
			out = append(out, tmpl[i])
			continue
		}
		val, ok := args[name]
		if !ok {
			return "", fmt.Errorf("missing arg %q", name)
		}
		j, err := jsonMarshal(val)
		if err != nil {
			return "", err
		}
		// Quote-wrap detection: if `"{name}"` is the surrounding
		// shape, strip the leading `"` from already-emitted output
		// and skip the trailing `"` in the template input. The
		// substituted JSON value (which may itself start with `"`
		// for strings, or with a non-quote char for numbers/bools/
		// objects) replaces the wrapped placeholder cleanly either
		// way.
		closingQuoteAhead := i+1+end+1 < len(tmpl) && tmpl[i+1+end+1] == '"'
		openingQuoteBehind := len(out) > 0 && out[len(out)-1] == '"'
		if openingQuoteBehind && closingQuoteAhead {
			out = out[:len(out)-1] // drop the leading quote
			out = append(out, j...)
			i = i + 1 + end + 1 // skip placeholder + trailing quote
			continue
		}
		out = append(out, j...)
		i = i + 1 + end
	}
	b.Write(out)
	return b.String(), nil
}

// dropAbsentOptionalJSONFields removes whole "key": {name} object properties
// from a JSON body template when {name} is a known, non-required param with no
// provided arg — so an omitted optional body field yields clean, valid JSON
// rather than a "missing arg" error. It handles the property's adjacent comma
// (whether the field sits first, middle, or last in the object) and tolerates
// a quote-wrapped placeholder ("key": "{name}"). Required params and provided
// optionals are left for normal substitution/validation. Only exact
// "key": {name} shapes are touched; anything else is left intact.
func dropAbsentOptionalJSONFields(tmpl string, params map[string]ToolParam, reqSet map[string]bool, args map[string]any) string {
	for name := range params {
		if reqSet[name] {
			continue
		}
		if _, provided := args[name]; provided {
			continue
		}
		ph := regexp.QuoteMeta("{" + name + "}")
		// The property value is the bare placeholder or a quote-wrapped one.
		prop := `"[A-Za-z0-9_]+"\s*:\s*"?` + ph + `"?`
		// Order matters: consume a trailing comma first (field is first or
		// middle), then a leading comma (field is last), then the lone field
		// (only property in the object).
		tmpl = regexp.MustCompile(prop+`\s*,\s*`).ReplaceAllString(tmpl, "")
		tmpl = regexp.MustCompile(`\s*,\s*`+prop).ReplaceAllString(tmpl, "")
		tmpl = regexp.MustCompile(prop).ReplaceAllString(tmpl, "")
	}
	return tmpl
}

// jsonMarshal is a wrapper around json.Marshal that returns the
// string form. Inlined to avoid pulling json into substituteJSON's
// signature.
func jsonMarshal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
