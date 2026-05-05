// Per-user, per-topic fact store for Answer. Mirrors servitor's facts
// system but keyed by (user, topic, key) instead of (appliance, key).
// Facts capture discrete pieces of knowledge the orchestrator picked
// up during research — they get pre-loaded as context on future
// questions about the same topic, so the orchestrator doesn't have
// to re-search what it already learned.
//
// Topic is the orchestrator's free-form namespace string. Examples:
//   "teamspeak"       — facts about the user's TS3 setup
//   "llama-server"    — config, version, quirks
//   "gohort.watchers" — design decisions, current behavior
// The orchestrator picks the topic based on the question; facts under
// matching topics get surfaced before the next question on that domain.

package answer

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const factsTable = "answer_facts"

// AnswerFact is one stored piece of knowledge.
type AnswerFact struct {
	ID      string   `json:"id"`             // user + ":" + topic + ":" + key
	Topic   string   `json:"topic"`          // free-form namespace, e.g. "teamspeak"
	Key     string   `json:"key"`            // the fact's name, e.g. "ts3.api_port"
	Value   string   `json:"value"`          // the fact's content
	Tags    []string `json:"tags,omitempty"` // optional categorization
	Sources []string `json:"sources,omitempty"` // URLs the fact was learned from
	Updated string   `json:"updated"`        // RFC3339
	TTL     string   `json:"ttl,omitempty"`  // "short" (1h) or "long" (default, 30d)
}

// factID computes the storage key for a fact.
func factID(user, topic, key string) string {
	return user + ":" + topic + ":" + key
}

// StoreAnswerFact writes a fact, overwriting any prior value for the
// same (user, topic, key). user must be non-empty for ownership scope.
func StoreAnswerFact(udb Database, user, topic, key, value string, tags, sources []string, ttl string) {
	if udb == nil || user == "" || topic == "" || key == "" {
		return
	}
	if ttl != "short" && ttl != "long" {
		ttl = "long"
	}
	id := factID(user, topic, key)
	udb.Set(factsTable, id, AnswerFact{
		ID:      id,
		Topic:   topic,
		Key:     key,
		Value:   value,
		Tags:    tags,
		Sources: sources,
		TTL:     ttl,
		Updated: time.Now().Format(time.RFC3339),
	})
}

// FactsForTopic returns all non-stale facts a user has stored under topic.
// Stale facts (past their TTL) are filtered out so the orchestrator only
// sees information it can still trust without re-verification.
func FactsForTopic(udb Database, user, topic string) []AnswerFact {
	if udb == nil || user == "" || topic == "" {
		return nil
	}
	prefix := user + ":" + topic + ":"
	var out []AnswerFact
	for _, k := range udb.Keys(factsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var f AnswerFact
		if !udb.Get(factsTable, k, &f) {
			continue
		}
		if isFactStale(f) {
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// AllFactsForUser returns every fact a user has stored across all
// topics. Used by the admin / inspection UI; the orchestrator should
// generally read by topic for relevance.
func AllFactsForUser(udb Database, user string) []AnswerFact {
	if udb == nil || user == "" {
		return nil
	}
	prefix := user + ":"
	var out []AnswerFact
	for _, k := range udb.Keys(factsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var f AnswerFact
		if udb.Get(factsTable, k, &f) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Topic != out[j].Topic {
			return out[i].Topic < out[j].Topic
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// DeleteAnswerFact removes one fact by its (user, topic, key) tuple.
// Idempotent — missing facts return nil.
func DeleteAnswerFact(udb Database, user, topic, key string) error {
	if udb == nil || user == "" || topic == "" || key == "" {
		return fmt.Errorf("user, topic, and key required")
	}
	udb.Unset(factsTable, factID(user, topic, key))
	return nil
}

// isFactStale reports whether a fact has aged past its TTL. Short TTL
// is for time-sensitive findings (someone's online status, ephemeral
// config); long TTL is for stable knowledge (server addresses, API
// shapes).
func isFactStale(f AnswerFact) bool {
	t, err := time.Parse(time.RFC3339, f.Updated)
	if err != nil {
		return false
	}
	limit := 30 * 24 * time.Hour // long
	if f.TTL == "short" {
		limit = time.Hour
	}
	return time.Since(t) > limit
}

// FormatFactsForPrompt renders a fact list as a compact prompt-ready
// block. Used by the orchestrator system prompt to inject prior
// knowledge before a new research pass.
func FormatFactsForPrompt(facts []AnswerFact) string {
	if len(facts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range facts {
		fmt.Fprintf(&b, "- %s: %s", f.Key, f.Value)
		if len(f.Sources) > 0 {
			fmt.Fprintf(&b, "  (sources: %s)", strings.Join(f.Sources, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ListAnswerTopics returns every distinct topic a user has facts under.
// Useful for surfacing "what do I already know about?" in admin UI
// and for the orchestrator's topic-suggestion step.
func ListAnswerTopics(udb Database, user string) []string {
	facts := AllFactsForUser(udb, user)
	seen := map[string]bool{}
	var out []string
	for _, f := range facts {
		if !seen[f.Topic] {
			seen[f.Topic] = true
			out = append(out, f.Topic)
		}
	}
	sort.Strings(out)
	return out
}
