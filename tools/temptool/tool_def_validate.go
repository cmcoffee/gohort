package temptool

import (
	"regexp"
	"sort"
	"strings"
)

// pathPlaceholderMsg is the authoring error for an optional PATH placeholder.
// Shared by the single-api-tool and toolbox-action create paths so both refuse
// the same shape with the same guidance.
const pathPlaceholderMsg = "param(s) %v are interpolated into the url_template's PATH but are not required — url substitution has nothing to put there when they're omitted, so the call dies at dispatch with `url template: missing arg \"%s\"`. Either add them to required, or move them to the query string (\"?key={%s}\"), where an omitted placeholder legitimately drops out of the URL"

// placeholderRE matches a {param} interpolation in a URL or body template.
// The optional ":modifier" tail is part of the placeholder syntax (see
// urlEncodeModifiers), so it must be part of every pattern that looks for
// one. Group 1 stays the bare name.
var placeholderRE = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)(?::[A-Za-z_]+)?\}`)

// templateReferences reports whether tmpl interpolates param — with or
// without an encoding modifier.
//
// Every "is this param wired in?" check was an exact strings.Contains for
// "{name}", so adding {name:encoded} made three of them believe the
// param was referenced nowhere. The tool worked; the VALIDATOR failed it,
// printing "live GET returned 200" and "required param appears in neither
// template" in the same report. A checker that contradicts itself is
// worse than one that says nothing, because the author has to work out
// which half to trust.
func templateReferences(tmpl, param string) bool {
	if param == "" {
		return false
	}
	return strings.Contains(tmpl, "{"+param+"}") || strings.Contains(tmpl, "{"+param+":")
}

// pathPlaceholderParams returns the params a url_template interpolates into its
// PATH — the segment before any "?" — that are not in required.
//
// A path placeholder cannot be optional: url-template substitution has nothing
// to put there when the arg is absent, so the call dies with `url template:
// missing arg "uid"` at DISPATCH time, long after authoring, with an error that
// reads like a framework bug rather than a spec mistake. (Seen live on an
// iCloud CalDAV create tool whose "{uid}.ics" filename param was declared
// optional — the model then invented a uid to get past it.)
//
// QUERY placeholders are deliberately excluded: "?since={cursor}" legitimately
// drops out of the URL when omitted, which is the documented optional-query
// behavior.
func pathPlaceholderParams(urlTpl string, required []string) []string {
	path := urlTpl
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	req := map[string]bool{}
	for _, r := range required {
		req[r] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range placeholderRE.FindAllStringSubmatch(path, -1) {
		name := m[1]
		if name == "" || req[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// unsentWriteParams returns the required params of a WRITE action (POST/PUT/
// PATCH/DELETE) that appear in neither the url_template nor the body_template
// — meaning the API never receives them. Scoped to write methods so it never
// blocks the legitimate GET "_"-placeholder pattern (a dummy required param
// that satisfies an API demanding some query arg). Reads rarely carry a
// required body field, so the risk there isn't worth the false-positive.
func unsentWriteParams(method, urlTpl, bodyTpl string, required []string) []string {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "POST", "PUT", "PATCH", "DELETE":
	default:
		return nil
	}
	var out []string
	for _, r := range required {
		if !templateReferences(urlTpl, r) && !templateReferences(bodyTpl, r) {
			out = append(out, r)
		}
	}
	return out
}

// cloneArgs returns a shallow copy so dispatch-side canonicalization can
// rewrite keys without mutating the caller's sample map.
func cloneArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// oneLine collapses whitespace/newlines and truncates for a compact,
// single-line report cell.
func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
