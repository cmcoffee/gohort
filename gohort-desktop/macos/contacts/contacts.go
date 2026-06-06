//go:build darwin

// Package contacts resolves macOS Address Book handles (phone/email)
// to display names, and exposes that lookup as a gohort-desktop tool
// (contacts.lookup) so server-side agents can call it over the WS
// tool bridge. Migrated from apps/phantom/_bridge/main.go.
package contacts

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"github.com/cmcoffee/snugforge/nfo"
	_ "modernc.org/sqlite"
)

// init registers the contacts tools so the daemon announces them over
// the WS tool bridge: contacts.lookup (handle -> name) and its reverse,
// contacts.search (name -> handles). The message service shares the same
// underlying lookup impl.
func init() {
	core.RegisterTool(new(lookupTool))
	core.RegisterTool(new(searchTool))
}

// Lookup returns the display name for a phone number or email address,
// or "" if unknown. Thin exported wrapper over the cached lookup so
// the message service (macos/imsg) and the LLM tool share one path.
func Lookup(handle string) string { return lookupContact(handle) }

// lookupTool exposes Lookup as a core.Tool (contacts.lookup).
type lookupTool struct{}

func (lookupTool) Name() string { return "contacts.lookup" }
func (lookupTool) Desc() string {
	return "Resolve a phone number or email address to the contact's display name from the local macOS Address Book. Use when you have a handle (phone/email) and need the person's name. Returns an empty result if the handle isn't in Contacts."
}
func (lookupTool) Params() map[string]core.ToolParam {
	return map[string]core.ToolParam{
		"handle": {Type: "string", Description: "Phone number or email address to resolve."},
	}
}
func (lookupTool) Required() []string { return []string{"handle"} }
func (lookupTool) Enabled() bool      { return true }
func (lookupTool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		handle, _ := args["handle"].(string)
		name := Lookup(handle)
		if name == "" {
			return "", nil
		}
		return name, nil
	}
}

// ContactRecord is one Address Book person with their reachable handles.
// Returned by Search (name -> handles), the reverse of Lookup.
type ContactRecord struct {
	Name   string   `json:"name"`
	Phones []string `json:"phones,omitempty"`
	Emails []string `json:"emails,omitempty"`
}

// Search returns Address Book contacts whose display name contains query
// (case-insensitive), best matches first (exact, then prefix, then
// substring), up to limit results. It's the reverse of Lookup: name in,
// reachable handles out. Shared by the LLM tool and any caller that needs
// to resolve a name to a phone/email.
func Search(query string, limit int) []ContactRecord {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = 10
	}

	contactsMu.RLock()
	built := contactsBuilt
	contactsMu.RUnlock()
	if !built {
		buildContactsCache()
	}

	contactsMu.RLock()
	defer contactsMu.RUnlock()

	type scored struct {
		rec   ContactRecord
		score int
	}
	var hits []scored
	for _, rec := range contactRecords {
		name := strings.ToLower(rec.Name)
		switch {
		case name == q:
			hits = append(hits, scored{rec, 0})
		case strings.HasPrefix(name, q):
			hits = append(hits, scored{rec, 1})
		case strings.Contains(name, q):
			hits = append(hits, scored{rec, 2})
		}
	}
	// Stable sort by match quality; contactRecords is already name-sorted,
	// so ties preserve alphabetical order.
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score < hits[j].score })
	out := make([]ContactRecord, 0, limit)
	for _, h := range hits {
		out = append(out, h.rec)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// searchTool exposes Search as a core.Tool (contacts.search).
type searchTool struct{}

func (searchTool) Name() string { return "contacts.search" }
func (searchTool) Desc() string {
	return "Search the local macOS Address Book by contact name (full or partial) and return matching people with their phone numbers and email addresses. Use when you have a name (e.g. \"mom\", \"John Smith\") and need a handle to text or email them. This is the reverse of contacts.lookup. Returns an empty result if no name matches."
}
func (searchTool) Params() map[string]core.ToolParam {
	return map[string]core.ToolParam{
		"name":  {Type: "string", Description: "Full or partial contact name to search for."},
		"limit": {Type: "integer", Description: "Maximum number of matches to return (default 10)."},
	}
}
func (searchTool) Required() []string { return []string{"name"} }
func (searchTool) Enabled() bool      { return true }
func (searchTool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		query, _ := args["name"].(string)
		limit := 10
		switch v := args["limit"].(type) {
		case float64:
			if v > 0 {
				limit = int(v)
			}
		case int:
			if v > 0 {
				limit = v
			}
		}
		recs := Search(query, limit)
		if len(recs) == 0 {
			return "", nil
		}
		return formatContacts(recs), nil
	}
}

// formatContacts renders search hits as one readable line per contact:
//
//	Mom | phones: +15551234567 | emails: mom@example.com
func formatContacts(recs []ContactRecord) string {
	var b strings.Builder
	for i, r := range recs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(r.Name)
		if len(r.Phones) > 0 {
			b.WriteString(" | phones: " + strings.Join(r.Phones, ", "))
		}
		if len(r.Emails) > 0 {
			b.WriteString(" | emails: " + strings.Join(r.Emails, ", "))
		}
		if len(r.Phones) == 0 && len(r.Emails) == 0 {
			b.WriteString(" | (no phone or email on file)")
		}
	}
	return b.String()
}

var (
	contactsMu     sync.RWMutex
	contactsCache  map[string]string // normalised phone/email → display name (handle -> name)
	contactRecords []ContactRecord   // per-contact records for name -> handle search
	contactsBuilt  bool
)

var nonDigit = regexp.MustCompile(`\D`)

// normalisePhone strips all non-digit characters and returns the last 10 digits,
// which is sufficient to match US numbers stored in various formats.
func normalisePhone(s string) string {
	digits := nonDigit.ReplaceAllString(s, "")
	if len(digits) > 10 {
		digits = digits[len(digits)-10:]
	}
	return digits
}

// sanitizeName scrubs a raw AddressBook name so the LLM (and iMessage
// display path) never receive garbage. Two failure modes are handled,
// with the raw bytes logged once so we can identify the source encoding:
//
//   - INVALID UTF-8: encoding/json would turn the bad bytes into U+FFFD
//     ("�"). Dropped via ToValidUTF8.
//   - VALID-UTF-8 CONTROL BYTES: this is the subtle one. NUL and other
//     control characters are *valid* UTF-8, so utf8.ValidString passes
//     them through — a UTF-16-laid-out name like "J\x00o\x00h\x00n" or a
//     typedstream-framed value reads as valid UTF-8 yet renders as
//     garbage. We strip control runes (keeping normal whitespace) so the
//     remaining text is clean; e.g. interspersed NULs collapse back to
//     "John".
//
// Returns the cleaned, trimmed name (possibly "" if nothing survives).
func sanitizeName(name string, pk int, first, last, nick, org string) string {
	bad := false
	if !utf8.ValidString(name) {
		bad = true
		name = strings.ToValidUTF8(name, "")
	}
	if hasControlRunes(name) {
		bad = true
		name = strings.Map(func(r rune) rune {
			// Keep printable runes and ordinary spacing; drop NUL and
			// other control characters that are valid UTF-8 but garbage.
			if r == '\t' || r == ' ' {
				return r
			}
			if unicode.IsControl(r) || r == 0xFFFD {
				return -1
			}
			return r
		}, name)
	}
	if bad {
		nfo.Log("contacts: pk=%d sanitized garbage name — raw bytes: %x | first=%q last=%q nick=%q org=%q -> %q",
			pk, []byte(strings.TrimSpace(first+" "+last+" "+nick+" "+org)), first, last, nick, org, name)
	}
	return strings.TrimSpace(name)
}

// hasControlRunes reports whether s contains any control character other
// than tab. NUL-interspersed (UTF-16-shaped) values trip this even when
// utf8.ValidString is happy.
func hasControlRunes(s string) bool {
	for _, r := range s {
		if r == '\t' {
			continue
		}
		if unicode.IsControl(r) || r == 0xFFFD {
			return true
		}
	}
	return false
}

// lookupContact returns the display name for a phone number or email address
// by querying the macOS AddressBook SQLite databases. Results are cached.
func lookupContact(handle string) string {
	if handle == "" {
		return ""
	}

	contactsMu.RLock()
	if contactsBuilt {
		name := contactsCache[strings.ToLower(handle)]
		contactsMu.RUnlock()
		return name
	}
	contactsMu.RUnlock()

	buildContactsCache()

	contactsMu.RLock()
	defer contactsMu.RUnlock()
	return contactsCache[strings.ToLower(handle)]
}

// buildContactsCache loads all contacts from all AddressBook sources into memory.
func buildContactsCache() {
	contactsMu.Lock()
	defer contactsMu.Unlock()
	if contactsBuilt {
		return
	}
	contactsCache = map[string]string{}
	contactRecords = nil
	contactsBuilt = true

	home, _ := os.UserHomeDir()
	// AddressBook databases live under ~/Library/Application Support/AddressBook/
	// either directly or under per-source subdirectories.
	pattern := filepath.Join(home, "Library", "Application Support", "AddressBook", "**", "AddressBook-v22.abcddb")
	matches, _ := filepath.Glob(filepath.Join(home, "Library", "Application Support", "AddressBook", "AddressBook-v22.abcddb"))
	// Also search one level of Sources subdirs.
	sub, _ := filepath.Glob(filepath.Join(home, "Library", "Application Support", "AddressBook", "Sources", "*", "AddressBook-v22.abcddb"))
	matches = append(matches, sub...)
	_ = pattern

	for _, dbPath := range matches {
		loadAddressBook(dbPath)
	}
	// Deterministic order so Search results (and any logging) are stable.
	sort.Slice(contactRecords, func(i, j int) bool { return contactRecords[i].Name < contactRecords[j].Name })
	nfo.Log("contacts: loaded %d entries, %d records", len(contactsCache), len(contactRecords))
}

// loadAddressBook reads one AddressBook database and merges contacts into the cache.
func loadAddressBook(dbPath string) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_busy_timeout=2000")
	if err != nil {
		return
	}
	defer db.Close()

	// Build name map: Z_PK → display name
	names := map[int]string{}
	rows, err := db.Query(`SELECT Z_PK, ZFIRSTNAME, ZLASTNAME, ZNICKNAME, ZORGANIZATION FROM ZABCDRECORD`)
	if err != nil {
		return
	}
	for rows.Next() {
		var pk int
		var first, last, nick, org sql.NullString
		if rows.Scan(&pk, &first, &last, &nick, &org) != nil {
			continue
		}
		var name string
		switch {
		case first.Valid && last.Valid:
			name = strings.TrimSpace(first.String + " " + last.String)
		case first.Valid:
			name = first.String
		case last.Valid:
			name = last.String
		case nick.Valid:
			name = nick.String
		case org.Valid:
			name = org.String
		}
		if name != "" {
			name = sanitizeName(name, pk, first.String, last.String, nick.String, org.String)
		}
		if name != "" {
			names[pk] = name
		}
	}
	rows.Close()

	// Per-pk records accumulate handles for the reverse (name -> handle)
	// search. Keyed by the same Z_PK as the name map so phone/email rows
	// attach to the right person.
	records := make(map[int]*ContactRecord, len(names))
	for pk, nm := range names {
		records[pk] = &ContactRecord{Name: nm}
	}

	// Map phone numbers → name. The number column(s) in ZABCDPHONENUMBER
	// vary by macOS version, so detect them rather than hardcoding a SELECT
	// that errors out wholesale when one is absent. That silent failure
	// (the old query swallowed its error via `if err == nil`) is exactly
	// the "emails resolve but phones don't" bug: a single missing column
	// made the whole phone pass disappear.
	if phoneCols := phoneValueColumns(tableColumns(db, "ZABCDPHONENUMBER")); len(phoneCols) > 0 {
		q := "SELECT ZOWNER, " + strings.Join(phoneCols, ", ") + " FROM ZABCDPHONENUMBER"
		rows, err = db.Query(q)
		if err != nil {
			nfo.Log("contacts: phone query %q failed: %v", q, err)
		} else {
			var owner int
			vals := make([]sql.NullString, len(phoneCols))
			dest := make([]any, 1+len(phoneCols))
			dest[0] = &owner
			for i := range vals {
				dest[i+1] = &vals[i]
			}
			for rows.Next() {
				if rows.Scan(dest...) != nil {
					continue
				}
				name := names[owner]
				if name == "" {
					continue
				}
				recorded := false
				for _, v := range vals {
					raw := strings.TrimSpace(v.String)
					if !v.Valid || raw == "" {
						continue
					}
					// Store both the raw form and the normalised 10-digit form
					// for handle -> name lookups.
					contactsCache[strings.ToLower(raw)] = name
					if norm := normalisePhone(raw); norm != "" {
						contactsCache[norm] = name
					}
					// Record one number per row (the row is one phone; the
					// extra column is just an alternate format of the same).
					if rec := records[owner]; rec != nil && !recorded {
						rec.Phones = addUnique(rec.Phones, raw)
						recorded = true
					}
				}
			}
			rows.Close()
		}
	} else {
		nfo.Log("contacts: ZABCDPHONENUMBER has no recognized number column; phones unavailable")
	}

	// Map email addresses → name
	rows, err = db.Query(`SELECT ZOWNER, ZADDRESS FROM ZABCDEMAILADDRESS`)
	if err == nil {
		for rows.Next() {
			var owner int
			var addr sql.NullString
			if rows.Scan(&owner, &addr) != nil {
				continue
			}
			if addr.Valid && addr.String != "" {
				name := names[owner]
				if name != "" {
					contactsCache[strings.ToLower(addr.String)] = name
				}
				if rec := records[owner]; rec != nil {
					rec.Emails = addUnique(rec.Emails, strings.TrimSpace(addr.String))
				}
			}
		}
		rows.Close()
	}

	// Merge this source's records into the package slice. Caller holds the
	// write lock (loadAddressBook only runs inside buildContactsCache).
	for _, rec := range records {
		if rec.Name == "" {
			continue
		}
		contactRecords = append(contactRecords, *rec)
	}
}

// tableColumns returns the set of column names (upper-cased) for a table
// via PRAGMA table_info. Empty on error or unknown table.
func tableColumns(db *sql.DB, table string) map[string]bool {
	cols := map[string]bool{}
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return cols
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk) != nil {
			continue
		}
		cols[strings.ToUpper(name)] = true
	}
	return cols
}

// phoneValueColumns picks the columns in ZABCDPHONENUMBER that hold a
// dialable number, from those that actually exist. Prefers the canonical
// names; falls back to any *NUMBER* column so a schema change still finds
// the value instead of silently dropping every phone.
func phoneValueColumns(cols map[string]bool) []string {
	var out []string
	for _, c := range []string{"ZFULLNUMBER", "ZVALUE"} {
		if cols[c] {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		for c := range cols {
			if strings.Contains(c, "NUMBER") {
				out = append(out, c)
			}
		}
		sort.Strings(out) // deterministic (map iteration is random)
	}
	return out
}

// addUnique appends v to list if not already present (case-sensitive).
func addUnique(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}
