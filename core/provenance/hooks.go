// Tunable lookups, injected. The tunables registry lives in the hub and can't
// follow this leaf out; these accessors read through the hooks core wires,
// falling back to the same defaults the registry declares.

package provenance

var (
	// TuneFloatFunc / TuneIntFunc read a registered tunable by key.
	TuneFloatFunc func(key string) float64
	TuneIntFunc   func(key string) int
)

func tuneFloat(key string) float64 {
	if TuneFloatFunc == nil {
		return 0
	}
	return TuneFloatFunc(key)
}

func tuneInt(key string) int {
	if TuneIntFunc == nil {
		return 0
	}
	return TuneIntFunc(key)
}
