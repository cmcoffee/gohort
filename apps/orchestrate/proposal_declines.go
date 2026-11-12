package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// A "no" to an agent's proposal that holds.
//
// An agent proposes things to its owner (a rule today, see propose_rule.go;
// sub-agents and edits to itself later, docs/design/agent-self-splitting.md).
// A decline used to be an activity line and a note to the model not to ask
// again in that conversation, so the next conversation asked again, and a
// reworded proposal or a sub-agent's never matched at all. A decline is now a
// record, and a new proposal is checked against the records before the owner
// sees it:
//
//   - The same change (by fingerprint) or the same change reworded (by
//     embedding, declineSameSim) is dropped, and the agent is told when its
//     owner said no.
//   - One that is only similar (declineSimilarSim) reaches the owner marked as
//     looking like the one declined, and the owner decides.
//
// A decline binds the agent's whole family (its root agent and everything
// owned under it), so a sub-agent cannot raise what its parent was told no.
// It is kept in the owner's store, never deleted: a reversed one is marked.

// proposalDeclinesTable holds the declines, in the owner's store, by id.
const proposalDeclinesTable = "agent_proposal_declines"

// The kinds of "no" a proposal can be given.
const (
	declineNotNow = "not_now" // holds for declineNotNowFor
	declineNo     = "no"      // holds until reversed
)

// declineNotNowFor is how long a Not now holds.
const declineNotNowFor = 30 * 24 * time.Hour

// declineSameSim is how alike (cosine) a proposal must be to a declined one
// to be that one reworded, and declineSimilarSim to be shown as like it. The
// first is fact dedup's threshold for the same note (factstore.go).
const (
	declineSameSim    = 0.90
	declineSimilarSim = 0.75
)

// declineEmbedTimeout bounds one embed of a proposal: the check must not hold
// a turn behind a slow embedder, and without a vector it still has the
// fingerprint.
const declineEmbedTimeout = 5 * time.Second

// proposalKindRule is a rule proposed with propose_rule.
const proposalKindRule = "rule"

// proposalDecline is one "no" to a proposal.
type proposalDecline struct {
	ID          string    `json:"id"`
	Family      string    `json:"family"` // the root agent: the decline binds every agent under it
	Agent       string    `json:"agent"`  // the agent that proposed it
	Kind        string    `json:"kind"`
	Change      string    `json:"change"` // what was proposed, as worded
	Why         string    `json:"why,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	Vector      []float32 `json:"vector,omitempty"`
	EmbedSpace  string    `json:"embed_space,omitempty"` // EmbedVersion the vector was made in
	Mode        string    `json:"mode"`
	Until       time.Time `json:"until,omitempty"` // when a Not now stops holding
	DeclinedAt  time.Time `json:"declined_at"`
	ReversedBy  string    `json:"reversed_by,omitempty"`
	ReversedAt  time.Time `json:"reversed_at,omitempty"`
}

// standing reports whether the decline still holds at now.
func (d proposalDecline) standing(now time.Time) bool {
	if !d.ReversedAt.IsZero() {
		return false
	}
	return d.Mode != declineNotNow || d.Until.IsZero() || now.Before(d.Until)
}

// toAgent is what the agent is told when its proposal matched this decline.
func (d proposalDecline) toAgent() string {
	said := "declined"
	if d.Mode == declineNotNow {
		said = "said not now to"
	}
	out := fmt.Sprintf("Not proposed: your owner %s this on %s (%q).", said, d.DeclinedAt.Local().Format("Jan 2"), d.Change)
	if d.Mode == declineNotNow && !d.Until.IsZero() {
		out += fmt.Sprintf(" It can be proposed again after %s.", d.Until.Local().Format("Jan 2"))
	} else {
		out += " Do not propose it again, reworded or not."
	}
	return out + " Carry on as you are."
}

// proposalFamily is the root of agentID's family: the agent at the top of
// its OwnedBy chain, or agentID itself when it is owned by none.
func proposalFamily(db Database, agentID string) string {
	id := strings.TrimSpace(agentID)
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		rec, ok := loadAgent(db, id)
		if !ok || strings.TrimSpace(rec.OwnedBy) == "" {
			return id
		}
		id = strings.TrimSpace(rec.OwnedBy)
	}
	return id
}

// proposalFingerprint names a change however it is spaced, cased or ended.
func proposalFingerprint(kind, change string) string {
	norm := strings.ToLower(strings.Join(strings.Fields(change), " "))
	norm = strings.TrimRight(norm, ".;:!,")
	sum := sha256.Sum256([]byte(kind + "\x00" + norm))
	return hex.EncodeToString(sum[:16])
}

// embedProposal is text's vector and the space it is in, or nil when there is
// no embedder or it did not answer in time.
func embedProposal(text string) ([]float32, string) {
	if !GetEmbeddingConfig().Enabled || strings.TrimSpace(text) == "" {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), declineEmbedTimeout)
	defer cancel()
	v, err := Embed(ctx, text)
	if err != nil || len(v) == 0 {
		return nil, ""
	}
	return v, EmbedVersion()
}

// recordProposalDecline keeps the owner's "no" (mode) to agentID's proposal
// of change, and returns it.
func recordProposalDecline(db Database, agentID, kind, change, why, mode string) proposalDecline {
	if mode != declineNotNow {
		mode = declineNo
	}
	now := time.Now()
	d := proposalDecline{
		ID: UUIDv4(), Family: proposalFamily(db, agentID), Agent: agentID, Kind: kind,
		Change: strings.TrimSpace(change), Why: strings.TrimSpace(why),
		Fingerprint: proposalFingerprint(kind, change), Mode: mode, DeclinedAt: now,
	}
	if mode == declineNotNow {
		d.Until = now.Add(declineNotNowFor)
	}
	d.Vector, d.EmbedSpace = embedProposal(d.Change)
	db.Set(proposalDeclinesTable, d.ID, d)
	Log("[orchestrate.proposals] %s: owner said %s to a %s it proposed (family %s)", agentID, mode, kind, d.Family)
	return d
}

// declineMatch is what the check found: a decline that is this proposal
// (Same), or one it only looks like (Similar).
type declineMatch struct {
	Same, Similar *proposalDecline
}

// checkProposalDeclines compares agentID's proposal of change with the
// standing declines of its family. A decline whose vector is missing or was
// made in another embedding space is embedded again here and kept, so an
// embedder that was down at the decline, or has been changed since, still
// matches rewordings.
func checkProposalDeclines(db Database, agentID, kind, change string) declineMatch {
	var m declineMatch
	family := proposalFamily(db, agentID)
	fp := proposalFingerprint(kind, change)
	now := time.Now()
	var standing []proposalDecline
	for _, k := range db.Keys(proposalDeclinesTable) {
		var d proposalDecline
		if !db.Get(proposalDeclinesTable, k, &d) || d.Family != family || d.Kind != kind || !d.standing(now) {
			continue
		}
		if d.Fingerprint == fp {
			m.Same = &d
			return m
		}
		standing = append(standing, d)
	}
	if len(standing) == 0 {
		return m
	}
	v, space := embedProposal(change)
	if v == nil {
		return m
	}
	var bestSame, bestSimilar float32
	for i := range standing {
		d := &standing[i]
		if d.EmbedSpace != space || len(d.Vector) == 0 {
			dv, dspace := embedProposal(d.Change)
			if dv == nil || dspace != space {
				continue
			}
			d.Vector, d.EmbedSpace = dv, dspace
			db.Set(proposalDeclinesTable, d.ID, *d)
		}
		switch sim := Cosine(v, d.Vector); {
		case sim >= declineSameSim && sim > bestSame:
			bestSame, m.Same = sim, d
		case sim >= declineSimilarSim && sim < declineSameSim && sim > bestSimilar:
			bestSimilar, m.Similar = sim, d
		}
	}
	return m
}

// likeDeclinedNote is the line a proposal's card carries when it looks like
// one the owner declined, so the owner decides with that one in view.
func likeDeclinedNote(d *proposalDecline) string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("It looks like one you declined on %s: %q.", d.DeclinedAt.Local().Format("Jan 2"), d.Change)
}
