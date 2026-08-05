// The slice of a key/value database this leaf uses. core's Database satisfies
// it structurally, so callers pass the handle they already have.

package looptune

type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
}
