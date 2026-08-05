// What promotion needs from the hub. The tunables REGISTRY stays in core —
// it owns the admin surface and the defaults — so this leaf reads values
// through hooks and core makes the two RegisterTunable calls on its behalf.
// Each accessor falls back to the same default the registry declares, so an
// unwired binary behaves rather than reading zero.

package promotion

import "time"

type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

var (
	// TuneDurationFunc / TuneIntFunc read a registered tunable by key.
	TuneDurationFunc func(key string) time.Duration
	TuneIntFunc      func(key string) int
)

// errString keeps this package free of a dependency on core's error type; the
// original lives beside the temp-tool store and stayed there.
type errString string

func (e errString) Error() string { return string(e) }
