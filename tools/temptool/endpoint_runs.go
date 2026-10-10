package temptool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/buildledger"
)

// What a real call of an endpoint answered, kept so a write that worked can
// count as fired.
//
// test never fires a write. It tells the author to make one direct call and
// confirm a 2xx, and then nothing listened for that call: a toolbox action's
// handler never reported its run at all, an api tool with a response_pipe had
// its status line replaced by the pipe's output before anything read it, and
// the next test set the write back to "never fired". A translate-and-post
// tool failed verification on every build of two different models that way,
// each of which had made the call and seen it succeed.
//
// So the status is read here, in dispatch, from the raw response and before
// any pipe runs. Keyed by owner and the dispatch name ("toolbox.action" for a
// toolbox action, the tool's own name for an api tool), so one action firing
// says nothing about its siblings. Each entry carries a fingerprint of the
// request the endpoint makes, so an edit to the url, method, body, headers or
// credential is a different endpoint and has to fire again.
//
// In memory only, by decision. A restart forgets every fired write, which
// costs one more direct call or test, and only when the restart lands inside
// one build's verify step: a tool already verified keeps that (it is stored
// per session), and a call made after the restart is recorded fresh.
// Persisting it would mean a stored "this worked" outliving the thing it was
// about, for one call saved in a rare case.
//
// test's own live probe of a read is recorded here too (probe set), so a read
// that answered 2xx stays proven on a later test whose cases leave it out.
// Without it, a read that passed became UNPROVEN as soon as the author tested
// a different action, and the tool could not reach verified by testing one
// thing at a time.
type endpointRun struct {
	fingerprint string
	ok          bool
	probe       bool
	at          time.Time
}

var (
	endpointRunsMu sync.Mutex
	endpointRuns   = map[string]endpointRun{}
	// unfiredWrites holds, per owner and tool, the write endpoints the last
	// test left as the only thing standing between the tool and verified.
	// When a direct call fires the last of them, the tool is verified then,
	// without another test round.
	unfiredWrites = map[string]unfiredSet{}
)

// unfiredSet is one tool's waiting writes and when the test that left them
// ran, so a set nobody came back to is dropped with the runs it waits on.
type unfiredSet struct {
	names map[string]bool
	at    time.Time
}

// pruneEndpointRuns drops runs and waiting sets older than endpointRunTTL.
// Called with endpointRunsMu held.
func pruneEndpointRuns(now time.Time) {
	for k, r := range endpointRuns {
		if now.Sub(r.at) > endpointRunTTL {
			delete(endpointRuns, k)
		}
	}
	for k, u := range unfiredWrites {
		if now.Sub(u.at) > endpointRunTTL {
			delete(unfiredWrites, k)
		}
	}
}

// endpointRunTTL bounds how long a recorded run is believed. A day covers a
// build session; past that, the service may have changed under the tool.
const endpointRunTTL = 24 * time.Hour

func endpointRunKey(sess *ToolSession, name string) string {
	return sessUser(sess) + "\x00" + name
}

// endpointFingerprint identifies the request an api-mode tool makes: the
// fields that decide what is sent and where, nothing descriptive.
func endpointFingerprint(tt *TempTool) string {
	method := strings.ToUpper(strings.TrimSpace(tt.Method))
	if method == "" {
		method = "GET"
	}
	b, _ := json.Marshal(struct {
		URL, Method, Body, ContentType, Credential string
		Headers                                    map[string]string
	}{tt.CommandTemplate, method, tt.BodyTemplate, tt.ContentType, tt.Credential, tt.Headers})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// endpointTool is the api-mode tool dispatch builds for an endpoint, reduced
// to what endpointFingerprint reads, under the name dispatch records it by.
// test uses it to find what a direct call recorded.
func endpointTool(tt TempTool, ep TempToolAction) TempTool {
	name := tt.Name
	if effectiveTempToolMode(tt) == TempToolModeToolbox {
		name = tt.Name + "." + ep.Name
	}
	return TempTool{
		Name: name, CommandTemplate: ep.URLTemplate, Method: ep.Method,
		BodyTemplate: ep.BodyTemplate, ContentType: ep.ContentType,
		Headers: ep.Headers, Credential: tt.Credential,
	}
}

// noteEndpointStatus records what a real call of tt answered. Called from
// dispatch with the raw status line, before a pipe or an extract can hide it.
// A 2xx from a write also settles it in unfiredWrites, and verifies the tool
// when it was the last write the tool was waiting on.
func noteEndpointStatus(sess *ToolSession, tt *TempTool, statusLine string) {
	if tt == nil || strings.HasPrefix(tt.Name, "test.") {
		return // test's own read probe: test reads its result directly
	}
	ok := isStatus2xx(statusLine)
	endpointRunsMu.Lock()
	now := time.Now()
	pruneEndpointRuns(now)
	endpointRuns[endpointRunKey(sess, tt.Name)] = endpointRun{fingerprint: endpointFingerprint(tt), ok: ok, at: now}
	parent := tt.Name
	if i := strings.IndexByte(parent, '.'); i > 0 {
		parent = parent[:i]
	}
	verified := false
	if ok && isMutatingMethod(tt.Method) {
		pk := endpointRunKey(sess, parent)
		if waiting := unfiredWrites[pk]; waiting.names[tt.Name] {
			delete(waiting.names, tt.Name)
			if len(waiting.names) == 0 {
				delete(unfiredWrites, pk)
				verified = true
			}
		}
	}
	endpointRunsMu.Unlock()
	if verified {
		Log("[temptool] %q: last unfired write %q answered a direct call with %s; tool verified", parent, tt.Name, statusLine)
		RecordToolVerification(sess, parent, true, "")
		// The build ledger is what the graders and the verify status read,
		// and the test that left this write waiting recorded it unproven. Without
		// a pass here the log said verified while the ledger stayed red.
		recordToolOutcome(sess, parent, "direct call", buildledger.Pass, nil,
			fmt.Sprintf("direct call of %s answered 2xx (%s)", tt.Name, statusLine))
	}
}

// endpointFired reports when the endpoint last answered a direct call with a
// 2xx, if it did and has not been edited since.
func endpointFired(sess *ToolSession, ep TempTool) (time.Time, bool) {
	at, ok, _ := endpointAnswered(sess, ep)
	return at, ok
}

// endpointAnswered is endpointFired that also says whether the 2xx came from
// test's own probe rather than a direct call.
func endpointAnswered(sess *ToolSession, ep TempTool) (at time.Time, ok, probe bool) {
	endpointRunsMu.Lock()
	defer endpointRunsMu.Unlock()
	r, found := endpointRuns[endpointRunKey(sess, ep.Name)]
	if !found || !r.ok || r.fingerprint != endpointFingerprint(&ep) || time.Since(r.at) > endpointRunTTL {
		return time.Time{}, false, false
	}
	return r.at, true, r.probe
}

// noteProbeStatus records what test's live probe of a read answered, under
// the name a direct call of the same endpoint records by. A probe that failed
// replaces an earlier pass: the endpoint is not proven any more.
func noteProbeStatus(sess *ToolSession, ep TempTool, ok bool) {
	endpointRunsMu.Lock()
	defer endpointRunsMu.Unlock()
	now := time.Now()
	pruneEndpointRuns(now)
	endpointRuns[endpointRunKey(sess, ep.Name)] = endpointRun{fingerprint: endpointFingerprint(&ep), ok: ok, probe: true, at: now}
}

// lastCallSucceeded reports whether the latest recorded call of tt answered
// 2xx, and whether there is one to go by. For a run whose output cannot say:
// an api tool whose response_pipe replaced the status line.
func lastCallSucceeded(sess *ToolSession, tt *TempTool) (ok, known bool) {
	endpointRunsMu.Lock()
	defer endpointRunsMu.Unlock()
	r, found := endpointRuns[endpointRunKey(sess, tt.Name)]
	if !found || r.fingerprint != endpointFingerprint(tt) {
		return false, false
	}
	return r.ok, true
}

// setUnfiredWrites records which write endpoints of a tool the last test left
// waiting on a direct call. Empty clears it: a test that found anything else
// unproven, or nothing at all, leaves no write to settle.
func setUnfiredWrites(sess *ToolSession, tool string, names []string) {
	endpointRunsMu.Lock()
	defer endpointRunsMu.Unlock()
	now := time.Now()
	pruneEndpointRuns(now)
	key := endpointRunKey(sess, tool)
	if len(names) == 0 {
		delete(unfiredWrites, key)
		return
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	unfiredWrites[key] = unfiredSet{names: set, at: now}
}
