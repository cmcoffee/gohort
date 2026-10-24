package imsg

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func resetRelayLog() {
	relayLog.Lock()
	relayLog.pending, relayLog.next = nil, 0
	relayLog.paused = relayLog.paused.AddDate(-1, 0, 0)
	relayLog.Unlock()
}

// What the relay logs reaches the server on the next poll, once, with the
// bridge key; a server from before the endpoint is not asked again each poll.
func TestTheRelaysLogIsSentOnce(t *testing.T) {
	resetRelayLog()
	var got [][]relayLogLine
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bridges/api/relay-log" || r.Header.Get("X-API-Key") != "k1" {
			t.Errorf("sent to %s with key %q", r.URL.Path, r.Header.Get("X-API-Key"))
		}
		var body struct {
			Lines []relayLogLine `json:"lines"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body.Lines)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	key := func() string { return "k1" }

	logf("attachment candidate: state=%d", 5)
	logf("attachment not found: %s", "IMG_2.heic")
	shipRelayLog(srv.URL, key)
	if len(got) != 1 || len(got[0]) != 2 || got[0][1].Text != "attachment not found: IMG_2.heic" || got[0][0].At == "" {
		t.Fatalf("sent %v", got)
	}
	shipRelayLog(srv.URL, key)
	if len(got) != 1 {
		t.Errorf("nothing new, nothing sent: %d send(s)", len(got))
	}

	// An older server: dropped, and left alone rather than asked every poll.
	status = http.StatusNotFound
	logf("one more")
	shipRelayLog(srv.URL, key)
	logf("and another")
	shipRelayLog(srv.URL, key)
	if len(got) != 2 {
		t.Errorf("an older server was asked %d time(s), want once", len(got)-1)
	}

	// Bounded while the server cannot be reached.
	resetRelayLog()
	for i := 0; i < relayLogCap+50; i++ {
		logf("line %d", i)
	}
	relayLog.Lock()
	n, first := len(relayLog.pending), relayLog.pending[0].Text
	relayLog.Unlock()
	if n != relayLogCap || first != "line 50" {
		t.Errorf("kept %d, oldest %q: want the newest %d", n, first, relayLogCap)
	}
}
