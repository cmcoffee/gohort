package core

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// --- the exec audit trail ----------------------------------------------------

// TestTheExecCommandIsAudited — the widest grant in the system ran a shell on a
// named appliance and left no record at all. Minting a key logged, an
// investigate question logged, the command actually EXECUTED did not, so after
// an incident there was nothing to read.
func TestTheExecCommandIsAudited(t *testing.T) {
	src, err := os.ReadFile("peer_investigate.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func HandlePeerExec")
	if i < 0 {
		t.Fatal("HandlePeerExec has moved")
	}
	h := body[i:]
	if j := strings.Index(h, "\nfunc "); j > 0 {
		h = h[:j]
	}
	// The command, the key and the appliance — a line naming fewer than all
	// three cannot answer "who ran what, where".
	if !strings.Contains(h, `Log("[peer] %s EXEC on %s: %s"`) {
		t.Error("the executed command is not logged before it runs")
	}
	for _, want := range []string{"k.Label", "truncateForAudit(cmd)"} {
		if !strings.Contains(h, want) {
			t.Errorf("the audit line does not carry %s", want)
		}
	}
	// Logged BEFORE the run: a command that hangs or kills the process is
	// exactly the one worth a record, and an after-only log loses those.
	if strings.Index(h, "Log(\"[peer] %s EXEC on %s: %s\"") > strings.Index(h, "PeerExecFunc(ctx") {
		t.Error("the command is logged only after it runs — a hang or a crash leaves no trace")
	}
	if !strings.Contains(h, "EXEC on %s failed after") || !strings.Contains(h, "EXEC on %s ok in") {
		t.Error("the outcome is not logged, so the trail says what was asked and never what happened")
	}
}

// TestAuditLinesCannotBeForged — an audit trail an attacker can write entries
// into is worse than none. A multi-line command would otherwise split the
// record, and a crafted later line could impersonate a separate entry.
func TestAuditLinesCannotBeForged(t *testing.T) {
	got := truncateForAudit("echo one\nLog: [peer] trusted EXEC on box: rm -rf /\r\necho two")
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("a newline survived into the audit line: %q", got)
	}
	if !strings.Contains(got, `\n`) {
		t.Errorf("the newline was dropped rather than escaped, losing what was actually run: %q", got)
	}
	// Long commands are bounded, and say they were bounded.
	long := strings.Repeat("x", auditCommandMax*3)
	out := truncateForAudit(long)
	if len(out) > auditCommandMax+64 {
		t.Errorf("a long command was not bounded: %d chars", len(out))
	}
	if !strings.Contains(out, "bytes total") {
		t.Errorf("truncation is silent, so the log reads as the whole command: %q", out[len(out)-40:])
	}
	// A short command survives whole — the common case must stay readable.
	if truncateForAudit("systemctl status nginx") != "systemctl status nginx" {
		t.Error("an ordinary command was altered")
	}
}

// --- the pre-auth throttle ---------------------------------------------------

func throttleReq(addr, key string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
	r.RemoteAddr = addr
	if key != "" {
		r.Header.Set(peerKeyHeader, key)
	}
	return r
}

// TestFailedAuthIsThrottledPerSource — LookupPeerKey reads every issued key
// before it can decide, and the per-key rate limit only applies once a key is
// RECOGNIZED. So an unauthenticated caller paid nothing and cost a full table
// read, without limit.
func TestFailedAuthIsThrottledPerSource(t *testing.T) {
	peerAuthFailMu.Lock()
	peerAuthFails = map[string]*peerFailWindow{}
	peerAuthFailMu.Unlock()

	bad := throttleReq("198.51.100.7:5555", "not-a-real-key")
	if peerSourceThrottled(bad) {
		t.Fatal("a source is throttled before it has failed anything")
	}
	for i := 0; i < peerAuthFailMax; i++ {
		peerNoteAuthFailure(bad)
	}
	if !peerSourceThrottled(bad) {
		t.Error("a source that spent its whole failure allowance is still admitted to the lookup")
	}
	// Another address is unaffected — the limit is per source, not global, or
	// one bad actor would lock every peer out.
	if peerSourceThrottled(throttleReq("203.0.113.9:4444", "x")) {
		t.Error("throttling one source throttled a different one")
	}
}

// TestTheThrottleCountsFailuresNotRequests — a busy legitimate peer makes
// hundreds of calls a minute from one address. Counting requests would cut it
// off; counting failures is what lets the ceiling be low enough to matter.
func TestTheThrottleCountsFailuresNotRequests(t *testing.T) {
	peerAuthFailMu.Lock()
	peerAuthFails = map[string]*peerFailWindow{}
	peerAuthFailMu.Unlock()

	good := throttleReq("198.51.100.8:6666", "valid")
	for i := 0; i < peerAuthFailMax*5; i++ {
		if peerSourceThrottled(good) {
			t.Fatalf("a source that never failed was throttled after %d successful checks", i)
		}
	}
}

// TestTheSourceIsTheConnectionNotAHeader — X-Forwarded-For is caller-controlled,
// so honoring it would let one source present a fresh identity per request and
// walk straight around the limit.
func TestTheSourceIsTheConnectionNotAHeader(t *testing.T) {
	r := throttleReq("198.51.100.9:7777", "x")
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	if got := peerRequestSource(r); got != "198.51.100.9" {
		t.Errorf("source = %q, want the TCP address — a header would be spoofable", got)
	}
	// A bare address (no port) still yields something rather than nothing:
	// returning "" would silently disable the throttle.
	r.RemoteAddr = "198.51.100.10"
	if peerRequestSource(r) == "" {
		t.Error("an unparseable RemoteAddr disabled the throttle instead of degrading")
	}
}

// TestEveryAuthEntryPointIsThrottled — a source sweep, because the defect
// returns the moment a third entry point authenticates on its own.
//
// The manifest already does: it is not gated on one capability, so it calls
// peerFromRequest directly rather than going through peerAuthorize. An
// unthrottled endpoint would simply be the one an attacker used.
func TestEveryAuthEntryPointIsThrottled(t *testing.T) {
	files, _ := regexpGlob(t, `peer_.*\.go$`)
	call := regexp.MustCompile(`peerFromRequest\(r\)`)
	found := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if !call.MatchString(line) {
				continue
			}
			found++
			throttled := false
			for back := i; back >= 0 && back > i-12; back-- {
				if strings.Contains(lines[back], "peerSourceThrottled(r)") {
					throttled = true
					break
				}
			}
			if !throttled {
				t.Errorf("%s:%d authenticates a peer without checking peerSourceThrottled first — "+
					"an unauthenticated caller can spend a full key-table read here, unbounded", f, i+1)
			}
		}
	}
	if found == 0 {
		t.Fatal("found no peer authentication sites — the sweep is no longer looking where they live")
	}
}

func regexpGlob(t *testing.T, pattern string) ([]string, error) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		if re.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
