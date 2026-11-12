package keystore

import (
	"encoding/json"
	"strings"
	"testing"
)

// memStore is a Store that copies values in and out through JSON, as a real
// store copies them through its encoding, so a test cannot pass by holding a
// pointer to what it saved.
type memStore struct{ rows map[string][]byte }

func newMem() *memStore { return &memStore{rows: map[string][]byte{}} }

func (m *memStore) Get(table, key string, out interface{}) bool {
	b, ok := m.rows[table+"/"+key]
	return ok && json.Unmarshal(b, out) == nil
}
func (m *memStore) Set(table, key string, v interface{}) {
	b, _ := json.Marshal(v)
	m.rows[table+"/"+key] = b
}
func (m *memStore) CryptSet(table, key string, v interface{}) { m.Set(table, key, v) }
func (m *memStore) Unset(table, key string)                   { delete(m.rows, table+"/"+key) }
func (m *memStore) Keys(table string) []string {
	var out []string
	for k := range m.rows {
		if t, key, ok := strings.Cut(k, "/"); ok && t == table {
			out = append(out, key)
		}
	}
	return out
}

func TestAKeyIsKeptOnceAndShownOnlyByItsEnd(t *testing.T) {
	db := newMem()
	e, err := Save(db, Entry{Name: "Google Gemini", Addresses: []string{"https://GenerativeLanguage.googleapis.com/"}}, "AIzaSyExampleKey0000WXYZ")
	if err != nil {
		t.Fatal(err)
	}
	if e.ID == "" || e.Kind != KindKey || len(e.Addresses) != 1 || e.Addresses[0] != "https://generativelanguage.googleapis.com" {
		t.Fatalf("saved as %+v", e)
	}
	if v, ok := Secret(db, e.ID); !ok || v != "AIzaSyExampleKey0000WXYZ" {
		t.Errorf("the key is not kept: %q", v)
	}
	if h := Hint(db, e); !strings.HasPrefix(h, "ends in ...WXYZ, given ") {
		t.Errorf("hint %q", h)
	}
	if list := List(db); len(list) != 1 || list[0].ID != e.ID {
		t.Errorf("the secret row was listed as an entry, or the entry is missing: %+v", list)
	}
	if !e.Covers("https://generativelanguage.googleapis.com/v1beta/models") || e.Covers("https://api.openai.com") {
		t.Error("an entry covers its own host and nothing else")
	}
}

// A key goes only where it was given for: moving an entry's addresses without
// giving the key again is refused, and an edit that leaves both alone keeps
// the key.
func TestMovingAnEntryNeedsTheKeyAgain(t *testing.T) {
	db := newMem()
	e, err := Save(db, Entry{Name: "OpenAI", Addresses: []string{"https://api.openai.com"}}, "sk-original-key-0000ABCD")
	if err != nil {
		t.Fatal(err)
	}
	moved := e
	moved.Addresses = []string{"https://evil.example.com"}
	if _, err := Save(db, moved, ""); err == nil {
		t.Fatal("the key was moved to a new address without being given again")
	}
	renamed := e
	renamed.Name = "OpenAI (work)"
	if _, err := Save(db, renamed, ""); err != nil {
		t.Fatalf("a rename was refused: %v", err)
	}
	if v, _ := Secret(db, e.ID); v != "sk-original-key-0000ABCD" {
		t.Errorf("a rename lost the key: %q", v)
	}
	if _, err := Save(db, moved, "sk-a-new-key"); err != nil {
		t.Errorf("moving with the key given again was refused: %v", err)
	}
}

func TestAnEntryNeedsANameAKeyAndAnAddress(t *testing.T) {
	db := newMem()
	for _, c := range []struct {
		e   Entry
		key string
	}{
		{Entry{Addresses: []string{"https://api.openai.com"}}, "k"},
		{Entry{Name: "x"}, "k"},
		{Entry{Name: "x", Addresses: []string{"api.openai.com"}}, "k"},
		{Entry{Name: "x", Addresses: []string{"https://api.openai.com"}}, ""},
		{Entry{Name: "x", Kind: KindAWS, Addresses: []string{BedrockAddress}}, "no-colon"},
	} {
		if _, err := Save(db, c.e, c.key); err == nil {
			t.Errorf("%+v with key %q was accepted", c.e, c.key)
		}
	}
	if _, err := Save(db, Entry{Name: "x", Addresses: []string{"https://api.openai.com"}}, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(db, Entry{Name: "X", Addresses: []string{"https://api.anthropic.com"}}, "k"); err == nil {
		t.Error("two entries share a name")
	}
}

// An AWS entry is a key pair or a profile, for Bedrock in any region.
func TestAnAWSEntrySignsWithAPairOrAProfile(t *testing.T) {
	db := newMem()
	p, err := Save(db, Entry{Name: "Bedrock (work)", Kind: KindAWS, Addresses: []string{"bedrock"}, Profile: "work"}, "")
	if err != nil {
		t.Fatalf("a profile entry was refused: %v", err)
	}
	if h := Hint(db, p); h != "signs with AWS profile work, given "+p.Set.Format("2 Jan 2006") {
		t.Errorf("hint %q", h)
	}
	k, err := Save(db, Entry{Name: "Bedrock", Kind: KindAWS, Addresses: []string{"bedrock"}}, "TESTKEYIDTESTKEYID12:secretsecret")
	if err != nil {
		t.Fatal(err)
	}
	if h := Hint(db, k); !strings.HasPrefix(h, "key pair TEST...ID12") {
		t.Errorf("hint %q", h)
	}
	if !k.Covers("bedrock") {
		t.Error("an AWS entry does not cover Bedrock")
	}
}

// An entry something names cannot be deleted out from under it.
func TestAnEntryInUseIsNotDeleted(t *testing.T) {
	db := newMem()
	e, _ := Save(db, Entry{Name: "Anthropic", Addresses: []string{"https://api.anthropic.com"}}, "sk-ant-x")
	using := map[string]bool{e.ID: true}
	RegisterUse(func(id string) []string {
		if using[id] {
			return []string{"Lead model"}
		}
		return nil
	})
	var heard []string
	OnChange(func(id string) { heard = append(heard, id) })
	if err := Delete(db, e.ID); err == nil || !strings.Contains(err.Error(), "Lead model") {
		t.Fatalf("an entry in use was deleted, or the refusal does not say by what: %v", err)
	}
	using[e.ID] = false
	if err := Delete(db, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := Get(db, e.ID); ok {
		t.Error("the entry is still there")
	}
	if _, ok := Secret(db, e.ID); ok {
		t.Error("its key is still kept")
	}
	if len(heard) != 1 || heard[0] != e.ID {
		t.Errorf("a delete was not announced: %v", heard)
	}
}
