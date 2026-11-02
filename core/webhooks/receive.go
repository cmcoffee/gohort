package webhooks

// Receiving: /hook/<name>. A POST is checked, read and fed on; every answer
// is kept on the webhook's row. What it costs a caller to be refused is
// bounded per address, what it costs to be accepted per webhook, and what a
// wake costs per watch (the watch's own budget).

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// maxBody caps what is read of a post. Provider events are a few
	// kilobytes; a GitHub push with many commits is the large end.
	maxBody = 1 << 20
	// maxText caps what reaches the agent, as a watch's own door does.
	maxText = 64 << 10
	// perMinute bounds the posts one webhook answers, whatever they are.
	perMinute = 120
	// failMax bounds refused posts per address.
	failMax = 20
	// keptIDs is how many event ids a webhook remembers, to drop a retry.
	keptIDs = 200
)

var (
	posts    = NewRateLimiter(perMinute, time.Minute)
	failures = NewRateLimiter(failMax, time.Minute)
)

func init() {
	RegisterPublicPath(Prefix)
	RegisterRelayablePath(Prefix)
	RegisterRootRoute(Prefix, Handle)
}

// Handle answers a post to a webhook.
func Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "post to a webhook", http.StatusMethodNotAllowed)
		return
	}
	source := RequestSource(r)
	if failures.Spent(source) {
		TooManyRequests(w, time.Minute, "too many refused posts from this address")
		return
	}
	refuse := func(status int, why string) {
		if !failures.Allow(source) {
			TooManyRequests(w, time.Minute, "too many refused posts from this address")
			return
		}
		http.Error(w, why, status)
	}
	name := strings.TrimPrefix(r.URL.Path, Prefix)
	h, ok := Get(name)
	if !ok {
		refuse(http.StatusNotFound, "no such webhook")
		return
	}
	if !posts.Allow(h.Name) {
		note(h.Name, Call{At: time.Now(), Outcome: "refused: posted to too often"})
		TooManyRequests(w, time.Minute, "this webhook is posted to too often: slow down")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		note(h.Name, Call{At: time.Now(), Outcome: "refused: the post was larger than a webhook takes"})
		refuse(http.StatusRequestEntityTooLarge, "too large")
		return
	}
	if err := h.verify(r.Header, body); err != nil {
		note(h.Name, Call{At: time.Now(), Outcome: "refused: " + err.Error()})
		refuse(http.StatusUnauthorized, "not verified")
		return
	}
	status, call := h.deliver(r.Header, body)
	call.At = time.Now()
	note(h.Name, call)
	w.WriteHeader(status)
}

// verify checks the caller of a post is the service h is for.
func (h Webhook) verify(header http.Header, body []byte) error {
	switch h.Verify {
	case VerifyNone:
		return nil
	case VerifyToken:
		want := secret(h.Name)
		got := strings.TrimSpace(header.Get(h.Header))
		if strings.EqualFold(h.Header, "Authorization") {
			if t, ok := cutFold(got, "Bearer "); ok {
				got = strings.TrimSpace(t)
			}
		}
		if want == "" || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return errors.New("its token does not match")
		}
		return nil
	case VerifyHMAC:
		key := secret(h.Name)
		got := strings.TrimSpace(header.Get(h.Header))
		if got == "" {
			return errors.New("it came with no signature")
		}
		if p := strings.TrimSpace(h.Prefix); p != "" {
			var ok bool
			if got, ok = cutFold(got, p); !ok {
				return errors.New("its signature does not start " + p)
			}
		}
		if key == "" || got == "" {
			return errors.New("it came with no signature")
		}
		newHash := sha256.New
		if h.Algorithm == "sha1" {
			newHash = func() hash.Hash { return sha1.New() }
		}
		mac := hmac.New(newHash, []byte(key))
		mac.Write(body)
		sum := mac.Sum(nil)
		// Providers send it as hex (GitHub), in either case, or base64
		// (Shopify).
		if subtle.ConstantTimeCompare([]byte(strings.ToLower(got)), []byte(hex.EncodeToString(sum))) == 1 ||
			subtle.ConstantTimeCompare([]byte(got), []byte(base64.StdEncoding.EncodeToString(sum))) == 1 {
			return nil
		}
		return errors.New("its signature does not match")
	}
	return errors.New("it checks nothing gohort knows")
}

// cutFold is s without prefix, matched regardless of case.
func cutFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// event is a post as its webhook reads it.
type event struct {
	text, kind, id, from string
}

// read is what h reads out of a post.
func (h Webhook) read(header http.Header, body []byte) event {
	at := func(path string) string {
		path = strings.TrimSpace(path)
		if path == "" {
			return ""
		}
		if name, ok := strings.CutPrefix(path, "header:"); ok {
			return strings.TrimSpace(header.Get(strings.TrimSpace(name)))
		}
		v, err := ExtractJSONPath(body, path)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(v)
	}
	e := event{text: at(h.Map.Text), kind: at(h.Map.Kind), id: at(h.Map.ID), from: at(h.Map.From)}
	if strings.TrimSpace(h.Map.Text) == "" {
		e.text = strings.TrimSpace(string(body))
	}
	return e
}

// deliver reads a verified post and feeds it to h's watch: the status to
// answer with and the call to keep.
func (h Webhook) deliver(header http.Header, body []byte) (int, Call) {
	e := h.read(header, body)
	call := Call{Kind: e.kind}
	if len(h.Kinds) > 0 && !contains(h.Kinds, e.kind) {
		call.Outcome, call.OK = "passed over: a kind it does not pass on", true
		return http.StatusAccepted, call
	}
	m, ok := GetEventMonitor(RootDB, h.Owner, h.Watch())
	if !ok || m.Kind != EventKindWebhook {
		call.Outcome = "not passed on: its watch " + h.Watch() + " is gone"
		return http.StatusAccepted, call
	}
	if m.Paused {
		call.Outcome, call.OK = "not passed on: its watch is paused", true
		return http.StatusAccepted, call
	}
	// A provider retries what it thinks was lost: the same id once is enough.
	if !claim(h.Name, e.id) {
		call.Outcome, call.OK = "passed over: an event it already passed on", true
		return http.StatusOK, call
	}
	if !EventMonitorFires.Allow(m.Owner + ":" + m.Name) {
		release(h.Name, e.id)
		call.Outcome = "not passed on: its watch is woken too often"
		return http.StatusTooManyRequests, call
	}
	go FireEventMonitor(context.Background(), RootDB, m, e.summary(h.Label))
	call.Outcome, call.OK = "woke "+m.Name, true
	return http.StatusNoContent, call
}

// summary is e as the woken agent reads it.
func (e event) summary(label string) string {
	head := label
	if e.kind != "" {
		head += ": " + e.kind
	}
	if e.from != "" {
		head += ", from " + e.from
	}
	text := e.text
	if text == "" {
		text = "(the event had no text)"
	}
	if len(text) > maxText {
		text = text[:maxText] + "\n(cut: the event was longer)"
	}
	return fmt.Sprintf("%s\n\n%s", head, text)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}

// recent are the event ids each webhook passed on lately, newest last.
var recent = struct {
	sync.Mutex
	ids map[string][]string
}{ids: map[string][]string{}}

// claim records event id as passed on by webhook name, and reports
// whether it was not already: one step, so two retries arriving together
// are not both passed on. An event with no id is always new.
func claim(name, id string) bool {
	if id == "" {
		return true
	}
	recent.Lock()
	defer recent.Unlock()
	if contains(recent.ids[name], id) {
		return false
	}
	ids := append(recent.ids[name], id)
	if len(ids) > keptIDs {
		ids = ids[len(ids)-keptIDs:]
	}
	recent.ids[name] = ids
	return true
}

// release forgets a claimed id that was not passed on after all, so the
// provider's retry is.
func release(name, id string) {
	recent.Lock()
	defer recent.Unlock()
	ids := recent.ids[name]
	for i := len(ids) - 1; i >= 0; i-- {
		if ids[i] == id {
			recent.ids[name] = append(ids[:i:i], ids[i+1:]...)
			return
		}
	}
}
