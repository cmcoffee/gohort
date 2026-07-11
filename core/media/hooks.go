package media

import "time"

// Injected hooks into services that live in the core hub. media is a pure leaf
// (it must not import core, or the dependency graph cycles), yet its EXIF/GPS
// extractors want to resolve coordinates to a place name — and reverse geocoding
// lives in core because it's wired to the tunables + download subsystem. So core
// injects the implementation at init (see core wiring); media calls through the
// hook, nil-guarded, degrading to raw coordinates when unset.

// ReverseGeocode resolves signed decimal lat/lon to a human place name, or ""
// when it can't. Wired by core at startup. nil until then — GPS coordinates are
// still emitted, just without a resolved location line.
var ReverseGeocode func(lat, lon float64) string

// ExtractTimeout returns the per-document extraction cap. Wired by core to its
// tunable (tune_document_extract_timeout); nil ⇒ DocumentExtractTimeout falls
// back to a 30s default. Kept as a hook because the tunables registry lives in
// core and media is a leaf.
var ExtractTimeout func() time.Duration

// reverseGeocode is the internal nil-safe accessor the extractors call.
func reverseGeocode(lat, lon float64) string {
	if ReverseGeocode == nil {
		return ""
	}
	return ReverseGeocode(lat, lon)
}

// firstNonEmpty returns the first non-blank value, or "". A pure helper local to
// media so the package stays dependency-free (core has its own copy for geocode).
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
