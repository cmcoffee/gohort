package admin

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/keystore"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Keystore section's API: a key added, listed with only its end shown,
// read for its edit form with the key never sent back, kept on a blank or
// "(unchanged)" save, and deleted.
func TestTheKeystoreKeepsAKeyAndNeverSendsItBack(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = prev })
	a := &AdminApp{}
	call := func(method, url string) *jsonapi.Call {
		return &jsonapi.Call{R: httptest.NewRequest(method, url, nil), W: httptest.NewRecorder(), User: "root"}
	}
	const key = "AIzaSyExampleExample0000WXYZ"
	out, err := a.handleKeystoreSave(call("POST", "/api/keystore"), keystoreForm{Name: "Google Gemini", Kind: keystore.KindKey,
		Addresses: []string{"https://generativelanguage.googleapis.com"}, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := out.(map[string]any)["id"].(string)
	if id == "" {
		t.Fatalf("no id came back: %v", out)
	}

	rows, err := a.handleKeystore(call("GET", "/api/keystore"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rows)
	if strings.Contains(string(b), key) {
		t.Fatalf("the list sent the key back: %s", b)
	}
	if !strings.Contains(string(b), "ends in …WXYZ") || !strings.Contains(string(b), "not used yet") {
		t.Errorf("the row does not say which key is kept, or what uses it: %s", b)
	}

	form, err := a.handleKeystore(call("GET", "/api/keystore?id="+id))
	if err != nil {
		t.Fatal(err)
	}
	if f := form.(keystoreForm); f.Key != "" || f.AWSPair != "" || !strings.HasPrefix(f.Kept, "ends in …WXYZ") {
		t.Errorf("the edit form sent a key back, or does not say which is kept: %+v", f)
	}

	if _, err := a.handleKeystoreSave(call("POST", "/api/keystore"), keystoreForm{ID: id, Name: "Gemini (home)",
		Addresses: []string{"https://generativelanguage.googleapis.com"}, Key: secretUnchanged}); err != nil {
		t.Fatalf("a rename with the key left unchanged was refused: %v", err)
	}
	if v, _ := keystore.Secret(db, id); v != key {
		t.Errorf("an unchanged save lost the key: %q", v)
	}

	if _, err := a.handleKeystoreDelete(call("DELETE", "/api/keystore?id="+id), struct{}{}); err != nil {
		t.Fatal(err)
	}
	if len(keystore.List(db)) != 0 {
		t.Error("the entry was not deleted")
	}
}
