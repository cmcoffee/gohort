package imagefetch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/bgrun"

	. "github.com/cmcoffee/gohort/core"
)

// Attempts at one picture: how many an edit has made from the same source
// pictures in one conversation, so the checking level's limit
// (ImageCheckAttempts) holds.
//
// The advice after a failed check said "try once more at most", and nothing
// counted. An agent unhappy with a blend re-rendered it as long as the
// per-turn runaway cap let it, each pass a full render and a full check.
//
// Counted by conversation, not by turn: a background render's result arrives
// in a wake turn, and a retry from there is the same attempt at the same
// picture. Counted by the pictures' content, not by how they were passed, so
// image#3 and the file it was saved as are one source. Kept in memory for a
// while after the last attempt: a restart, or coming back to the same
// sources much later, starts the count again, which is a fresh request.
const attemptsKeptFor = 30 * time.Minute

var attempts = struct {
	sync.Mutex
	made map[string]attemptCount
}{made: map[string]attemptCount{}}

type attemptCount struct {
	n    int
	last time.Time
}

// attemptKey is the conversation and the sources, by content; "" when there
// is no conversation to count in or no sources to count by (a picture made
// from a prompt alone is not an attempt at a picture: the runaway cap is its
// limit).
func attemptKey(sess *ToolSession, refs []string) string {
	if sess == nil || len(refs) == 0 {
		return ""
	}
	convo := firstNonBlank(sess.DeliverySessionID, sess.ChatSessionID)
	if convo == "" {
		return ""
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		if data, ok := ResolveRecentImage(sess, ref); ok && len(data) > 0 {
			sum := sha256.Sum256(data)
			ids = append(ids, hex.EncodeToString(sum[:8]))
			continue
		}
		ids = append(ids, "ref:"+strings.TrimSpace(ref))
	}
	sort.Strings(ids)
	return sess.Username + "\x1f" + convo + "\x1f" + strings.Join(ids, ",")
}

// attemptsMade is how many pictures have been made from key's sources.
func attemptsMade(key string, now time.Time) int {
	if key == "" {
		return 0
	}
	attempts.Lock()
	defer attempts.Unlock()
	c, ok := attempts.made[key]
	if !ok || now.Sub(c.last) > attemptsKeptFor {
		return 0
	}
	return c.n
}

// recordAttempt counts one picture made from key's sources and returns how
// many that makes. It sweeps the counts that have gone stale as it goes.
func recordAttempt(key string, now time.Time) int {
	if key == "" {
		return 1
	}
	attempts.Lock()
	defer attempts.Unlock()
	for k, c := range attempts.made {
		if now.Sub(c.last) > attemptsKeptFor {
			delete(attempts.made, k)
		}
	}
	c := attempts.made[key]
	c.n++
	c.last = now
	attempts.made[key] = c
	return c.n
}

// attemptsRefusal is what an edit says instead of rendering past the limit.
func attemptsRefusal(made, limit int, level string) error {
	have := "have"
	if made == 1 {
		have = "has"
	}
	return fmt.Errorf("no picture was made: %s %s already been made from these same source pictures, and checking is set to %s, which gives up after %d. "+
		"Do NOT render them again. Deliver the best result you have, and tell the person plainly what did not come through. "+
		"If they want another try, they can ask for it with more checking (checking: thorough), or with a larger, clearer picture of what was lost",
		pluralPictures(made), have, level, limit)
}

// lastAttemptNote is what a result says when it was the last picture its
// sources may give: the check's "try once more" no longer holds.
func lastAttemptNote(made, limit int) string {
	return fmt.Sprintf(" ATTEMPTS, which overrides any word above about trying again: this was picture %d of the %d allowed from these sources. Do not render them again: deliver the best of them, and say plainly what did not come through, if anything did not.", made, limit)
}

// editChecking is the checking level for one edit: the call's own, when it
// names one, else the deployment's.
func editChecking(args map[string]any) string {
	if l, ok := ImageCheckLevel(StringArg(args, "checking")); ok {
		return l
	}
	return ImageChecking()
}

// editProgress says where an edit has got, to a person watching a
// background render (the live pill, the Monitor's Trace). No-op inline.
func editProgress(sess *ToolSession, line string) {
	if sess != nil {
		bgrun.ReportTaskProgress(sess.Context(), line)
	}
}

// firstNonBlank is the first of vs that is not blank, trimmed.
func firstNonBlank(vs ...string) string {
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
