package orchestrate

// Replacing a working credential's key needs the person's approval.
//
// An agent may land the FIRST key of a credential it received mid-flow (a
// self-registration response), through store_credential_secret. Once the
// credential has a key the store refuses an agent's write (ErrCredentialHasKey),
// so a model that misreads a flow, or holds a stale token, cannot quietly break
// a credential that works. A genuine rotation waits here, encrypted and
// short-lived, while a card in the chat asks "replace the key on X?"; the card
// itself never carries the key.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	keyReplacementTable = "credential_key_replacements"
	// keyReplacementTTL is how long a held replacement waits for an answer.
	keyReplacementTTL = time.Hour
)

type heldKeyReplacement struct {
	Secret string    `json:"secret"`
	At     time.Time `json:"at"`
}

func keyReplacementID(user, name string) string { return user + "\x00" + name }

// holdKeyReplacement keeps a replacement key until the person answers.
func holdKeyReplacement(user, name, secret string) {
	if RootDB == nil {
		return
	}
	RootDB.CryptSet(keyReplacementTable, keyReplacementID(user, name), heldKeyReplacement{Secret: secret, At: time.Now()})
}

// takeKeyReplacement returns and forgets a held replacement, if it has not
// expired.
func takeKeyReplacement(user, name string) (string, bool) {
	if RootDB == nil {
		return "", false
	}
	var h heldKeyReplacement
	id := keyReplacementID(user, name)
	ok := RootDB.Get(keyReplacementTable, id, &h)
	RootDB.Unset(keyReplacementTable, id)
	if !ok || h.Secret == "" || time.Since(h.At) > keyReplacementTTL {
		return "", false
	}
	return h.Secret, true
}

// storeAgentKey is store_credential_secret's work: land a first key, or hold
// a replacement and ask. The reply is for the model.
func (t *chatTurn) storeAgentKey(name, secret string) (string, error) {
	user := ""
	if t != nil {
		user = t.user
	}
	admin := UserIsAdmin(user)
	err := Secure().StoreAgentSecret(user, admin, name, secret)
	switch {
	case err == nil:
		_, enabled, _ := Secure().CredentialStatusOwned(user, name)
		status := "It still needs to be ENABLED (the setup card, or Extensions > API credentials) before calls go through."
		if enabled {
			status = "The credential is enabled: dispatch through it now (fetch_url_" + name + " or your wrapped tool)."
		}
		return fmt.Sprintf("Key stored encrypted on credential %q: do NOT repeat the value in chat. %s Verify with check_credential(%q).", name, status, name), nil
	case errors.Is(err, ErrCredentialHasKey):
		holdKeyReplacement(user, name, secret)
		t.emitKeyReplacementCard(name)
		return fmt.Sprintf("Credential %q already has a working key, so it was NOT replaced. A card in the chat asks the user to approve replacing it with the one you received (held server-side for an hour; the card never shows it). Do not call store_credential_secret again for %q, and do not repeat the key: tell the user why the key should change and wait for their answer.", name, name), nil
	}
	return "", err
}

// emitKeyReplacementCard asks the person to approve replacing a key. The card
// names the credential only.
func (t *chatTurn) emitKeyReplacementCard(name string) {
	if t == nil || t.sse == nil {
		return
	}
	id := "credrotate-" + name
	data := map[string]string{"name": name}
	t.sse.Send(map[string]any{"kind": "block", "type": "credential_rotate", "id": id, "title": name, "data": data})
	if t.session != nil {
		blk := UIBlock{Type: "credential_rotate", ID: id, Title: name, Data: data}
		t.toolMu.Lock()
		t.session.upsert_ui_block(blk, func(b *UIBlock) bool { return b.ID == id })
		t.toolMu.Unlock()
	}
}

// handleKeyReplacement answers the card: POST {name, approve}. Approving
// writes the held key through the same reach checks (the person's own
// credential, or the deployment's for an admin); either answer forgets it.
func (T *OrchestrateApp) handleKeyReplacement(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name    string `json:"name"`
		Approve bool   `json:"approve"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	secret, held := takeKeyReplacement(user, name)
	if !body.Approve {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !held {
		http.Error(w, "the replacement key is no longer held (it waits an hour): ask the assistant to fetch a new one", http.StatusGone)
		return
	}
	if err := Secure().ReplaceSecret(user, RequestIsAdmin(r), name, secret); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
