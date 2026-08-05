// What the source-hook engine needs from the hub, and nothing more.
//
// The engine itself is self-contained — fetch a source, cache it, index it,
// search it — but four facts about the deployment live in core: the mail
// identity two polite-pool APIs require, the app version for a User-Agent,
// the table its network config sits in, and the maintenance registry a purge
// action hangs off. All four arrive as hooks with defaults, so the package
// builds and behaves standalone.

package sourcehooks

import (
	"context"
	"time"
)

type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	// CryptSet stores an encrypted value — a source hook may carry an API key.
	CryptSet(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

var (
	// ContactEmail is the deployment's From address. OpenAlex asks for a
	// mailto for polite-pool access and SEC EDGAR requires a contact in the
	// User-Agent, and both fall back to a placeholder when it isn't set.
	ContactEmail func() string
	// AppVersion rides in the User-Agent EDGAR requires.
	AppVersion func() string
	// RegisterMaintenance publishes the cache-purge action on the admin
	// maintenance surface.
	RegisterMaintenance func(key, label, desc string, fn func(ctx context.Context) int)
	// NetworkConfigTable is where network config is stored. Defaulted so the
	// leaf works unwired; core assigns its own const over it so the table name
	// has one source of truth.
	NetworkConfigTable = "network_config"
)

func contactEmail() string {
	if ContactEmail == nil {
		return ""
	}
	return ContactEmail()
}

func appVersion() string {
	if AppVersion == nil {
		return "dev"
	}
	return AppVersion()
}

func registerMaintenance(key, label, desc string, fn func(ctx context.Context) int) {
	if RegisterMaintenance == nil {
		return
	}
	RegisterMaintenance(key, label, desc, fn)
}

// authKey mirrors the hub's authorization key format. Copied rather than
// hooked: it is one string concatenation, and a hook for it would be more
// machinery than the rule it carries.
func authKey(owner, id string) string { return owner + ":" + id }

// RequestTimeout and ConnectTimeout read the operator-configured HTTP budgets.
// Accessors rather than the vars themselves, because ApplyHTTPTimeouts rewrites
// those at runtime from stored config — a var re-exported through core.go would
// be a copy taken at init and would never see the change.
func RequestTimeout() time.Duration { return HTTPRequestTimeout }
func ConnectTimeout() time.Duration { return HTTPConnectTimeout }
