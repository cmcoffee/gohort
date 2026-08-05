// The root database, injected — same shape as the pushsub leaf. Nil until the
// hub opens the database, which is the state sub_session.go already handled
// when it read RootDB directly.

package subsession

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
