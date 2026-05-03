package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/iotimeout"
)

// Offline reverse geocoding for vision-pipeline image/video EXIF coordinates.
// Backed by `cities500.json` from lmfmaier/cities-json — a CC BY 4.0
// re-distribution of the GeoNames cities500 dump, pre-filtered to populated
// places only (PPL/PPLA1-5/PPLC), with admin1 and admin2 already resolved
// to full names rather than GeoNames-internal codes. ~178K records,
// updated periodically. Backed by GitHub raw URLs, which means CDN-served
// downloads — way more reliable than donation-funded download.geonames.org.
//
// We still pull `countryInfo.txt` from GeoNames to translate the ISO
// alpha-2 country codes into full country names ("RO" → "Romania"). It's
// tiny (~10 KB) and rarely fails.
//
// Lookup falls back to Nominatim when the nearest city is too far (open
// ocean, deep wilderness) or when the offline DB couldn't load.

// Attribution: city data derived from the GeoNames Gazetteer
// (https://www.geonames.org/), licensed CC BY 4.0
// (https://creativecommons.org/licenses/by/4.0/), via the lmfmaier/cities-json
// re-distribution (https://github.com/lmfmaier/cities-json).

const (
	citiesDataFile       = "cities500.json"
	geonamesCountryFile  = "countryInfo.txt"
	offlineMaxDistanceKM = 50.0 // farther than this: fall through to Nominatim
	geocodeDownloadUA    = "gohort (https://github.com/cmcoffee/gohort)"

	// geocodeIdleTimeout is the moving-window timeout applied to the
	// download body via iotimeout.NewReadCloser. If no bytes flow for
	// this long, the read errors and we fall to the next mirror. Lets
	// genuinely-fast transfers run uncapped while killing stalled ones.
	geocodeIdleTimeout    = 30 * time.Second
	geocodeConnectTimeout = 10 * time.Second
	geocodeMaxAttempts    = 3
)

// geocodeMirrors lists candidate URLs for each data file in priority order.
// The downloader iterates the list per file; first one to deliver wins.
// GitHub raw URLs are CDN-backed and reliable; GeoNames is here as a last
// resort when the CDN routes fail.
var geocodeMirrors = map[string][]string{
	citiesDataFile: {
		"https://raw.githubusercontent.com/lmfmaier/cities-json/master/cities500.json",
	},
	geonamesCountryFile: {
		"https://download.geonames.org/export/dump/countryInfo.txt",
		"https://www.geonames.org/export/dump/countryInfo.txt",
	},
}

// cityRecord is the in-memory form of one city. cities500.json carries
// admin1/admin2 as full names already, so we don't need a code-to-name
// translation table for subdivisions.
type cityRecord struct {
	Name        string
	CountryCode string // ISO 3166-1 alpha-2 (e.g. "RO")
	Admin1      string // full name (e.g. "Timiș") or "" when not provided
	Lat, Lon    float64
}

type offlineGeocoder struct {
	cities    []cityRecord
	countries map[string]string // "RO" → "Romania"
}

var (
	geocodeDir          string
	offlineGeoOnce      sync.Once
	offlineGeoInstance  *offlineGeocoder
	offlineGeoLoadError error
)

// SetGeocodeDir configures where the offline data files live. Call once at
// startup before any geocoding happens. Files are downloaded on first use
// if absent.
func SetGeocodeDir(dir string) {
	geocodeDir = dir
}

// getOfflineGeocoder returns the lazily-loaded singleton. Subsequent calls
// after a successful load return the same instance immediately.
func getOfflineGeocoder() (*offlineGeocoder, error) {
	offlineGeoOnce.Do(func() {
		if geocodeDir == "" {
			offlineGeoLoadError = fmt.Errorf("geocode dir not configured (SetGeocodeDir not called)")
			return
		}
		offlineGeoInstance, offlineGeoLoadError = loadOfflineGeocoder(geocodeDir)
		if offlineGeoLoadError != nil {
			Debug("[geocode] offline DB unavailable: %s — Nominatim fallback only", offlineGeoLoadError)
		}
	})
	return offlineGeoInstance, offlineGeoLoadError
}

// offlineLookup is the entry point used by reverseGeocode. Returns "" if
// the offline DB isn't loaded or no city is near enough.
func offlineLookup(lat, lon float64) string {
	g, err := getOfflineGeocoder()
	if err != nil || g == nil {
		return ""
	}
	return g.lookup(lat, lon)
}

func loadOfflineGeocoder(dir string) (*offlineGeocoder, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create geocode dir: %w", err)
	}
	if err := ensureGeoFiles(dir); err != nil {
		return nil, fmt.Errorf("ensure files: %w", err)
	}
	g := &offlineGeocoder{
		countries: map[string]string{},
	}
	if err := g.loadCountries(filepath.Join(dir, geonamesCountryFile)); err != nil {
		return nil, fmt.Errorf("countries: %w", err)
	}
	if err := g.loadCities(filepath.Join(dir, citiesDataFile)); err != nil {
		return nil, fmt.Errorf("cities: %w", err)
	}
	Debug("[geocode] offline DB loaded: %d cities, %d countries",
		len(g.cities), len(g.countries))
	return g, nil
}

// lookup returns "<city>, <admin1>, <country>" for the nearest city to
// (lat, lon), or "" when the nearest is farther than offlineMaxDistanceKM.
// Brute-force scan; ~1 ms per query at 178K rows.
func (g *offlineGeocoder) lookup(lat, lon float64) string {
	best := -1
	bestDist := math.MaxFloat64
	for i := range g.cities {
		d := equirectDistKM(lat, lon, g.cities[i].Lat, g.cities[i].Lon)
		if d < bestDist {
			bestDist = d
			best = i
		}
	}
	if best < 0 || bestDist > offlineMaxDistanceKM {
		return ""
	}
	c := g.cities[best]
	var parts []string
	parts = append(parts, c.Name)
	if c.Admin1 != "" && c.Admin1 != c.Name {
		parts = append(parts, c.Admin1)
	}
	if name := g.countries[c.CountryCode]; name != "" {
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

// loadCountries parses GeoNames countryInfo.txt → ISO2 → country name.
// Tab-separated, '#' comments. Column 0 is ISO, column 4 is the country
// name.
func (g *offlineGeocoder) loadCountries(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 5 {
			continue
		}
		iso := strings.TrimSpace(fields[0])
		name := strings.TrimSpace(fields[4])
		if iso != "" && name != "" {
			g.countries[iso] = name
		}
	}
	return scanner.Err()
}

// jsonCity matches the schema published by lmfmaier/cities-json. Numeric
// fields (lat, lon, pop) are stringified in the source for some records,
// so we accept either form via a custom unmarshal helper.
type jsonCity struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Admin1  string `json:"admin1"`
	Admin2  string `json:"admin2"`
	Lat     string `json:"lat"`
	Lon     string `json:"lon"`
	Pop     string `json:"pop"`
}

// loadCities parses cities500.json (~178K records, populated places only,
// admin1/admin2 pre-resolved to names). Records with bad coords are skipped.
func (g *offlineGeocoder) loadCities(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	// Expect a top-level JSON array; stream entries to keep peak memory
	// under control on large files.
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("read opening token: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return fmt.Errorf("expected JSON array at start, got %v", tok)
	}
	for dec.More() {
		var c jsonCity
		if err := dec.Decode(&c); err != nil {
			return fmt.Errorf("decode record: %w", err)
		}
		lat, err1 := strconv.ParseFloat(c.Lat, 64)
		lon, err2 := strconv.ParseFloat(c.Lon, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		g.cities = append(g.cities, cityRecord{
			Name:        c.Name,
			CountryCode: c.Country,
			Admin1:      c.Admin1,
			Lat:         lat,
			Lon:         lon,
		})
	}
	return nil
}

// ensureGeoFiles downloads any missing data files into dir. Skips files
// that already exist; only refreshes on demand (delete the file to force
// re-download). Failure on any single file leaves the offline DB unloaded
// — caller falls back to Nominatim.
//
// On final failure logs a clear manual-download instruction so the operator
// can fall back to wget/curl into the same dir.
func ensureGeoFiles(dir string) error {
	type job struct {
		filename string
		mirrors  []string
	}
	jobs := []job{
		{citiesDataFile, geocodeMirrors[citiesDataFile]},
		{geonamesCountryFile, geocodeMirrors[geonamesCountryFile]},
	}
	for _, j := range jobs {
		dest := filepath.Join(dir, j.filename)
		if _, err := os.Stat(dest); err == nil {
			continue // already present
		}
		var lastErr error
		ok := false
		for attempt := 1; attempt <= geocodeMaxAttempts && !ok; attempt++ {
			for _, url := range j.mirrors {
				Log("[geocode] downloading %s (attempt %d, %s) …", j.filename, attempt, url)
				if err := downloadGeo(dir, j.filename, url); err != nil {
					lastErr = err
					Debug("[geocode] %s failed: %v — trying next mirror", url, err)
					continue
				}
				ok = true
				break
			}
			if !ok && attempt < geocodeMaxAttempts {
				backoff := time.Duration(attempt) * 2 * time.Second
				Debug("[geocode] all mirrors failed on attempt %d, sleeping %s before retry", attempt, backoff)
				time.Sleep(backoff)
			}
		}
		if !ok {
			Log("[geocode] FAILED to download %s — last error: %v", j.filename, lastErr)
			Log("[geocode] manual fallback: place the file in %s by running:", dir)
			for _, url := range j.mirrors {
				Log("[geocode]   curl -o %s %s", filepath.Join(dir, j.filename), url)
			}
			return fmt.Errorf("download %s: %w", j.filename, lastErr)
		}
	}
	return nil
}

// downloadGeo fetches url and writes the body to dir/filename atomically
// (temp file → rename on success). Body reads are wrapped in iotimeout —
// a moving window of geocodeIdleTimeout — so a stalled mirror dies fast
// while genuinely fast transfers run uncapped.
func downloadGeo(dir, filename, url string) error {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   geocodeConnectTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   geocodeConnectTimeout,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		},
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", geocodeDownloadUA)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	body := iotimeout.NewReadCloser(resp.Body, geocodeIdleTimeout)
	defer body.Close()

	tmp, err := os.CreateTemp(dir, ".geo-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, body); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	return os.Rename(tmpPath, filepath.Join(dir, filename))
}

// equirectDistKM is a fast cartesian-flat-earth approximation of the great
// circle distance. ~0.5% error worst case at city-size distances — way
// below our 50 km cutoff threshold. Avoids the trig calls of haversine,
// which matters when scanning ~180K rows per query.
func equirectDistKM(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKM = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	midLat := (lat1 + lat2) / 2 * math.Pi / 180
	x := dLon * math.Cos(midLat)
	return earthRadiusKM * math.Sqrt(x*x+dLat*dLat)
}
