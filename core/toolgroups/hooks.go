// The slice of a key/value database this leaf uses, plus the ID generator.
// core's Database satisfies Store structurally.

package toolgroups

type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

// NewID returns a fresh unique ID. Wired by core to UUIDv4.
var NewID = func() string { return "" }
