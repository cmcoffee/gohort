// Package toolcat holds the categories gohort files tools under on its own.
//
// Every tool has a category. The owner can set one by hand (TempTool.Category),
// and theirs always wins; a tool with none is filed automatically by a worker
// model, and that answer is kept here. Apart from the tool record on purpose:
// writing the record re-versions a tool, and a published one would unpublish.
//
// An answer is keyed by the tool's name and a hash of what it does, so a tool
// whose description changes is filed again. The filing itself (the model call)
// belongs to the app that has a worker; it registers Filer, and anything that
// shows tools calls Kick to have the unfiled ones filed in the background.
package toolcat

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core"
)

const table = "tool_auto_category"

func key(user string, t core.TempTool) string {
	sum := sha256.Sum256([]byte(t.Mode + "\x00" + t.Description))
	return user + "\x00" + t.Name + "\x00" + hex.EncodeToString(sum[:8])
}

// Auto is the category t was filed under for user, if it has been.
func Auto(user string, t core.TempTool) (string, bool) {
	db := core.RootDB
	if db == nil || user == "" || t.Name == "" {
		return "", false
	}
	var c string
	if db.Get(table, key(user, t), &c) && strings.TrimSpace(c) != "" {
		return c, true
	}
	return "", false
}

// SetAuto records the category t was filed under.
func SetAuto(user string, t core.TempTool, category string) {
	if db := core.RootDB; db != nil && user != "" && t.Name != "" && strings.TrimSpace(category) != "" {
		db.Set(table, key(user, t), strings.TrimSpace(category))
	}
}

// rejectTable holds, per tool, the categories the owner took it out of. A
// filed tool unticked from a category in the picker must not be filed straight
// back into it.
const rejectTable = "tool_category_rejected"

func rejectKey(user, name string) string { return user + "\x00" + name }

// Reject records that user took t out of category: the filing is cleared if
// it was that one, and the filer will not choose it for t again.
func Reject(user string, t core.TempTool, category string) {
	db := core.RootDB
	category = strings.TrimSpace(category)
	if db == nil || user == "" || t.Name == "" || category == "" {
		return
	}
	if c, ok := Auto(user, t); ok && strings.EqualFold(c, category) {
		db.Unset(table, key(user, t))
	}
	have := Rejected(user, t)
	for _, r := range have {
		if strings.EqualFold(r, category) {
			return
		}
	}
	db.Set(rejectTable, rejectKey(user, t.Name), append(have, category))
}

// Rejected is the categories user took t out of.
func Rejected(user string, t core.TempTool) []string {
	var out []string
	if db := core.RootDB; db != nil && user != "" && t.Name != "" {
		db.Get(rejectTable, rejectKey(user, t.Name), &out)
	}
	return out
}

// Of is t's category: the owner's own when set, else the one it was filed
// under. auto says which; "" when it has neither yet.
func Of(user string, t core.TempTool) (category string, auto bool) {
	if c := strings.TrimSpace(t.Category); c != "" {
		return c, false
	}
	if c, ok := Auto(user, t); ok {
		return c, true
	}
	return "", false
}

// InUse is every category user's tools are in, by hand or filed, sorted, one
// spelling each: what a new tool is filed among first.
func InUse(user string, tools []core.TempTool) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tools {
		if c, _ := Of(user, t); c != "" && !seen[strings.ToLower(c)] {
			seen[strings.ToLower(c)] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// Filer files user's tools that have no category yet. Registered by the app
// that owns the worker model; nil means nothing files.
var Filer func(user string)

// Kick asks for user's unfiled tools to be filed, in the background.
func Kick(user string) {
	if f := Filer; f != nil && user != "" {
		go f(user)
	}
}
