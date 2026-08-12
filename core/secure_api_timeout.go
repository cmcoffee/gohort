// Per-call wall-clock overrides for governed dispatch.
package core

// secureTimeoutSeconds reads a per-call timeout override out of the dispatch
// args, in seconds. Zero means "no override — use the general cap".
//
// Tolerant of the numeric type because args maps arrive from several places
// (hand-built by a connector, decoded from JSON where every number is a
// float64), and a silently ignored override is the failure mode this whole
// change exists to remove.
func secureTimeoutSeconds(args map[string]any) int {
	switch v := args[secureTimeoutArg].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}
