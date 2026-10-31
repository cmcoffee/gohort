package service

// Web search's Brave, Serper, Google and SearXNG engines were written in Go,
// before every capability came from a connector: a provider name, a key and
// an endpoint in the settings. This moves a setting that named one onto a
// service connector once, at startup, so there is one way a search is made:
// the connector is the template gohort ships for that engine, holding the key
// the settings used, and can be read, tested, fixed and replaced.
//
// What stays: DuckDuckGo needs Go to read its HTML, and moves when the built-in
// transports do; a peer's search moves with the peers; a Source Hook as the
// engine is a connector already.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// searchTemplates are the templates the engines that were written in Go move
// to.
var searchTemplates = map[string]string{
	"brave": "brave-search", "serper": "serper-search", "google": "google-search", "searxng": "searxng-search",
}

// MigrateWebSearch moves Web Search's Brave, Serper, Google or SearXNG engine
// onto a service connector, once (Admin > Migrations shows it ran). It needs
// the credential store, for the key.
func MigrateWebSearch(db Database) {
	NewMigrationRunner("core", "").Once("web_search_to_connector:v1", func() int {
		changed, err := migrateWebSearch(db)
		if err != nil {
			// Recorded on the marker, where Admin > Migrations shows it.
			panic(fmt.Sprintf("web search was not moved to a connector: %v", err))
		}
		return changed
	})
}

// migrateWebSearch is MigrateWebSearch's work: a local setting naming one of
// the engines, with what that engine needs (a key, or SearXNG's address),
// becomes its template, installed as an approved connector, and the setting
// names it. One missing what it needs never made a search, and is left as it
// is: there is no off for search, and moving it would send searches to an
// engine nobody chose. It reports how many records it changed.
func migrateWebSearch(db Database) (int, error) {
	if db == nil {
		return 0, nil
	}
	var provider, key, endpoint, source string
	db.Get(SearchTable, "provider", &provider)
	db.Get(SearchTable, "api_key", &key)
	db.Get(SearchTable, "endpoint", &endpoint)
	db.Get(SearchTable, "source", &source)
	if s := strings.TrimSpace(source); s != "" && s != "local" {
		return 0, nil // a peer searches: it moves with the peers
	}
	provider, key = strings.TrimSpace(provider), strings.TrimSpace(key)
	id, ok := searchTemplates[provider]
	if !ok {
		return 0, nil // DuckDuckGo, a Source Hook, or a connector already
	}
	answers := map[string]string{"api_key": key}
	switch provider {
	case "google":
		// Kept as key:cx: the API key, then the search engine's ID.
		k, cx, _ := strings.Cut(key, ":")
		key, answers = strings.TrimSpace(k), map[string]string{"api_key": strings.TrimSpace(k), "cx": strings.TrimSpace(cx)}
		if answers["cx"] == "" {
			key = ""
		}
	case "searxng":
		answers = map[string]string{"base_url": strings.TrimSpace(endpoint)}
		if answers["base_url"] == "" {
			Log("[migration] web search names SearXNG with no address, so it made no searches: it is left as it is until an engine is chosen under Web Search")
			return 0, nil
		}
	}
	if key == "" && provider != "searxng" {
		Log("[migration] web search names %s with no key, so it made no searches: it is left as it is until an engine is chosen under Web Search", provider)
		return 0, nil
	}
	name, changed, err := installMoved(db, id, answers, key, "Web Search")
	if err != nil {
		return changed, err
	}
	db.Set(SearchTable, "provider", media.ServiceProviderPrefix+name)
	db.Unset(SearchTable, "api_key")
	db.Unset(SearchTable, "endpoint")
	changed++
	Log("[migration] web search now comes from connector %s (the %s template, as the settings had it)", name, provider)
	return changed, nil
}
