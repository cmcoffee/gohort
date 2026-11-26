package apppage

import "regexp"

// Every open of an app starts fresh, reloads included: its page's
// localStorage lasts one visit, and what it keeps between visits it keeps
// with app.state (apps/customapps/view_state.go). Pages written before that
// kept where the person was in localStorage and had it put back on every
// open; they now open fresh until they move it.

var (
	localStorageSetRE    = regexp.MustCompile(`\blocalStorage\s*\.\s*setItem\s*\(`)
	localStorageIndexRE  = regexp.MustCompile(`\blocalStorage\s*\[[^\]]*\]\s*=[^=]`)
	localStorageAssignRE = regexp.MustCompile(`\blocalStorage\s*\.\s*([A-Za-z_$][\w$]*)\s*=[^=]`)
	appStateRE           = regexp.MustCompile(`\bapp\.state\b`)
)

// localStorageMembers are the storage's own: assigning one is not keeping
// something in it.
var localStorageMembers = map[string]bool{"getItem": true, "setItem": true, "removeItem": true, "clear": true, "key": true, "length": true}

// KeepsInLocalStorage reports whether src (an app's page, or one of its
// script files, or all of them together) writes to localStorage and never
// uses app.state: what it keeps there for a next visit is gone with this
// one.
func KeepsInLocalStorage(src string) bool {
	if appStateRE.MatchString(src) {
		return false
	}
	if localStorageSetRE.MatchString(src) || localStorageIndexRE.MatchString(src) {
		return true
	}
	for _, m := range localStorageAssignRE.FindAllStringSubmatch(src, -1) {
		if !localStorageMembers[m[1]] {
			return true
		}
	}
	return false
}
