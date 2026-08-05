// The slice of a key/value database the channel records need. core's Database
// satisfies it structurally, so callers keep passing the handle they have.

package messaging

type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}
