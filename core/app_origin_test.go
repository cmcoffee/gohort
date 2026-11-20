package core

import "testing"

// An app's copy id is the same for as long as it is the same app, and
// different for every copy of it: another owner, or the same slug made again.
func TestAnAppsCopyIDNamesThatCopy(t *testing.T) {
	a := AppSpec{Owner: "u", Slug: "desk", Created: "2026-11-19T10:00:00Z", Name: "Desk"}
	same := a
	same.Name, same.Updated = "Front desk", "2026-11-20T10:00:00Z"
	if appCopyID(a) == "" || appCopyID(a) != appCopyID(same) {
		t.Errorf("a change to the app changed its copy id: %q %q", appCopyID(a), appCopyID(same))
	}
	other := a
	other.Owner = "v"
	again := a
	again.Created = "2026-11-21T10:00:00Z"
	if appCopyID(other) == appCopyID(a) || appCopyID(again) == appCopyID(a) {
		t.Error("another copy of the app has its copy id")
	}
}
