package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Reverse geocoding turns raw GPS coords from EXIF into a human-readable
// place string ("Timișoara, Timiș, Romania") that we can put in front of
// the vision LLM. Doing the lookup ourselves is the only reliable path —
// even with cardinal-direction coords, smaller-prominence locations cause
// silent confident hallucinations from the model (it confabulates a famous
// landmark in a completely different country instead of admitting it
// doesn't recognize the coordinates).
//
// Results are cached forever in RootDB — coordinates don't move, and
// Nominatim's TOS strongly prefers cached answers over repeated lookups.

const (
	geocodeCacheTable = "geocode_cache"
	nominatimEndpoint = "https://nominatim.openstreetmap.org/reverse"
	geocodeUserAgent  = "gohort (https://github.com/cmcoffee/gohort)"
	geocodeTimeout    = 5 * time.Second
)

// nominatimAddress mirrors the subset of /reverse fields we use.
type nominatimAddress struct {
	City        string `json:"city"`
	Town        string `json:"town"`
	Village     string `json:"village"`
	Hamlet      string `json:"hamlet"`
	Suburb      string `json:"suburb"`
	County      string `json:"county"`
	State       string `json:"state"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
}

type nominatimResponse struct {
	Address     nominatimAddress `json:"address"`
	DisplayName string           `json:"display_name"`
}

// nominatimGate enforces a single in-flight request at a time so we never
// burst past Nominatim's "1 req/sec" usage policy. A mutex is enough for
// a single-process server; high-volume users should self-host an instance.
var nominatimGate sync.Mutex

// reverseGeocode resolves lat/lon to a short place string ("San Francisco,
// California, United States"). Layered:
//  1. kvlite cache by (lat, lon) at 4-decimal precision — permanent.
//  2. Offline GeoNames DB (~150K cities) — fast, no network, no rate limit.
//  3. Nominatim — falls through when the offline DB doesn't recognize the
//     coordinates (open ocean, deep wilderness, sub-village locality).
//
// Returns "" if every layer fails or the caches aren't initialized.
// Errors are silent by design — the caller treats missing as "no info"
// rather than surfacing a network/data failure to the user.
func reverseGeocode(lat, lon float64) string {
	if RootDB == nil {
		return "" // no cache available, skip — avoids hammering the public endpoint from CLI/test runs
	}
	key := fmt.Sprintf("%.4f,%.4f", lat, lon)
	cache := RootDB.Bucket(geocodeCacheTable)
	var cached string
	if cache.Get("", key, &cached) {
		return cached
	}

	// Try offline first — single-digit-ms lookup, no network call.
	if place := offlineLookup(lat, lon); place != "" {
		cache.Set("", key, place)
		return place
	}

	// Fall through to Nominatim for places the offline DB doesn't cover.
	place := fetchNominatim(lat, lon)
	if place == "" {
		return ""
	}
	cache.Set("", key, place)
	return place
}

// fetchNominatim makes the actual HTTP call. Returns "" on any failure.
func fetchNominatim(lat, lon float64) string {
	nominatimGate.Lock()
	defer nominatimGate.Unlock()

	url := fmt.Sprintf("%s?format=jsonv2&lat=%.6f&lon=%.6f&zoom=12&addressdetails=1",
		nominatimEndpoint, lat, lon)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		Debug("[geocode] new request failed: %v", err)
		return ""
	}
	// Nominatim TOS: every client must set a unique User-Agent identifying the app.
	req.Header.Set("User-Agent", geocodeUserAgent)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: geocodeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		Debug("[geocode] request error for %.4f,%.4f: %v", lat, lon, err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		Debug("[geocode] %.4f,%.4f returned status %d", lat, lon, resp.StatusCode)
		return ""
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		Debug("[geocode] read body for %.4f,%.4f failed: %v", lat, lon, err)
		return ""
	}
	var nr nominatimResponse
	if err := json.Unmarshal(body, &nr); err != nil {
		Debug("[geocode] decode failed: %v", err)
		return ""
	}
	place := formatPlace(nr)
	if place != "" {
		Debug("[geocode] %.4f,%.4f → %s", lat, lon, place)
	}
	return place
}

// formatPlace assembles a compact "<locality>, <region>, <country>" string
// from a Nominatim address record, falling back to display_name when no
// usable locality is present (e.g. ocean, remote desert).
func formatPlace(nr nominatimResponse) string {
	a := nr.Address
	locality := firstNonEmpty(a.City, a.Town, a.Village, a.Hamlet, a.Suburb)
	region := ""
	if a.State != "" && a.State != locality {
		region = a.State
	} else if a.County != "" && a.County != locality {
		region = a.County
	}

	var parts []string
	if locality != "" {
		parts = append(parts, locality)
	}
	if region != "" {
		parts = append(parts, region)
	}
	if a.Country != "" {
		parts = append(parts, a.Country)
	}
	if len(parts) == 0 {
		return strings.TrimSpace(nr.DisplayName)
	}
	return strings.Join(parts, ", ")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
