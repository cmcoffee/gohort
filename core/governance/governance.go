// Package governance holds the administrator's say over what people may add
// for themselves: their own API credentials and their own MCP servers.
//
// Each is one of three policies. Allowed: a person adds their own and it works
// at once. Needs approval: they add it and it waits, off, until an
// administrator approves it. Admin only: the administrator configures these
// and nobody else adds one. An administrator is never held by either policy.
//
// A policy only governs adding a new one. What someone already has keeps
// working, and the administrator turns any of it off from Governance.
//
// The defaults are what each did before there was a policy: people already
// added their own API credentials freely, and a server someone brought in
// already waited for an administrator. A stored value nothing recognises
// reads as Admin only, the closed answer.
package governance

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Policy is who may add one of a kind of personal integration.
type Policy string

const (
	AdminOnly     Policy = "admin"
	NeedsApproval Policy = "approval"
	Allowed       Policy = "allowed"
)

// Kind is a kind of personal integration a policy governs.
type Kind string

const (
	Credentials Kind = "credentials" // a person's own API credentials
	MCPServers  Kind = "mcp_servers" // a person's own MCP servers
)

// Kinds are the kinds a policy governs, in the order the admin page shows.
var Kinds = []Kind{Credentials, MCPServers}

var defaults = map[Kind]Policy{
	Credentials: Allowed,
	MCPServers:  NeedsApproval,
}

// nouns are each kind as a refusal names it.
var nouns = map[Kind]string{
	Credentials: "API credentials",
	MCPServers:  "MCP servers",
}

func key(k Kind) string { return "personal_" + string(k) + "_policy" }

// Default is the policy a kind has until the administrator sets one.
func Default(k Kind) Policy { return defaults[k] }

// Parse reads a stored or submitted policy for k: empty is the default, and
// anything not a policy is Admin only.
func Parse(k Kind, v string) Policy {
	switch Policy(strings.TrimSpace(v)) {
	case "":
		return Default(k)
	case AdminOnly, NeedsApproval, Allowed:
		return Policy(strings.TrimSpace(v))
	}
	return AdminOnly
}

// Of is the administrator's policy for k now.
func Of(k Kind) Policy {
	var v string
	if db := AuthDB(); db != nil {
		db.Get(WebTable, key(k), &v)
	}
	return Parse(k, v)
}

// Set records the administrator's policy for k. A value that is not a policy
// is refused rather than stored, so the page says so instead of it reading as
// Admin only later.
func Set(k Kind, p string) error {
	if _, ok := defaults[k]; !ok {
		return fmt.Errorf("no policy for %q", k)
	}
	switch Policy(p) {
	case AdminOnly, NeedsApproval, Allowed:
	default:
		return fmt.Errorf("policy must be %s, %s or %s", Allowed, NeedsApproval, AdminOnly)
	}
	db := AuthDB()
	if db == nil {
		return fmt.Errorf("settings are not available")
	}
	db.Set(WebTable, key(k), p)
	return nil
}

// Permit says whether user may add a new personal integration of kind k, and
// whether it then waits for an administrator. The error is the refusal, in
// words the person can act on.
func Permit(k Kind, user string) (hold bool, err error) {
	if UserIsAdmin(user) {
		return false, nil
	}
	switch Of(k) {
	case Allowed:
		return false, nil
	case NeedsApproval:
		return true, nil
	}
	return false, fmt.Errorf("your administrator sets up %s on this deployment, and nobody else adds them: ask them to add the one you need", nouns[k])
}
