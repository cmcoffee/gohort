// The root database, injected. pushsub can't import core (that would close an
// import cycle), and the handle isn't available until startup — so it arrives
// as a func the hub fills in. Nil until wired, same as RootDB is nil before
// the database opens.

package migrate

type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

// DB returns the root store. Wired by core.
var DB func() Store

func rootDB() Store {
	if DB == nil {
		return nil
	}
	return DB()
}
