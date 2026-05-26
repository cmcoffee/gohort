package servitor

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const knowledgeTable = "ssh_knowledge"

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
	"System Identity":                      "overview",
	"Hardware":                             "overview",
	"Network":                              "overview",
	"Summary and Notable Findings":         "overview",
	"Services and Ports":                   "services",
	"Service Configuration Details":        "services",
	"Scheduled Jobs":                       "services",
	"SSL/TLS Certificates":                 "services",
	"Inter-Service Communication":          "services",
	"Security Posture":                     "services",
	"Database Access and Connection Strings": "databases",
	"Database Schemas":                     "databases",
	"Application Architecture":             "apps",
	"Application Code Structure":           "apps",
	"Application Deployments":              "apps",
	"Log Files":                            "filesystem",
	"Recent Changes":                       "filesystem",
	"Recent Errors":                        "filesystem",
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
		return strings.TrimSpace(entry.Content), factAgeStr(entry.Updated, now)
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
		doc, ok := sectionToDoc[heading]
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
