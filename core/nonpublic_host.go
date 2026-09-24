// One implementation of "is this URL allowed to leave the machine".
package core

import (
	"fmt"
	"net"
	neturl "net/url"
	"strings"

	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// RefuseNonPublicHost rejects a URL that names loopback, private, link-local or
// unspecified address space, or a hostname reserved for local resolution.
//
// This check existed in three places by copy-paste — the LLM-callable
// browse_page tool, the sandbox hook's browse_page, and now the peer endpoint —
// and the third copy is what made it worth consolidating rather than pasting
// again. The peer case is also the one where getting it wrong costs the most:
// a serving instance usually sits INSIDE a network the caller cannot reach, so
// a browse endpoint without this is not a convenience, it is a hole punched
// through to the LAN for anyone holding a peer key.
//
// Hostnames are checked literally here, which is the fast first answer. The
// address actually dialled is checked by NewPublicHTTPClient, which is what
// closes a name that resolves inward and a DNS answer that changes between
// the check and the connect.
func RefuseNonPublicHost(rawURL string) error {
	parsed, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("url must be an http:// or https:// URL")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("url has no host")
	}
	if ip := net.ParseIP(host); ip != nil && sourcehooks.NonPublicIP(ip) {
		return fmt.Errorf("refusing to reach non-public host: %s", host)
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
		return fmt.Errorf("refusing to reach non-public host: %s", host)
	}
	return nil
}
