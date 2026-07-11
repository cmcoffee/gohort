package servitor

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const knowledgeTable = "ssh_knowledge"

// docStaleAfter is how long a knowledge document is trusted before the lead is
// warned to re-verify it. Systems and repos drift; a doc older than this is
// flagged "STALE, re-verify" wherever its age is surfaced so the LLM does not
// act on a months-old service inventory or code map as if it were current.
const docStaleAfter = 14 * 24 * time.Hour

// knowledgeDocNames are the structured documents the lead maintains per appliance.
// Each doc is a focused markdown file the lead reads from and writes to.
var knowledgeDocNames = []string{"overview", "databases", "filesystem", "services", "apps"}

// cliMapsForAppliance returns all CLI map docs stored for an appliance.
// CLI maps are stored under the key "<applianceID>:cli:<cmd>" in knowledgeTable.
// Returns a map of command name → (age-annotated) content.
func cliMapsForAppliance(udb Database, applianceID string) map[string]string {
	if udb == nil {
		return nil
	}
	prefix := applianceID + ":cli:"
	now := time.Now()
	out := make(map[string]string)
	for _, k := range udb.Keys(knowledgeTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		cmd := strings.TrimPrefix(k, prefix)
		var entry KnowledgeDocEntry
		if udb.Get(knowledgeTable, k, &entry) && entry.Content != "" {
			age := factAgeStr(entry.Updated, now)
			if age != "" {
				out[cmd] = fmt.Sprintf("[Last updated: %s]\n\n%s", age, entry.Content)
			} else {
				out[cmd] = entry.Content
			}
		}
	}
	return out
}

// sectionToDoc maps ## headings from the map profile to the five knowledge doc names.
var sectionToDoc = map[string]string{
	"System Identity":                        "overview",
	"Hardware":                               "overview",
	"Network":                                "overview",
	"Summary and Notable Findings":           "overview",
	"Services and Ports":                     "services",
	"Service Configuration Details":          "services",
	"Scheduled Jobs":                         "services",
	"SSL/TLS Certificates":                   "services",
	"Inter-Service Communication":            "services",
	"Security Posture":                       "services",
	"Database Access and Connection Strings": "databases",
	"Database Schemas":                       "databases",
	"Application Architecture":               "apps",
	"Application Code Structure":             "apps",
	"Application Deployments":                "apps",
	"Log Files":                              "filesystem",
	"Recent Changes":                         "filesystem",
	"Recent Errors":                          "filesystem",
}

// sortedSectionKeys returns the sectionToDoc headings in a stable order so fuzzy
// matching is deterministic when two known headings tie on score.
func sortedSectionKeys() []string {
	keys := make([]string, 0, len(sectionToDoc))
	for k := range sectionToDoc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// normalizeHeading strips case and non-alphanumerics so "System Identity & Purpose"
// and "System Identity" compare closer.
func normalizeHeading(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// headingTokens returns the set of significant (len >= 3) words in a heading,
// used for token-overlap matching. Short filler words ("and", "of") are dropped.
func headingTokens(s string) map[string]bool {
	out := make(map[string]bool)
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}) {
		if len(f) >= 3 {
			out[f] = true
		}
	}
	return out
}

// matchSectionDoc maps a profile heading to a knowledge doc, tolerant of wording
// drift from the synthesizing LLM. It tries, in order: exact match, normalized
// (punctuation/case-insensitive) match, then token-overlap — so a heading like
// "System Identity & Purpose" still routes to "overview" instead of being
// silently dropped. Returns ("", false) when nothing matches with confidence.
func matchSectionDoc(heading string) (string, bool) {
	if doc, ok := sectionToDoc[heading]; ok {
		return doc, true
	}
	nh := normalizeHeading(heading)
	keys := sortedSectionKeys()
	for _, k := range keys {
		if normalizeHeading(k) == nh {
			return sectionToDoc[k], true
		}
	}
	ht := headingTokens(heading)
	if len(ht) == 0 {
		return "", false
	}
	bestDoc := ""
	bestScore := 0.0
	for _, k := range keys {
		kt := headingTokens(k)
		if len(kt) == 0 {
			continue
		}
		shared := 0
		for t := range ht {
			if kt[t] {
				shared++
			}
		}
		if shared == 0 {
			continue
		}
		// Score over the smaller token set so a subset heading (fewer words) still
		// scores high against its fuller canonical form.
		denom := len(kt)
		if len(ht) < denom {
			denom = len(ht)
		}
		if score := float64(shared) / float64(denom); score > bestScore {
			bestScore = score
			bestDoc = sectionToDoc[k]
		}
	}
	if bestScore >= 0.5 {
		return bestDoc, true
	}
	return "", false
}

// docIsStale reports whether a doc's last-updated timestamp is older than
// docStaleAfter. A zero now, empty timestamp, or parse failure is treated as
// not stale (no false alarms on legacy plain-string docs).
func docIsStale(updated string, now time.Time) bool {
	if now.IsZero() || updated == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, updated)
	if err != nil {
		return false
	}
	return now.Sub(t) > docStaleAfter
}

// KnowledgeDocEntry is the stored form of a knowledge document, including a
// last-updated timestamp so stale docs can be surfaced to the lead LLM.
type KnowledgeDocEntry struct {
	Content string `json:"content"`
	Updated string `json:"updated"` // RFC3339
}

// writeDoc persists a knowledge document with the current timestamp.
func writeDoc(udb Database, applianceID, doc, content string) {
	if udb == nil || applianceID == "" || doc == "" {
		return
	}
	udb.Set(knowledgeTable, applianceID+":"+doc, KnowledgeDocEntry{
		Content: strings.TrimSpace(content),
		Updated: time.Now().Format(time.RFC3339),
	})
}

// readDoc fetches the content of a single knowledge document.
// Returns "" when not present. Handles both the new struct format and the
// legacy plain-string format transparently.
func readDoc(udb Database, applianceID, doc string) string {
	if udb == nil {
		return ""
	}
	var entry KnowledgeDocEntry
	if udb.Get(knowledgeTable, applianceID+":"+doc, &entry) && entry.Content != "" {
		return strings.TrimSpace(entry.Content)
	}
	// Legacy fallback: plain string stored before KnowledgeDocEntry was introduced.
	var s string
	udb.Get(knowledgeTable, applianceID+":"+doc, &s)
	return strings.TrimSpace(s)
}

// readDocWithAge fetches a document and returns its content plus a human-readable
// age string (e.g. "3 days ago"). age is "" when the timestamp is unavailable.
func readDocWithAge(udb Database, applianceID, doc string, now time.Time) (content, age string) {
	if udb == nil {
		return "", ""
	}
	var entry KnowledgeDocEntry
	if udb.Get(knowledgeTable, applianceID+":"+doc, &entry) && entry.Content != "" {
		age = factAgeStr(entry.Updated, now)
		if docIsStale(entry.Updated, now) {
			if age != "" {
				age += " — STALE, re-verify"
			} else {
				age = "STALE, re-verify"
			}
		}
		return strings.TrimSpace(entry.Content), age
	}
	var s string
	udb.Get(knowledgeTable, applianceID+":"+doc, &s)
	return strings.TrimSpace(s), ""
}

// allDocs returns every existing knowledge document for an appliance, annotated
// with a "[Last updated: X]" header when freshness data is available.
func allDocs(udb Database, applianceID string) map[string]string {
	if udb == nil {
		return nil
	}
	now := time.Now()
	out := make(map[string]string)
	for _, name := range knowledgeDocNames {
		content, age := readDocWithAge(udb, applianceID, name, now)
		if content == "" {
			continue
		}
		if age != "" {
			out[name] = fmt.Sprintf("[Last updated: %s]\n\n%s", age, content)
		} else {
			out[name] = content
		}
	}
	return out
}

// parseProfileSections splits a markdown profile on "## " headings and returns a heading→content map.
func parseProfileSections(profile string) map[string]string {
	out := make(map[string]string)
	lines := strings.Split(profile, "\n")
	var cur string
	var buf strings.Builder
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if cur != "" {
				out[cur] = strings.TrimSpace(buf.String())
			}
			cur = strings.TrimPrefix(line, "## ")
			buf.Reset()
		} else if cur != "" {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	if cur != "" {
		out[cur] = strings.TrimSpace(buf.String())
	}
	return out
}

// extractDocsFromProfile parses a completed map profile and populates the five structured
// knowledge docs so the lead has immediate context on the next chat session.
func extractDocsFromProfile(udb Database, applianceID, profile string) {
	if udb == nil || applianceID == "" || profile == "" {
		return
	}
	sections := parseProfileSections(profile)
	docBuilders := make(map[string]*strings.Builder, len(knowledgeDocNames))
	for _, name := range knowledgeDocNames {
		docBuilders[name] = &strings.Builder{}
	}
	for heading, content := range sections {
		if content == "" {
			continue
		}
		doc, ok := matchSectionDoc(heading)
		if !ok {
			continue
		}
		docBuilders[doc].WriteString("## ")
		docBuilders[doc].WriteString(heading)
		docBuilders[doc].WriteString("\n\n")
		docBuilders[doc].WriteString(content)
		docBuilders[doc].WriteString("\n\n")
	}
	for _, name := range knowledgeDocNames {
		if s := strings.TrimSpace(docBuilders[name].String()); s != "" {
			writeDoc(udb, applianceID, name, s)
		}
	}
}
