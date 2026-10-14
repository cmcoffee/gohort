package pathhold

import "testing"

// A held folder is in use, and so is anything inside it and any folder it is
// inside; a sibling is not, and a release ends the hold once every holder has
// let go.
func TestHolds(t *testing.T) {
	r1 := Hold("/stores/intake/CASE-1/prepared")
	if !InUse("/stores/intake/CASE-1") || !InUse("/stores/intake/CASE-1/prepared") || !InUse("/stores/intake/CASE-1/prepared/x.log") {
		t.Error("a held folder, its parent or its contents read as free")
	}
	if InUse("/stores/intake/CASE-10") || InUse("/stores/intake/CASE-2") {
		t.Error("a sibling (or a name that only shares a prefix) read as in use")
	}
	r2 := Hold("/stores/intake/CASE-1/prepared/")
	r1()
	r1()
	if !InUse("/stores/intake/CASE-1") {
		t.Error("released while a second holder still had it")
	}
	r2()
	if InUse("/stores/intake/CASE-1") {
		t.Error("still in use after every holder let go")
	}
	all := HoldAll("/a/one", "/b/two")
	if !InUse("/a") || !InUse("/b/two") {
		t.Error("HoldAll did not hold each path")
	}
	all()
	if InUse("/a") || InUse("/b") {
		t.Error("HoldAll's release left a hold")
	}
}
