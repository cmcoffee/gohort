package core

import "testing"

// A city that is itself an IANA zone name resolves without an alias entry: a
// chat agent asked for the time in Cairo, Lima and Reykjavik lost a round to
// each before retrying with the full name.
func TestResolveZoneFindsACityUnderItsRegion(t *testing.T) {
	for city, want := range map[string]string{
		"Cairo": "Africa/Cairo", "lima": "America/Lima", "Reykjavik": "Atlantic/Reykjavik",
		"nairobi": "Africa/Nairobi", "Mexico City": "America/Mexico_City", "hong kong": "Asia/Hong_Kong",
		"Tokyo": "Asia/Tokyo", "America/New_York": "America/New_York",
	} {
		_, iana, err := ResolveZone(city)
		if err != nil || iana != want {
			t.Errorf("%q resolved to %q (%v), want %q", city, iana, err, want)
		}
	}
	if _, _, err := ResolveZone("Atlantis"); err == nil {
		t.Error("a city that is no zone must still be unknown")
	}
}
