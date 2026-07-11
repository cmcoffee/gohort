package geo

import "time"

// Injected hooks into core services this leaf can't import. geo is a pure
// package (it must not import core, or the graph cycles); core wires these at
// startup (see core.go). Each has a standalone default so the package works in
// tests / CLI when core hasn't wired it.

// CacheBucket is the minimal permanent kv cache ReverseGeocode memoizes
// coordinate→place lookups in. core wires it to RootDB's geocode bucket.
type CacheBucket interface {
	Get(sub, key string, out any) bool
	Set(sub, key string, value any)
}

var (
	// Cache returns the permanent geocode cache bucket, or nil when unavailable
	// (no RootDB — a CLI/test run), in which case ReverseGeocode skips entirely
	// rather than hammering the public Nominatim endpoint.
	Cache func() CacheBucket

	// Tunable values, wired to the tunables registry (defaults mirror the
	// registered tunable defaults so behavior is identical when unwired).
	HTTPTimeout    func() time.Duration // per online (Nominatim) request
	IdleTimeout    func() time.Duration // offline-DB download moving-window no-bytes
	ConnectTimeout func() time.Duration // offline-DB download dial + TLS
	MaxAttempts    func() int           // offline-DB download mirror attempts
)

func cacheBucket() CacheBucket {
	if Cache == nil {
		return nil
	}
	return Cache()
}

func geocodeTimeout() time.Duration {
	if HTTPTimeout != nil {
		return HTTPTimeout()
	}
	return 5 * time.Second
}

func geocodeIdleTimeout() time.Duration {
	if IdleTimeout != nil {
		return IdleTimeout()
	}
	return 30 * time.Second
}

func geocodeConnectTimeout() time.Duration {
	if ConnectTimeout != nil {
		return ConnectTimeout()
	}
	return 10 * time.Second
}

func geocodeMaxAttempts() int {
	if MaxAttempts != nil {
		return MaxAttempts()
	}
	return 3
}
