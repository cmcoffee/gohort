package servitor

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const factsTable = "ssh_facts"
const techniquesTable = "ssh_techniques"

// SshFact is a discrete piece of knowledge stored about an appliance.
// Facts are keyed by (appliance_id, key) and overwrite on re-store,
// so re-mapping a system keeps the knowledge base current.
type SshFact struct {
	ID            string   `json:"id"`            // applianceID + ":" + key
	ApplianceID   string   `json:"appliance_id"`
	ApplianceName string   `json:"appliance_name"`
	Key           string   `json:"key"`
	Value         string   `json:"value"`
	Tags          []string `json:"tags,omitempty"`
	Updated       string   `json:"updated"`        // RFC3339
	TTL           string   `json:"ttl,omitempty"` // "short" (30min) or "long" (24h); default "long"
}

// storeFact writes a fact to udb, overwriting any previous value for the same key.
// ttl is "short" (30min) or "long" (24h); empty defaults to "long".
func storeFact(udb Database, applianceID, applianceName, key, value, ttl string, tags []string) {
	if udb == nil || applianceID == "" || key == "" {
		return
	}
	if ttl != "short" && ttl != "long" {
		ttl = "long"
	}
	dbKey := applianceID + ":" + key
	udb.Set(factsTable, dbKey, SshFact{
		ID:            dbKey,
		ApplianceID:   applianceID,
		ApplianceName: applianceName,
		Key:           key,
		Value:         value,
		Tags:          tags,
		TTL:           ttl,
		Updated:       time.Now().Format(time.RFC3339),
	})
}

// factsForAppliance returns all stored facts for one appliance.
func factsForAppliance(udb Database, applianceID string) []SshFact {
	if udb == nil {
		return nil
	}
	prefix := applianceID + ":"
	var out []SshFact
	for _, k := range udb.Keys(factsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var f SshFact
		if udb.Get(factsTable, k, &f) {
			out = append(out, f)
		}
	}
	return out
}

// searchFacts does a case-insensitive substring search across all facts in
// udb, optionally filtered to a specific appliance name or ID substring.
func searchFacts(udb Database, query, applianceFilter string) []SshFact {
	if udb == nil {
		return nil
	}
	q := strings.ToLower(query)
	af := strings.ToLower(applianceFilter)
	var out []SshFact
	for _, k := range udb.Keys(factsTable) {
		var f SshFact
		if !udb.Get(factsTable, k, &f) {
			continue
		}
		if af != "" {
			if !strings.Contains(strings.ToLower(f.ApplianceName), af) &&
				!strings.Contains(strings.ToLower(f.ApplianceID), af) {
				continue
			}
		}
		if strings.Contains(strings.ToLower(f.Key), q) ||
			strings.Contains(strings.ToLower(f.Value), q) {
			out = append(out, f)
			continue
		}
		for _, tag := range f.Tags {
			if strings.Contains(strings.ToLower(tag), q) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// recordTechnique appends a successful technique to the per-appliance techniques list.
// Techniques describe HOW to do things on this specific system — successful approaches,
// correct command syntax, working auth methods, non-standard paths. Unlike lessons
// (which record mistakes to avoid), techniques are positive playbooks for future sessions.
func recordTechnique(udb Database, applianceID, technique string) {
	if udb == nil || applianceID == "" || strings.TrimSpace(technique) == "" {
		return
	}
	var existing string
	udb.Get(techniquesTable, applianceID, &existing)
	entry := fmt.Sprintf("- %s (%s)\n", strings.TrimSpace(technique), time.Now().Format("2006-01-02"))
	udb.Set(techniquesTable, applianceID, existing+entry)
}

// techniquesFor returns the stored techniques string for an appliance, or "".
func techniquesFor(udb Database, applianceID string) string {
	if udb == nil {
		return ""
	}
	var s string
	udb.Get(techniquesTable, applianceID, &s)
	return strings.TrimSpace(s)
}

// formatFacts renders a fact list as compact lines for injection into a prompt.
func formatFacts(facts []SshFact) string {
	return formatFactsWithAge(facts, time.Time{})
}

// factAgeStr returns a human-readable age for an RFC3339 timestamp relative to now.
// Returns "" if now is zero or the timestamp is unparseable.
func factAgeStr(updated string, now time.Time) string {
	if now.IsZero() {
		return ""
	}
	t, err := time.Parse(time.RFC3339, updated)
	if err != nil {
		return ""
	}
	age := now.Sub(t)
	switch {
	case age < 2*time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%d min ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%d hr ago", int(age.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(age.Hours()/24))
	}
}

// factTTLDuration returns the TTL duration for a fact.
func factTTLDuration(ttl string) time.Duration {
	if ttl == "short" {
		return 30 * time.Minute
	}
	return 24 * time.Hour
}

// formatFactsWithAge renders a fact list with per-fact age annotations.
// Facts past their TTL are annotated with [STALE — re-verify].
// Pass time.Time{} (zero) to omit ages and staleness checks.
func formatFactsWithAge(facts []SshFact, now time.Time) string {
	if len(facts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range facts {
		tags := ""
		if len(f.Tags) > 0 {
			tags = " [" + strings.Join(f.Tags, ", ") + "]"
		}
		age := ""
		stale := ""
		if s := factAgeStr(f.Updated, now); s != "" {
			age = " (" + s + ")"
		}
		if !now.IsZero() && f.Updated != "" {
			if t, err := time.Parse(time.RFC3339, f.Updated); err == nil {
				if now.Sub(t) > factTTLDuration(f.TTL) {
					stale = " [STALE — re-verify]"
				}
			}
		}
		fmt.Fprintf(&b, "• [%s] %s: %s%s%s%s\n", f.ApplianceName, f.Key, f.Value, tags, age, stale)
	}
	return b.String()
}

// --- Discoveries ---
// Discoveries represent key breakthroughs: solved goals, verified access, critical findings.
// They sit above techniques and facts in the knowledge tier and are surfaced first in prompts.

const discoveriesTable = "ssh_discoveries"

// SshDiscovery records a significant finding that directly answers an investigation goal.
type SshDiscovery struct {
	ID          string `json:"id"`
	ApplianceID string `json:"appliance_id"`
	Title       string `json:"title"`    // one-line summary
	Finding     string `json:"finding"`  // full narrative with evidence
	Category    string `json:"category"` // database | credentials | routing | service | code | security | config | general
	Timestamp   string `json:"timestamp"`
}

func storeDiscovery(udb Database, applianceID, title, finding, category string) {
	if udb == nil || applianceID == "" || strings.TrimSpace(title) == "" {
		return
	}
	if category == "" {
		category = "general"
	}
	id := applianceID + ":" + time.Now().Format("20060102-150405.000")
	udb.Set(discoveriesTable, id, SshDiscovery{
		ID:          id,
		ApplianceID: applianceID,
		Title:       strings.TrimSpace(title),
		Finding:     strings.TrimSpace(finding),
		Category:    category,
		Timestamp:   time.Now().Format(time.RFC3339),
	})
}

func discoveriesFor(udb Database, applianceID string) []SshDiscovery {
	if udb == nil {
		return nil
	}
	prefix := applianceID + ":"
	var out []SshDiscovery
	for _, k := range udb.Keys(discoveriesTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var d SshDiscovery
		if udb.Get(discoveriesTable, k, &d) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	return out
}

func formatDiscoveries(discoveries []SshDiscovery) string {
	if len(discoveries) == 0 {
		return ""
	}
	var b strings.Builder
	for _, d := range discoveries {
		cat := d.Category
		if cat == "" {
			cat = "general"
		}
		fmt.Fprintf(&b, "### [%s] %s\n%s\n\n", cat, d.Title, d.Finding)
	}
	return strings.TrimSpace(b.String())
}
