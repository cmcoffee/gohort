// promotion_requests.go — the bottom-up "publish my resource deployment-wide"
// request queue (namespacing phase 5, deliverable 6). A user asks for one of their
// OWN resources to be promoted into the global catalog; an admin approves or
// denies. This is the storage + lifecycle seam; each app decides what "approve"
// DOES for its kind (a tool → Shared; a credential → global per-user shape; an
// agent → global copy). Requests live in the app-wide store, keyed by
// (kind, owner, name) so a re-request updates the existing row rather than piling
// up duplicates.
//
// NOTE: distinct from promotion.go, which is the unrelated SUB-SESSION promotion
// router (routing a follow-up turn to a sub-agent). Same English word, different
// concern — this file is about publishing resources, not conversation routing.
package core

import (
	"sort"
	"strings"
	"time"
)

const promotionRequestsTable = "promotion_requests"

// Promotion request states.
const (
	PromotionPendingState  = "pending"
	PromotionApprovedState = "approved"
	PromotionDeniedState   = "denied"
)

// PromotionRequest is one user's ask to publish a resource they own. Kind is
// "tool" | "credential" | "agent". State is one of the Promotion*State constants.
type PromotionRequest struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Note      string    `json:"note,omitempty"`
	Created   time.Time `json:"created"`
	State     string    `json:"state"`
	DecidedBy string    `json:"decided_by,omitempty"`
}

func promotionRequestKey(kind, owner, name string) string { return kind + ":" + owner + ":" + name }

// CreatePromotionRequest records a PENDING promotion for (kind, owner, name),
// idempotent per that triple: a re-request (including one that follows a denial)
// overwrites the existing row back to pending with the new note. Returns an error
// on missing fields.
func CreatePromotionRequest(db Database, owner, kind, name, note string) error {
	if db == nil {
		return errString("db not initialized")
	}
	owner, kind, name = strings.TrimSpace(owner), strings.TrimSpace(kind), strings.TrimSpace(name)
	if owner == "" || kind == "" || name == "" {
		return errString("owner, kind and name are required")
	}
	id := promotionRequestKey(kind, owner, name)
	db.Set(promotionRequestsTable, id, PromotionRequest{
		ID: id, Owner: owner, Kind: kind, Name: name, Note: strings.TrimSpace(note),
		Created: time.Now(), State: PromotionPendingState,
	})
	return nil
}

// ListPromotionRequests returns every request, newest first. Pass onlyPending to
// restrict to the actionable queue.
func ListPromotionRequests(db Database, onlyPending bool) []PromotionRequest {
	out := []PromotionRequest{}
	if db == nil {
		return out
	}
	for _, k := range db.Keys(promotionRequestsTable) {
		var req PromotionRequest
		if db.Get(promotionRequestsTable, k, &req) {
			if onlyPending && req.State != PromotionPendingState {
				continue
			}
			out = append(out, req)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// GetPromotionRequest returns one request by ID.
func GetPromotionRequest(db Database, id string) (PromotionRequest, bool) {
	var req PromotionRequest
	if db == nil {
		return req, false
	}
	ok := db.Get(promotionRequestsTable, id, &req)
	return req, ok
}

// SetPromotionRequestState records an admin decision (approved / denied) and who
// made it. The caller performs the kind-specific side effect (Share the tool,
// etc.) BEFORE marking approved, so a failed side effect leaves the row pending.
func SetPromotionRequestState(db Database, id, state, decidedBy string) error {
	if db == nil {
		return errString("db not initialized")
	}
	var req PromotionRequest
	if !db.Get(promotionRequestsTable, id, &req) {
		return errString("no promotion request " + id)
	}
	req.State = state
	req.DecidedBy = decidedBy
	db.Set(promotionRequestsTable, id, req)
	return nil
}

// PendingPromotion reports whether (kind, owner, name) currently has a pending
// request — the "Requested" indicator on the owner's resource list.
func PendingPromotion(db Database, owner, kind, name string) bool {
	req, ok := GetPromotionRequest(db, promotionRequestKey(kind, owner, name))
	return ok && req.State == PromotionPendingState
}
