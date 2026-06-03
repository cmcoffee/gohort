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
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"github.com/cmcoffee/snugforge/nfo"
	_ "modernc.org/sqlite"
)

// init registers contacts.lookup so the daemon announces it over the
// WS tool bridge and the message service shares the same lookup impl.
func init() { core.RegisterTool(new(lookupTool)) }

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

var (
	contactsMu    sync.RWMutex
	contactsCache map[string]string // normalised phone/email → display name
	contactsBuilt bool
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
	nfo.Log("contacts: loaded %d entries", len(contactsCache))
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
			// macOS-version diagnostic: if a name field comes back as
			// non-UTF-8 bytes (the cause of the "�" U+FFFD the LLM sees —
			// encoding/json replaces invalid bytes with the replacement
			// char), log the raw hex so we can identify the encoding
			// (UTF-16 / typedstream / ciphertext / …) and sanitize so the
			// LLM gets clean text or nothing rather than garbage.
			if !utf8.ValidString(name) {
				nfo.Log("contacts: pk=%d name not valid UTF-8 — raw bytes: %x | first=%q last=%q nick=%q org=%q",
					pk, []byte(name), first.String, last.String, nick.String, org.String)
				name = strings.TrimSpace(strings.ToValidUTF8(name, ""))
			}
		}
		if name != "" {
			names[pk] = name
		}
	}
	rows.Close()

	// Map phone numbers → name
	rows, err = db.Query(`SELECT ZOWNER, ZFULLNUMBER, ZVALUE FROM ZABCDPHONENUMBER`)
	if err == nil {
		for rows.Next() {
			var owner int
			var full, val sql.NullString
			if rows.Scan(&owner, &full, &val) != nil {
				continue
			}
			name := names[owner]
			if name == "" {
				continue
			}
			for _, raw := range []string{full.String, val.String} {
				if raw == "" {
					continue
				}
				// Store both E.164 full form and normalised 10-digit form.
				contactsCache[strings.ToLower(raw)] = name
				norm := normalisePhone(raw)
				if norm != "" {
					contactsCache[norm] = name
				}
			}
		}
		rows.Close()
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
			}
		}
		rows.Close()
	}
}
