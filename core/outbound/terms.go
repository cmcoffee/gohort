package outbound

// What a query must not carry out of the deployment.
//
// A query leaves for a search engine someone else runs, and what must not go
// with it depends on the work: a private project's own name, its repository,
// a ticket ID, an internal host. A fixed list cannot know the first two, so
// the place that starts the work names what is private in it
// (WithPrivateTerms), and every query leaving for an outside service is
// checked against that and a few patterns that are rarely safe to send
// anywhere (Leak).
//
// The topic is not the leak, the name is. "kubernetes operator reconcile
// backoff" is a fine search from inside a private kubernetes tool; that
// tool's own name in the query is not. So the check matches names, never
// subjects, and leaves the agent free to search the public concepts its
// work is built on.

import (
	"context"
	"net"
	"regexp"
	"strings"
)

type privateTermsKey struct{}

// WithPrivateTerms returns ctx naming terms as private to the work it runs:
// words a web search made from it should not send. Added to any already on
// ctx, so a dispatched run keeps its parent's.
func WithPrivateTerms(ctx context.Context, terms ...string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	all := append([]string(nil), PrivateTerms(ctx)...)
	seen := map[string]bool{}
	for _, t := range all {
		seen[normalizeSearchText(t)] = true
	}
	for _, t := range terms {
		n := normalizeSearchText(t)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		all = append(all, strings.TrimSpace(t))
	}
	return context.WithValue(ctx, privateTermsKey{}, all)
}

// PrivateTerms are the terms ctx names as private, nil for none.
func PrivateTerms(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(privateTermsKey{}).([]string)
	return t
}

// CarryPrivateTerms puts src's private terms on dst, for a run detached from
// the request that started it (its context rooted afresh), which would
// otherwise lose them.
func CarryPrivateTerms(dst, src context.Context) context.Context {
	if t := PrivateTerms(src); len(t) > 0 {
		return WithPrivateTerms(dst, t...)
	}
	return dst
}

// genericNameWords are words a project is often named with that say nothing
// about whose project it is. A term made only of them ("Demo service", "api")
// names nothing private, and matching it would flag ordinary searches.
var genericNameWords = map[string]bool{
	"app": true, "apps": true, "api": true, "web": true, "site": true, "website": true,
	"demo": true, "test": true, "tests": true, "example": true, "sample": true,
	"service": true, "services": true, "server": true, "client": true, "project": true,
	"backend": true, "frontend": true, "docs": true, "main": true, "core": true,
	"tool": true, "tools": true, "my": true, "new": true, "the": true, "repo": true,
	"src": true, "lib": true, "library": true, "cli": true, "bot": true, "data": true,
}

// normalizeSearchText lowercases s and turns every run of separators into one
// space, so "acme-control", "acme_control" and "Acme Control" read the same.
func normalizeSearchText(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127 {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
			continue
		}
		space = true
	}
	return b.String()
}

// namesSomething reports whether a normalized term could identify anything:
// at least one of its words is not generic, and it is not a short single
// word, which would match inside ordinary queries.
func namesSomething(term string) bool {
	words := strings.Fields(term)
	if len(words) == 1 && len(term) < 4 {
		return false
	}
	for _, w := range words {
		if !genericNameWords[w] {
			return true
		}
	}
	return false
}

// ticketIDRE is an issue tracker's key: a project code, a hyphen, a number
// (ABC-123). publicCodes are prefixes of that shape that name public things.
var (
	ticketIDRE  = regexp.MustCompile(`\b([A-Z][A-Z0-9]{1,9})-([0-9]{1,6})\b`)
	publicCodes = map[string]bool{
		"CVE": true, "CWE": true, "RFC": true, "ISO": true, "IEC": true, "SHA": true, "UTF": true,
		"MD": true, "AES": true, "TLS": true, "SSL": true, "HTTP": true, "COVID": true, "GPT": true,
		"IEEE": true, "ECMA": true, "ES": true, "PEP": true, "JSR": true, "KB": true, "ANSI": true,
		"RTX": true, "GTX": true, "USB": true, "PCI": true, "DDR": true, "LTE": true, "MP": true,
		"CSS": true, "ID": true, "WCAG": true, "NIST": true, "SP": true, "FIPS": true, "OWASP": true,
	}
	hostRE = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.(?:local|internal|intranet|corp|lan|home\.arpa)\b`)
	ipRE   = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
)

// Leak names what in query should not go to an outside service, "" when
// nothing does: one of terms (the work's private names), something shaped
// like an issue tracker's ticket ID, or an internal host or address.
//
// The reason is written to finish "this query ...", for a log line or a
// refusal the agent reads and rewrites its search from.
func Leak(query string, terms []string) string {
	q := " " + normalizeSearchText(query) + " "
	for _, t := range terms {
		n := normalizeSearchText(t)
		if n == "" || !namesSomething(n) {
			continue
		}
		if strings.Contains(q, " "+n+" ") {
			return "names \"" + strings.TrimSpace(t) + "\", which is private to this work"
		}
	}
	for _, m := range ticketIDRE.FindAllStringSubmatch(query, -1) {
		if !publicCodes[m[1]] {
			return "carries what looks like a ticket ID (" + m[0] + ")"
		}
	}
	if h := hostRE.FindString(query); h != "" {
		return "names an internal host (" + h + ")"
	}
	for _, a := range ipRE.FindAllString(query, -1) {
		if ip := net.ParseIP(a); ip != nil && (ip.IsPrivate() || ip.IsLoopback()) {
			return "carries an internal address (" + a + ")"
		}
	}
	return ""
}
