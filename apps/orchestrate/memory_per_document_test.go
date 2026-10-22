package orchestrate

import (
	"strings"
	"testing"
)

// An agent whose memory follows the document (MemoryPerDocument) sees what it
// saved about the open document and nothing it saved about another; what it
// saved with no document open, and anything outside the per-document topics,
// stays shared. One Guide Author works on every guide, and a finding about one
// guide surfacing in another is it doing what nobody asked.
func TestMemoryFollowsTheDocument(t *testing.T) {
	turn := func(doc string) *chatTurn {
		return &chatTurn{user: "u", agent: AgentRecord{ID: "author", MemoryPerDocument: true},
			session: &ChatSession{ID: "s", AppContext: doc}}
	}
	saved := func(doc, topic string) string {
		if doc != "" {
			topic = docTopic(doc) + "." + topic
		}
		return knowledgeSource("u", "author", topic)
	}
	a, b := turn("guide-A-1"), turn("guide-B-2")
	if !a.seesFinding(saved("guide-A-1", "style")) {
		t.Error("a guide's own finding must be visible in its conversations")
	}
	if b.seesFinding(saved("guide-A-1", "style")) {
		t.Error("another guide's finding must stay out")
	}
	if !b.seesFinding(saved("", "general")) || !b.seesFinding(knowledgeSource("u", "author", "attachments")) {
		t.Error("what is not about one document stays shared")
	}
	if none := turn(""); none.seesFinding(saved("guide-A-1", "style")) {
		t.Error("with no document open, no document's findings are seen")
	}
	// Not per document: everything is seen, as before.
	plain := &chatTurn{user: "u", agent: AgentRecord{ID: "author"}, session: &ChatSession{AppContext: "guide-B-2"}}
	if !plain.seesFinding(saved("guide-A-1", "style")) {
		t.Error("an agent without per-document memory must see its whole corpus")
	}
	// The topic survives normalizeTopic, or saves and reads would disagree.
	if dt := docTopic("guide-A-1"); normalizeTopic(dt) != dt || !strings.HasPrefix(dt, docTopicPrefix) {
		t.Errorf("docTopic %q does not survive normalizeTopic", dt)
	}
	// The topic hints show the open document's topics bare and no other's.
	got := a.visibleTopics([]string{"general", docTopic("guide-A-1") + ".style", docTopic("guide-B-2") + ".setup"})
	if strings.Join(got, ",") != "general,style" {
		t.Errorf("topic hints = %v, want general and the open guide's style", got)
	}
}

// Every way into the agent's own findings goes through seesFinding, and a save
// in a document's conversation lands under that document: a gate on the search
// alone left fetch-by-id and the save-time dedup reaching across documents.
func TestEveryWayIntoFindingsIsPerDocument(t *testing.T) {
	src := readSourceFile(t, "knowledge.go")
	if n := strings.Count(src, "t.seesFinding("); n < 3 {
		t.Errorf("seesFinding guards %d sites in knowledge.go; fetch-by-id and both save-time dedups need it", n)
	}
	if !strings.Contains(src, "if doc := t.memoryDocument(); doc != \"\" {\n\t\ttopic = docTopic(doc) + \".\" + topic") {
		t.Error("memorySave no longer files a finding under the open document")
	}
	if !strings.Contains(readSourceFile(t, "memory_scope.go"), "if t.seesFinding(h.Source) {") {
		t.Error("searchOwnKnowledge no longer drops another document's findings")
	}
}
