package orchestrate

import (
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
)

// TestRecordAgentTopicRecencyOrder: new slugs append; reusing an existing slug
// moves it to the end (most-recently-used), with no duplicates. The tail order
// is what the Known-topics cap keeps.
func TestRecordAgentTopicRecencyOrder(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	order := func() string { return strings.Join(listAgentTopics(db, "u", "a"), ",") }

	for _, top := range []string{"alpha", "beta", "gamma"} {
		recordAgentTopic(db, "u", "a", top)
	}
	if got := order(); got != "alpha,beta,gamma" {
		t.Fatalf("append order: got %q", got)
	}

	recordAgentTopic(db, "u", "a", "alpha") // reuse → moves to end
	if got := order(); got != "beta,gamma,alpha" {
		t.Fatalf("reuse should move to end: got %q", got)
	}

	recordAgentTopic(db, "u", "a", "beta") // reuse again, still no dupes
	if got := order(); got != "gamma,alpha,beta" {
		t.Fatalf("got %q", got)
	}
}
