package core

import (
	"net/http/httptest"
	"testing"
)

// A peer on an older release, a desktop app built before the rename, or a
// client holding a deployment key sends the old header names; each is read.
func TestTheOldHeaderNamesAreStillRead(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(legacyPeerKeyHeader, "p")
	r.Header.Set(legacyDeploymentKeyHeader, "d")
	r.Header.Set("X-Gohort-Desktop-Client-Key", "k")
	if headerOr(r, peerKeyHeader, legacyPeerKeyHeader) != "p" || headerOr(r, deploymentKeyHeader, legacyDeploymentKeyHeader) != "d" ||
		headerOr(r, "X-Oddjob-Desktop-Client-Key", "X-Gohort-Desktop-Client-Key") != "k" {
		t.Error("an old header name was not read")
	}
	r.Header.Set(peerKeyHeader, "new")
	if headerOr(r, peerKeyHeader, legacyPeerKeyHeader) != "new" {
		t.Error("the current name should win when both are present")
	}
}
