package orchestrate

// Reference Memory chunks live in ONE global store, isolated by a string
// prefix rather than by a partition — so the comparison IS the boundary.

import (
	"os"
	"strings"
	"testing"
)

func TestSourceScopeMatchesTheCorpusAndItsTopics(t *testing.T) {
	p := agentKnowledgePrefix("alice", "agent-1")
	for _, in := range []string{p, p + ":runbooks", p + ":a:b"} {
		if !sourceInScope(in, p) {
			t.Errorf("%q belongs to this corpus", in)
		}
	}
}

// The reason this is a predicate and not a bare HasPrefix. Nothing leaks today
// — user agent ids are UUIDs and no two hand-written seed ids stand in this
// relation — so the boundary held by accident of naming, and adding one id
// beside an existing one would have opened it with nothing failing.
func TestANeighbouringCorpusIsNotSweptIn(t *testing.T) {
	p := agentKnowledgePrefix("alice", "app-guides")
	for _, other := range []string{
		agentKnowledgePrefix("alice", "app-guides-author"),
		agentKnowledgePrefix("alice", "app-guides-author") + ":runbooks",
		agentKnowledgePrefix("alice", "app-guides2"),
	} {
		if sourceInScope(other, p) {
			t.Errorf("%q is a DIFFERENT agent's corpus and must not match %q", other, p)
		}
	}
}

// The user half of the prefix has to hold the same way.
func TestAnotherUsersCorpusIsNotSweptIn(t *testing.T) {
	p := agentKnowledgePrefix("alice", "agent-1")
	for _, other := range []string{
		agentKnowledgePrefix("alice2", "agent-1"),
		agentKnowledgePrefix("alicebob", "agent-1"),
	} {
		if sourceInScope(other, p) {
			t.Errorf("%q belongs to another user and must not match %q", other, p)
		}
	}
}

// Scoped app-agent users carry colons of their own (app:servitor:<id>), which
// is the case most likely to be got wrong by hand.
func TestScopedAppAgentUsersStaySeparate(t *testing.T) {
	one := agentKnowledgePrefix("app:servitor:1", "app-servitor-investigator")
	ten := agentKnowledgePrefix("app:servitor:10", "app-servitor-investigator")
	if sourceInScope(ten, one) {
		t.Error("appliance 10's corpus must not fall inside appliance 1's")
	}
	if !sourceInScope(one+":facts", one) {
		t.Error("its own topics must still match")
	}
}

// The loose form cannot come back by hand. Ten sites checked this by
// open-coding strings.HasPrefix, and each was one edit away from being wrong;
// a predicate only helps while everyone uses it.
func TestNobodyOpenCodesTheScopeCheck(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		body, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "HasPrefix(c.Source, prefix)") {
			t.Errorf("%s compares a chunk Source by bare prefix — use sourceInScope, "+
				"or a neighbouring corpus whose name merely starts the same way gets swept in", n)
		}
	}
}
