// What this leaf needs from the hub: a store and an id generator. core's
// Database satisfies Store structurally, so callers pass what they already
// have. Member de-duplication comes from core/toolgroups, which owns that
// rule for tool groups and is imported directly — a leaf may depend on
// another leaf, only never on core.

package appgroups

type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

// NewID returns a fresh unique ID. Wired by core to UUIDv4.
var NewID = func() string { return "" }
