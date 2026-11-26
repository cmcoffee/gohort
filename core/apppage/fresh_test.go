package apppage

import "testing"

// A page that writes localStorage, in any of the ways a page does, keeps
// something for a next visit; reading it, or using app.state, does not.
func TestAPageThatKeepsSomethingInLocalStorage(t *testing.T) {
	for src, want := range map[string]bool{
		`localStorage.setItem("tab", t)`:                      true,
		`localStorage[ "tab" ] = t`:                           true,
		`window.localStorage.lastCase = id`:                   true,
		`localStorage[\"tab\"] = t`:                           true, // quoted as in a page's JSON
		`var t = localStorage.getItem("tab")`:                 false,
		`if (localStorage["tab"] == "x") go()`:                false,
		`localStorage.clear = null`:                           false,
		`localStorage.setItem("a", 1); app.state.set("a", 1)`: false,
		`app.records.list()`:                                  false,
	} {
		if got := KeepsInLocalStorage(src); got != want {
			t.Errorf("%s: %v, want %v", src, got, want)
		}
	}
}
