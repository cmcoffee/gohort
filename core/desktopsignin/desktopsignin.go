// Package desktopsignin hands a sign-in made in a person's own browser to the
// desktop app on their computer.
//
// The desktop app shows gohort through a proxy of its own, which keeps the
// sign-in cookie. A sign-in through an identity provider (Microsoft, any
// OIDC) leaves that proxy for the provider's site and comes back to the
// server's real address, so the cookie lands in the web view's storage and
// the app never sees it. So the app signs in the way command-line tools and
// editors do:
//
//  1. It starts a sign-in here with a challenge, the hash of a secret only
//     it holds, and gets a code and a short phrase to show.
//  2. It opens the person's browser on the server's confirm page for that
//     code, where they sign in however they normally do.
//  3. The page shows the phrase and asks them to confirm signing in the
//     app as themselves; a link someone else started cannot be confirmed
//     by accident, because the phrase is not the one their app shows.
//  4. The app, asking meanwhile, claims the sign-in with its secret, once,
//     and gets a session.
//
// In memory: a sign-in lives minutes, and a restart costs only a retry.
package desktopsignin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// TTL is how long a sign-in may wait to be confirmed and claimed.
const TTL = 10 * time.Minute

// maxPending bounds the sign-ins waiting at once: starting one needs no
// account, so the table must not grow without end.
const maxPending = 500

type entry struct {
	challenge string
	phrase    string
	user      string
	confirmed bool
	created   time.Time
}

var (
	mu      sync.Mutex
	pending = map[string]*entry{}
	now     = time.Now
)

// Challenge is what an app sends for its secret verifier: the hex SHA-256
// of it.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return hex.EncodeToString(sum[:])
}

// Start begins a sign-in for challenge, and returns its code (in the
// confirm page's address) and the phrase the app and the page both show.
func Start(challenge string) (code, phrase string, err error) {
	challenge = strings.TrimSpace(strings.ToLower(challenge))
	if len(challenge) != 64 {
		return "", "", errors.New("send the challenge: the hex SHA-256 of a secret the app keeps")
	}
	mu.Lock()
	defer mu.Unlock()
	sweep()
	if len(pending) >= maxPending {
		return "", "", errors.New("too many sign-ins are waiting: try again in a few minutes")
	}
	code, phrase = randomHex(16), randomPhrase()
	pending[code] = &entry{challenge: challenge, phrase: phrase, created: now()}
	return code, phrase, nil
}

// Phrase is the phrase of the sign-in waiting under code, ok false when
// there is none (never started, expired, or confirmed already).
func Phrase(code string) (string, bool) {
	mu.Lock()
	defer mu.Unlock()
	sweep()
	e, ok := pending[code]
	if !ok || e.confirmed {
		return "", false
	}
	return e.phrase, true
}

// Confirm signs the app waiting under code in as user. False when there is
// no such sign-in waiting.
func Confirm(code, user string) bool {
	mu.Lock()
	defer mu.Unlock()
	sweep()
	e, ok := pending[code]
	if !ok || e.confirmed || strings.TrimSpace(user) == "" {
		return false
	}
	e.user, e.confirmed = user, true
	return true
}

// Status is where a claim stands.
type Status int

const (
	Waiting Status = iota // not confirmed yet: ask again
	Ready                 // confirmed: the user is signed in, this once
	Gone                  // expired, claimed, or the wrong secret
)

// Claim is the app asking for its sign-in with its secret: Ready, with the
// person, once it is confirmed (and never again), Waiting until then, Gone
// otherwise. A wrong secret ends the sign-in, so a stolen code cannot be
// guessed at.
func Claim(code, verifier string) (string, Status) {
	mu.Lock()
	defer mu.Unlock()
	sweep()
	e, ok := pending[code]
	if !ok {
		return "", Gone
	}
	if subtle.ConstantTimeCompare([]byte(Challenge(verifier)), []byte(e.challenge)) != 1 {
		delete(pending, code)
		return "", Gone
	}
	if !e.confirmed {
		return "", Waiting
	}
	delete(pending, code)
	return e.user, Ready
}

// sweep drops sign-ins past TTL. Called with mu held.
func sweep() {
	cut := now().Add(-TTL)
	for k, e := range pending {
		if e.created.Before(cut) {
			delete(pending, k)
		}
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// phraseAlphabet leaves out what reads alike (0/O, 1/I/L).
const phraseAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// randomPhrase is eight characters as two groups, "K7QM-3XRD", for a person
// to compare between the app and the page.
func randomPhrase() string {
	b := make([]byte, 8)
	rand.Read(b)
	var s strings.Builder
	for i, c := range b {
		if i == 4 {
			s.WriteByte('-')
		}
		s.WriteByte(phraseAlphabet[int(c)%len(phraseAlphabet)])
	}
	return s.String()
}
