package accountname

import (
	"strings"
	"testing"
)

func TestANameIsLowercasedAndHeldToPlainCharacters(t *testing.T) {
	for raw, want := range map[string]string{"  Alice@Example.TEST ": "alice@example.test", "bob_ops+gohort@example.test": "bob_ops+gohort@example.test"} {
		if got, err := Canonical(raw); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", raw, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "alice@example.test:bob@example.test", "../etc", "a b@example.test", "alïce@example.test", "a/b@example.test", strings.Repeat("a", MaxLen+1)} {
		if got, err := Canonical(bad); err == nil {
			t.Errorf("%q was accepted as %q", bad, got)
		}
	}
}

func TestOneNameInAnotherCaseIsTheSameName(t *testing.T) {
	existing := []string{"Alice@example.test", "bob@example.test"}
	if got, ok := Clash(existing, "alice@example.test"); !ok || got != "Alice@example.test" {
		t.Errorf("clash: %q %v", got, ok)
	}
	if _, ok := Clash(existing, "carol@example.test"); ok {
		t.Error("a new name clashed")
	}
	if got := Matches([]string{"Alice@example.test", "alice@Example.test", "bob@example.test"}, " ALICE@example.test"); len(got) != 2 {
		t.Errorf("matches: %v", got)
	}
}

func TestAuditNamesWhatTheRuleWouldRefuse(t *testing.T) {
	got := strings.Join(Audit([]string{"alice@example.test", "Alice@example.test", "eve@example.test:alice", "bob@example.test"}), "\n")
	for _, want := range []string{`"Alice@example.test" and "alice@example.test" are one name`, `"eve@example.test:alice"`} {
		if !strings.Contains(got, want) {
			t.Errorf("audit lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "bob@example.test") {
		t.Errorf("a fine name was reported:\n%s", got)
	}
	if len(Audit([]string{"alice@example.test", "bob@example.test"})) != 0 {
		t.Error("clean names were reported")
	}
}
