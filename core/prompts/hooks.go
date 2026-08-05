// The slice of a key/value database this leaf uses. core's Database
// satisfies it structurally, so callers pass the handle they already have.

package prompts

type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
}

// OverrideTable is the main-DB table deployment-level prompt overrides live
// in. Defaulted here so the leaf works unwired; core assigns its own WebTable
// const over it at init so the name has one source of truth.
var OverrideTable = "web_config"
