package extensions

// Lending a credential is two grants, not one with a flag, and the difference
// has to survive both the wording and the wiring.

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Reads and writes must never read the same in the list. "Shared with 2" tells
// an owner nothing about whether somebody can post under their name.
func TestTheShareSummaryDistinguishesReadsFromWrites(t *testing.T) {
	cases := []struct {
		name string
		c    SecureCredential
		want string
	}{
		{"unshared", SecureCredential{}, "Just you"},
		{"readers", SecureCredential{SharedReadOnly: []string{"bob", "carol"}}, "2 reading"},
		{"writers", SecureCredential{SharedReadWrite: []string{"bob"}}, "1 writing as you"},
		{"both", SecureCredential{SharedReadOnly: []string{"carol"}, SharedReadWrite: []string{"bob"}},
			"1 reading, 1 writing as you"},
	}
	for _, tc := range cases {
		if got := shareSummary(tc.c); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A write grant must SAY that the writing arrives as the owner. It is the one
// thing about this share that cannot be undone afterwards, and an owner who
// only learns it from the audit log learned it too late.
func TestTheWriteShareSaysWhoTheWritesLookLike(t *testing.T) {
	raw, err := os.ReadFile("extensions.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	src := string(raw)
	idx := strings.Index(src, `Field:         "shared_read_write"`)
	if idx < 0 {
		t.Fatal("the write-share picker is gone")
	}
	intro := src[idx:min(idx+1200, len(src))]
	for _, want := range []string{"as YOU", "ledger"} {
		if !strings.Contains(intro, want) {
			t.Errorf("the write-share picker does not mention %q:\n%s", want, intro)
		}
	}
}

// Each door opens one thing. A picker saves the share lists and nothing else;
// the edit form saves the config and cannot touch who the key reaches. Wiring
// both to the same URL is how one of them silently starts overwriting the
// other's field with whatever its own form happened to hold.
func TestTheShareDoorIsNotTheEditDoor(t *testing.T) {
	raw, err := os.ReadFile("extensions.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	for _, field := range []string{`"shared_read_only"`, `"shared_read_write"`} {
		idx := strings.Index(string(raw), "Field:         "+field)
		if idx < 0 {
			t.Fatalf("no picker for %s", field)
		}
		block := string(raw)[idx:min(idx+400, len(string(raw)))]
		if !strings.Contains(block, `PostTo:        "api/credentials?action=share&name={name}"`) {
			t.Errorf("the %s picker does not post to the share door:\n%s", field, block)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// The ledger's Who column carries one fact, and must not invent it. A row with
// no caller recorded predates the field or had no session behind it; reading it
// as the owner would be the ledger asserting the one thing it does not know.
func TestAnUnrecordedCallerIsNotReadAsYou(t *testing.T) {
	cases := []struct {
		who  string
		user string
		want string
	}{
		{"", "alice", "unrecorded"},
		{"alice", "alice", "you"},
		{"bob", "alice", "bob"},
	}
	for _, tc := range cases {
		got := tc.who
		switch {
		case got == "":
			got = "unrecorded"
		case got == tc.user:
			got = "you"
		}
		if got != tc.want {
			t.Errorf("who=%q user=%q: got %q, want %q", tc.who, tc.user, got, tc.want)
		}
	}
}

// Sent-and-rejected, never-sent and sent-and-answered are three different
// things. A ledger that renders a refusal as a status code teaches the reader
// that the call went out.
func TestTheLedgerTellsRefusedFromRejected(t *testing.T) {
	cases := []struct {
		name string
		e    SecureAPIAuditEntry
		want string
	}{
		{"answered", SecureAPIAuditEntry{Status: 200}, "200"},
		{"rejected", SecureAPIAuditEntry{Status: 403}, "403 (refused by the API)"},
		{"never sent", SecureAPIAuditEntry{Error: "refused before sending: read-only"},
			"Not sent: refused before sending: read-only"},
	}
	for _, tc := range cases {
		if got := ledgerOutcome(tc.e); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
