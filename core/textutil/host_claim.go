// Shared knowledge: reading a comparable host out of a URL or a URL TEMPLATE,
// and rendering the note that says a tool already covers it.
//
// Beside the JS-heavy domain list and the same-origin hint, and for the same
// reason all three live here: they are facts about URLs that several call
// paths need and none of them owns. Nothing in this file knows what a tool or
// a session is — core supplies those and calls in.
//
// Why the note exists at all: the generic fetch tools are always in the
// catalog and always work, so the model reaches for them even for a host one
// of its purpose-built tools was written to serve. Sometimes the generic fetch
// then comes back technically successful and substantively empty, and nothing
// in the transcript says the wrong door was used.
package textutil

import (
	"fmt"
	"sort"
	"strings"
)

// claimNoteMaxActions caps how many action names the note lists. The point is
// to make the tool findable, not to reproduce a schema the reader already has.
const claimNoteMaxActions = 6

// ToolClaim is one tool that declares URLs on the host asked about.
type ToolClaim struct {
	Tool    string   // the tool's LLM-facing name
	Actions []string // matching action names; empty for a single-endpoint tool
}

// HostKey extracts the comparable host from a URL or a URL template, or ""
// when there isn't one to compare.
//
// Templates carry {arg} placeholders. One in the path is irrelevant here, but
// one in the HOST ("https://{region}.api.example.com/…") means the host is not
// known until dispatch — those compare as nothing rather than as something
// wrong.
//
// "www." is stripped, matching how IsJSHeavyDomain normalizes, so a tool
// written against the apex host still covers the www one and vice versa.
func HostKey(raw string) string {
	raw = strings.TrimSpace(raw)
	// Split by hand rather than with url.Parse: a placeholder can contain
	// characters that make Parse fail or, worse, succeed with a host nobody
	// meant.
	i := strings.Index(raw, "://")
	// The scheme must BEGIN the string, not merely appear in it. A command
	// line ("curl https://example.com/x") contains a URL without being one,
	// and treating it as one would let a shell tool claim a host on the
	// strength of an argument.
	if i <= 0 || !isSchemeToken(raw[:i]) {
		return ""
	}
	host := raw[i+3:]
	if j := strings.IndexAny(host, "/?#"); j >= 0 {
		host = host[:j]
	}
	if at := strings.LastIndex(host, "@"); at >= 0 { // strip userinfo
		host = host[at+1:]
	}
	if c := strings.LastIndex(host, ":"); c >= 0 && !strings.Contains(host[c:], "]") {
		host = host[:c] // strip port; the bracket check spares IPv6
	}
	if host == "" || strings.ContainsAny(host, "{}") {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(host), "www.")
}

// ClaimNote renders the advisory for a generic fetch of a claimed host, or ""
// when nothing claims it.
//
// Advisory, deliberately, and not a route: refusing is right for a credential,
// because sending unauthenticated is actively harmful, but fetching a claimed
// host's ordinary web page is not — someone may legitimately want to read an
// article on a site whose API a tool wraps. So the fetch proceeds and the note
// rides along, landing in the round where it is actionable.
func ClaimNote(host string, claims []ToolClaim) string {
	if host == "" || len(claims) == 0 {
		return ""
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].Tool < claims[j].Tool })
	parts := make([]string, 0, len(claims))
	for _, c := range claims {
		if len(c.Actions) == 0 {
			parts = append(parts, fmt.Sprintf("%q", c.Tool))
			continue
		}
		shown, extra := c.Actions, ""
		if len(shown) > claimNoteMaxActions {
			extra = fmt.Sprintf(", +%d more", len(shown)-claimNoteMaxActions)
			shown = shown[:claimNoteMaxActions]
		}
		parts = append(parts, fmt.Sprintf("%q (%s%s)", c.Tool, strings.Join(shown, ", "), extra))
	}
	return fmt.Sprintf("\n\n[Note: %s already covers %s and returns its data directly. "+
		"Prefer it over fetching pages from this host — it sees things a page fetch cannot.]",
		joinWithAnd(parts), host)
}

// joinWithAnd renders a list as "a", "a and b", "a, b and c".
func joinWithAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

// isSchemeToken reports whether s is a URL scheme: a letter followed by
// letters, digits, "+", "-" or ".". RFC 3986 section 3.1.
func isSchemeToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}
