package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What Admin › Sharing gets back, through the real routes and the gate in
// front of them: minting, listing, re-granting, scoping, switching, re-pairing
// and removing the keys this instance lends with; and adding, refreshing,
// re-keying and forgetting the peers it borrows from, this instance standing in
// for the peer. Each as an administrator, as a user who is not one, by the
// wrong method and with a broken or missing body, and then what the activity
// log says happened. A key is shown in the reply that mints it and in no other.
// Compared with testdata/peers_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestPeersRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot, prevCfg := AuthDB, RootDB, GetEmbeddingConfig()
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot; SetEmbeddingConfig(prevCfg) })

	// This instance is the peer too: an embedder for its manifest to offer,
	// and the two endpoints a consumer pairs through.
	embedder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer embedder.Close()
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Endpoint: embedder.URL, Model: "nomic-embed-text"})
	peerMux := http.NewServeMux()
	peerMux.HandleFunc("/api/peer/manifest", HandlePeerManifest)
	peerMux.HandleFunc("/api/peer/v1/token", HandlePeerToken)
	peer := httptest.NewServer(peerMux)
	defer peer.Close()

	ct := &contract{t: t}
	// The peer's manifest names this machine; the record names a stand-in.
	host, _ := os.Hostname()
	masks := []string{peer.URL, "<peer>", host, "<this host>"}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) string {
		ct.rewrite = strings.NewReplacer(masks...)
		return ct.run(mux, adb, user, method, path, body)
	}
	// mint issues a key, remembers its id under a name the record shows, and
	// holds its secret to every later reply.
	mint := func(as, body string) (id, key string) {
		var got struct{ ID, Key string }
		json.Unmarshal([]byte(do("root", "POST", "/api/peer-keys/mint", body)), &got)
		if got.ID == "" || got.Key == "" {
			t.Fatalf("mint %s: no id or key", body)
		}
		masks = append(masks, got.ID, as, got.Key, as+".key")
		ct.secrets = append(ct.secrets, got.Key)
		return got.ID, got.Key
	}

	for _, p := range []string{"/api/peer-keys", "/api/peer-keys/mint", "/api/peers", "/api/peers/add"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
	}
	do("root", "POST", "/api/peer-keys", `{"label":"x"}`)
	do("root", "POST", "/api/peers", `{"name":"x"}`)
	do("alice", "POST", "/api/peer-keys/mint", `{"label":"mallory","caps":["embeddings"]}`)

	k1, key1 := mint("<k1>", `{"label":"consumer","caps":["embeddings"],"rate_per_min":30}`)
	k2, _ := mint("<k2>", `{"label":"listed","caps":"embeddings, transcribe"}`)
	k3, key3 := mint("<k3>", `{"label":"quoted","caps":"[\"embeddings\"]"}`)
	do("root", "POST", "/api/peer-keys/mint", `{"label":"","caps":["embeddings"]}`)
	do("root", "POST", "/api/peer-keys/mint", `{"label":"odd","caps":["nonsense"]}`)
	do("root", "POST", "/api/peer-keys/mint", `{"label":`)
	do("root", "POST", "/api/peer-keys/mint", ``)
	do("root", "PUT", "/api/peer-keys/mint", `{}`)
	do("root", "GET", "/api/peer-keys", "")

	for _, id := range []string{k1, "nope"} {
		do("alice", "GET", "/api/peer-keys/"+id, "")
		do("root", "GET", "/api/peer-keys/"+id, "")
	}
	do("root", "GET", "/api/peer-keys/", "")
	do("alice", "PUT", "/api/peer-keys/"+k2, `{"enabled":false}`)
	do("root", "PUT", "/api/peer-keys/"+k2, `{"enabled":false}`)
	do("root", "PATCH", "/api/peer-keys/"+k2, `{"enabled":true}`)
	do("root", "POST", "/api/peer-keys/"+k2, `{"caps":["embeddings","transcribe"]}`)
	do("root", "PUT", "/api/peer-keys/"+k2, `{"caps":["nonsense"]}`)
	do("root", "PUT", "/api/peer-keys/"+k2, `{"owner":"alice","appliances":[]}`)
	do("root", "PUT", "/api/peer-keys/"+k2, `{"appliances":["alice:a1","bob:b1"]}`)
	do("root", "PUT", "/api/peer-keys/"+k2, `{}`)
	do("root", "PUT", "/api/peer-keys/"+k2, `{"enabled":`)
	do("root", "PUT", "/api/peer-keys/"+k2, ``)
	do("root", "PUT", "/api/peer-keys/nope", `{"enabled":true}`)
	do("root", "PUT", "/api/peer-keys/nope", `{"caps":["embeddings"]}`)
	do("root", "GET", "/api/peer-keys/"+k2, "")

	do("root", "GET", "/api/peer-keys/"+k3+"/repair", "")
	do("root", "POST", "/api/peer-keys/"+k3+"/bogus", "")
	do("root", "POST", "/api/peer-keys/nope/repair", "")
	do("alice", "POST", "/api/peer-keys/"+k3+"/repair", "")
	var re struct{ Key string }
	json.Unmarshal([]byte(do("root", "POST", "/api/peer-keys/"+k3+"/repair", "")), &re)
	if re.Key == "" || re.Key == key3 {
		t.Fatalf("re-pair gave no new key")
	}
	ct.secrets = append(ct.secrets, re.Key)
	masks = append(masks, re.Key, "<k3>.repaired-key")

	do("alice", "POST", "/api/peers/add", `{"name":"gpu-box","base_url":"`+peer.URL+`","key":"`+key1+`"}`)
	do("root", "POST", "/api/peers/add", `{"name":"gpu-box","base_url":"`+peer.URL+`","key":"0000000000000000000000000000000000000000000000000000000000000000"}`)
	do("root", "POST", "/api/peers/add", `{"name":"gpu-box","base_url":"http://127.0.0.1:1","key":"`+key1+`"}`)
	do("root", "POST", "/api/peers/add", `{"name":"gpu-box","base_url":"`+peer.URL+`","key":"`+key1+`"}`)
	do("root", "POST", "/api/peers/add", `{"name":`)
	do("root", "POST", "/api/peers/add", ``)
	do("root", "PUT", "/api/peers/add", `{}`)
	do("root", "GET", "/api/peers", "")
	do("root", "GET", "/api/peer-keys", "")

	do("alice", "POST", "/api/peers/gpu-box/refresh", "")
	do("root", "POST", "/api/peers/gpu-box/refresh", "")
	do("root", "POST", "/api/peers/nope/refresh", "")
	do("root", "GET", "/api/peers/gpu-box/refresh", "")
	do("root", "GET", "/api/peers/gpu-box/key", "")
	do("alice", "GET", "/api/peers/gpu-box/key", "")
	do("root", "POST", "/api/peers/gpu-box/key", `{"key":"`+re.Key+`"}`)
	do("root", "POST", "/api/peers/gpu-box/key", `{"key":`)
	do("root", "POST", "/api/peers/nope/key", `{"key":"`+re.Key+`"}`)
	do("root", "GET", "/api/peers/gpu-box", "")
	do("root", "PUT", "/api/peers/gpu-box", `{}`)
	do("root", "GET", "/api/peers/", "")
	do("root", "GET", "/api/peers", "")

	do("alice", "DELETE", "/api/peers/gpu-box", "")
	do("root", "DELETE", "/api/peers/gpu-box", "")
	do("root", "DELETE", "/api/peers/gpu-box", "")
	do("root", "DELETE", "/api/peers/", "")
	do("alice", "DELETE", "/api/peer-keys/"+k1, "")
	do("root", "DELETE", "/api/peer-keys/"+k1, "")
	do("root", "DELETE", "/api/peer-keys/"+k1, "")
	do("root", "DELETE", "/api/peer-keys/", "")
	do("root", "GET", "/api/peer-keys", "")
	do("root", "GET", "/api/peers", "")
	ct.activity(mux, adb, "root")

	ct.check(t, "testdata/peers_contract.golden")
}
