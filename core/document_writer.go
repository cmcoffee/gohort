// Document targets — the WRITE-side mirror of ReferenceSource (which is read/
// pull). A writer app (guides today; techwriter / wikis later) registers itself
// as a target that OTHER apps can push content INTO — e.g. servitor pushing an
// investigation finding into a user's guide as a new section — WITHOUT the
// producer importing the writer. The producer only ever talks to this registry.
//
// Symmetry with reference_source.go is deliberate: one registry for "pull
// knowledge from another app," one for "push a document section into another
// app," both keyed by a stable kind and resolved per user in the target's own
// store.

package core

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// DocItem is one writable document a user can push into (a guide, etc.), or a
// registered target kind (ID = kind, Title = label) in the target picker.
type DocItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// DocumentTarget is implemented by a writer app to accept pushed sections. It
// binds its OWN database at registration; List/Append take only the user and
// resolve that user's documents from the target's own store — the producer never
// sees the target's database.
type DocumentTarget interface {
	// Kind is a stable identifier ("guide") used to route an append.
	Kind() string
	// Label is the human group name ("Guides").
	Label() string
	// List returns the user's writable documents, for a picker or a name match.
	List(user string) []DocItem
	// Append adds a section to a document. When docID is empty a NEW document
	// titled newDocTitle is created first. Returns the document ID written to.
	// The target enforces the user's write access to docID.
	Append(ctx context.Context, user, docID, newDocTitle, sectionTitle, markdown string) (string, error)
}

var (
	docTargetsMu sync.RWMutex
	docTargets   = map[string]DocumentTarget{}
)

// RegisterDocumentTarget registers a writer. Re-registering the same Kind
// replaces it. Call once where the app's DB is live (route registration).
func RegisterDocumentTarget(t DocumentTarget) {
	if t == nil || t.Kind() == "" {
		return
	}
	docTargetsMu.Lock()
	docTargets[t.Kind()] = t
	docTargetsMu.Unlock()
}

// DocumentTargetKinds returns registered target kinds (ID=kind, Title=label),
// sorted by label — for a producer's "push to…" picker.
func DocumentTargetKinds() []DocItem {
	docTargetsMu.RLock()
	defer docTargetsMu.RUnlock()
	out := make([]DocItem, 0, len(docTargets))
	for kind, t := range docTargets {
		out = append(out, DocItem{ID: kind, Title: t.Label()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// ListDocuments returns a user's writable documents for one target kind. Empty
// when the kind isn't registered.
func ListDocuments(user, kind string) []DocItem {
	docTargetsMu.RLock()
	t := docTargets[kind]
	docTargetsMu.RUnlock()
	if t == nil {
		return nil
	}
	return t.List(user)
}

// AppendToDocument pushes a section into a target document. An empty docID
// creates a new document titled newDocTitle. Returns the document ID written to,
// or an error (unknown kind / no access / not found).
func AppendToDocument(ctx context.Context, user, kind, docID, newDocTitle, sectionTitle, markdown string) (string, error) {
	docTargetsMu.RLock()
	t := docTargets[kind]
	docTargetsMu.RUnlock()
	if t == nil {
		return "", fmt.Errorf("no document target registered for kind %q", kind)
	}
	return t.Append(ctx, user, docID, newDocTitle, sectionTitle, markdown)
}

// ReferencingDocumentTarget is an optional DocumentTarget capability: filter the
// user's documents to those that reference a given source (a reference-source
// kind + item id). Lets a producer offer "push into a doc that's already ABOUT
// this thing" — e.g. servitor pushing a finding only into guides that have this
// System attached as a source. Targets that don't implement it fall back to List.
type ReferencingDocumentTarget interface {
	DocumentTarget
	ListReferencing(user, srcKind, srcItemID string) []DocItem
}

// ListDocumentsReferencing returns the user's documents of the given target kind
// that reference (srcKind, srcItemID). Falls back to the full list when the
// target isn't reference-aware.
func ListDocumentsReferencing(user, kind, srcKind, srcItemID string) []DocItem {
	docTargetsMu.RLock()
	t := docTargets[kind]
	docTargetsMu.RUnlock()
	if t == nil {
		return nil
	}
	if rt, ok := t.(ReferencingDocumentTarget); ok {
		return rt.ListReferencing(user, srcKind, srcItemID)
	}
	return t.List(user)
}

// HasDocumentTarget reports whether a writer target is registered for kind.
func HasDocumentTarget(kind string) bool {
	docTargetsMu.RLock()
	_, ok := docTargets[kind]
	docTargetsMu.RUnlock()
	return ok
}
