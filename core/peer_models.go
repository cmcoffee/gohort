// Lending inference: letting a peer run its turns on this instance's model.
//
// The other capabilities lend a resource with a small, stable wire format —
// a vector, a transcript, a rendered page. Inference is not like that. The
// chat wire format carries tool schemas, tool calls, images, sampling
// parameters, thinking budgets, stop sequences and a streaming protocol, and
// every one of those is a thing a caller depends on. Decoding all that into
// core.Message, calling the local LLM interface and re-encoding would silently
// drop whatever the interface does not model, and the caller would experience
// the loss as a model that has mysteriously got worse.
//
// So this endpoint does not interpret. It PROXIES: the request body goes
// upstream to the local inference server untouched, and the response comes back
// untouched, streaming bytes through as they arrive. Whatever the model can do,
// the peer gets.
//
// ONLY LOCAL BACKENDS ARE LENT. llama.cpp and ollama are lent; anthropic,
// openai, gemini and bedrock are refused. Two reasons, and either alone would
// be sufficient. First, relaying a hosted provider means spending the operator's
// API key on someone else's prompts with no per-caller accounting — the peer key
// becomes a billing hole. Second, it defeats the purpose: the point of borrowing
// a peer's model is reaching hardware the caller does not have, and a hosted
// endpoint is equally reachable from both ends.
//
// This is also what makes the borrowed model PRIVATE from the consumer's side.
// A peer serving llama.cpp is the operator's own GPU on the operator's own
// network, which is the premise "All LLMs are private" rests on — see
// llm_privacy.go.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/oddjob/core/prompts"
	"github.com/cmcoffee/snugforge/iotimeout"
)

// peerLendableProviders are the backends this instance will proxy.
//
// Deliberately a small allow-list rather than "anything not hosted": a new
// provider should have to be considered and added, because the failure mode of
// getting this wrong is lending out a metered API key.
var peerLendableProviders = map[string]bool{
	"llama.cpp": true,
	"vllm":      true,
	"ollama":    true,
}

// peerModelStreamIdle bounds the gap between bytes on a proxied stream.
//
// Not a cap on how long a generation may take — a long answer is not a stuck
// one, and a deadline here would cut a working response off mid-token. It
// bounds SILENCE: the upstream is on this machine, so minutes without a byte
// means it has wedged.
//
// It became load-bearing when the proxy started holding a scheduler slot for
// the life of the response. A wedged upstream used to cost one hung connection;
// now it would hold the slot too, and on stock llama.cpp at maxParallel=1 that
// is every local turn on the machine blocked behind a peer request that will
// never finish.
const peerModelStreamIdle = 5 * time.Minute

// peerModelMaxBody caps a proxied request. Generous because a vision turn
// carries base64 images, but finite — an unbounded body is a memory bomb
// wearing a valid peer key.
const peerModelMaxBody = 64 << 20

// PeerModelInfo advertises one model this instance will run for a peer.
//
// Tier is reported because it tells the operator on the far side what they are
// borrowing: a peer's worker and its lead may be different models on the same
// box, and picking the wrong one is the difference between a fast summarizer
// and a slow reasoner.
type PeerModelInfo struct {
	Tier     string `json:"tier"`     // "worker" | "lead"
	Model    string `json:"model"`    // the model id to name in a request
	Provider string `json:"provider"` // llama.cpp | ollama — what the far side should configure
	Path     string `json:"path"`     // where to point an OpenAI-shaped client
	// Prompts is where this instance's wording for its model is read. Absent
	// from an older build, which a borrowing instance reads as "no wording".
	Prompts string `json:"prompts,omitempty"`
}

// peerServableTier is one candidate tier plus the upstream to reach it.
type peerServableTier struct {
	Tier     string
	Provider string
	Model    string
	Endpoint string
	APIKey   string
}

// peerTierConfig reads the minimal provider fields for one tier straight from
// the config table.
//
// Only what the proxy needs. Sampling, thinking budgets and no-think signals
// are the CALLER's to choose — they arrive in the request body and are passed
// through untouched, so reading this instance's versions of them would be both
// useless and misleading.
func peerTierConfig(tier, table string) (peerServableTier, bool) {
	if AuthDB == nil {
		return peerServableTier{}, false
	}
	db := AuthDB()
	if db == nil {
		return peerServableTier{}, false
	}
	t := peerServableTier{Tier: tier}
	db.Get(table, "provider", &t.Provider)
	db.Get(table, "model", &t.Model)
	db.Get(table, "endpoint", &t.Endpoint)
	db.Get(table, "api_key", &t.APIKey)
	t.Provider = strings.ToLower(strings.TrimSpace(t.Provider))
	if !peerLendableProviders[t.Provider] {
		return peerServableTier{}, false
	}
	// Refuse to relay, as embeddings, search and browse all do. An instance
	// borrowing its own inference from a peer must not serve it onward: A→B→A
	// is a loop neither side can see, and it fails as a hang rather than an
	// error.
	if strings.Contains(t.Endpoint, "/api/peer/") {
		return peerServableTier{}, false
	}
	return t, true
}

// peerServableTiers lists the tiers this instance can lend, worker first.
//
// Worker first because it is the one a borrowing instance usually wants and
// because it is the tier a bare request with no model name resolves to.
// Deduplicated by model: when both tiers point at the same model, advertising
// it twice would present a choice that is not one.
func peerServableTiers() []peerServableTier {
	var out []peerServableTier
	seen := map[string]bool{}
	for _, t := range []struct{ tier, table string }{
		{"worker", LLMTable},
		{"lead", LeadLLMTable},
	} {
		cfg, ok := peerTierConfig(t.tier, t.table)
		if !ok {
			continue
		}
		if key := cfg.Provider + "\x00" + cfg.Model; !seen[key] {
			seen[key] = true
			out = append(out, cfg)
		}
	}
	return out
}

// peerModelsInfo renders the servable tiers for the manifest.
func peerModelsInfo(path string) []PeerModelInfo {
	var out []PeerModelInfo
	for _, t := range peerServableTiers() {
		out = append(out, PeerModelInfo{
			Tier: t.Tier, Model: t.Model, Provider: t.Provider, Path: path,
			Prompts: path + "/prompts",
		})
	}
	return out
}

// peerModelsServed reports whether anything is lendable right now. Used by the
// manifest so a key granted the capability on an instance with only a hosted
// provider configured learns that, rather than seeing an endpoint that refuses
// everything.
func peerModelsServed() bool { return len(peerServableTiers()) > 0 }

// resolvePeerTier picks which tier serves a request.
//
// An empty model name takes the first servable tier. A named model must MATCH,
// and a mismatch is refused rather than served by whatever happens to be
// loaded: silently answering from a different model returns text the caller
// attributes to a model it never ran, and nothing downstream can detect that.
// Same reasoning as the embeddings model check, where the consequence is
// vectors from the wrong space.
func resolvePeerTier(want string) (peerServableTier, error) {
	tiers := peerServableTiers()
	if len(tiers) == 0 {
		return peerServableTier{}, fmt.Errorf(
			"this instance has no local model to lend: inference sharing serves llama.cpp and ollama only, " +
				"because relaying a hosted provider would spend this operator's API key on a peer's prompts")
	}
	want = strings.TrimSpace(want)
	if want == "" {
		return tiers[0], nil
	}
	var names []string
	for _, t := range tiers {
		if t.Model == want {
			return t, nil
		}
		// The tier name is accepted as an alias, so a caller can ask for "lead"
		// without having to track which model that currently is.
		if strings.EqualFold(want, t.Tier) {
			return t, nil
		}
		names = append(names, t.Model+" ("+t.Tier+")")
	}
	return peerServableTier{}, fmt.Errorf(
		"this instance does not serve %q, it serves: %s. Refused rather than answered from a different model, "+
			"which would return text the caller attributes to a model that never ran",
		want, strings.Join(names, ", "))
}

// upstreamChatURL builds the upstream chat URL for a tier, matching the path
// handling the ordinary OpenAI client uses so a peer reaches exactly the
// endpoint a local caller would.
func upstreamChatURL(endpoint string) (string, error) {
	base := strings.TrimSpace(endpoint)
	if base == "" {
		return "", fmt.Errorf("no endpoint is configured for the model this instance would lend")
	}
	u, err := url.Parse(strings.TrimSuffix(base, "/"))
	if err != nil {
		return "", fmt.Errorf("the configured endpoint could not be parsed: %w", err)
	}
	path := u.Path
	if path == "" {
		path = "/v1"
	}
	u.Path = path + "/chat/completions"
	return u.String(), nil
}

// upstreamModelsURL builds the upstream /v1/models URL, matching LlamaCppModels.
func upstreamModelsURL(endpoint string) (string, error) {
	u, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(endpoint), "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("the configured endpoint could not be parsed")
	}
	path := strings.TrimSuffix(u.Path, "/")
	if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	u.Path = path + "/models"
	return u.String(), nil
}

// HandlePeerModels serves GET /api/peer/v1/models.
//
// Present because an OpenAI-shaped client's model picker asks for it: an
// operator configuring the far side chooses from a list, and without this the
// list is empty and they have to know the model id by heart. Answered from what
// this instance will actually LEND rather than proxied from upstream, so the
// picker cannot offer a model the chat endpoint would then refuse.
func HandlePeerModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		peerDeny(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapModels)
	if !ok {
		return
	}
	type entry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string  `json:"object"`
		Data   []entry `json:"data"`
	}{Object: "list", Data: []entry{}}
	for _, t := range peerServableTiers() {
		out.Data = append(out.Data, entry{ID: t.Model, Object: "model", OwnedBy: t.Tier})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
	touchPeerKey(k)
}

// HandlePeerChatCompletions serves POST /api/peer/v1/chat/completions by
// proxying to this instance's local inference server.
func HandlePeerChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		peerDeny(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapModels)
	if !ok {
		return
	}
	r, held, ok := peerHold(w, r, k, PeerCapModels)
	if !ok {
		return
	}
	defer held()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, peerModelMaxBody))
	if err != nil {
		peerDeny(w, http.StatusBadRequest, "could not read the request body: "+err.Error())
		return
	}
	// Peeked, not parsed. Only the two fields that decide ROUTING are read; the
	// body forwarded upstream is the original bytes, so every field this struct
	// does not name still reaches the model.
	var peek struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		peerDeny(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	tier, err := resolvePeerTier(peek.Model)
	if err != nil {
		peerDeny(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	target, err := upstreamChatURL(tier.Endpoint)
	if err != nil {
		peerDeny(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	// When the caller named a tier alias rather than a model id, upstream would
	// receive "lead" as the model. Rewrite to the resolved id — the one case
	// where the body is not passed through verbatim, and only because the alias
	// is a convenience this endpoint invented.
	if want := strings.TrimSpace(peek.Model); want != "" && want != tier.Model {
		if patched, err := patchJSONModel(body, tier.Model); err == nil {
			body = patched
		}
	}
	body = clampPeerModelBody(body)

	// Queue behind the SAME serializer local turns use, labelled as this peer.
	//
	// Without it a peer's request went straight at the inference server while
	// oddjob's own turns waited politely behind the mutex — and the serializer
	// is not a fairness nicety: stock llama.cpp is single-threaded and answers
	// 503 under concurrent load, so an overlapping peer turn could fail a local
	// one outright, with nothing on either machine explaining why.
	//
	// The label is the peer's, not "unknown", for the reason WithEmbedCaller
	// carries one: the scheduler round-robins between callers, so a peer
	// grinding through a batch interleaves with local work instead of arriving
	// as an anonymous flood that looks like local work and delays the
	// conversation a person is waiting on. It also makes the peer visible in
	// OllamaSchedulerStats, which already counts in-flight and queued PER
	// caller — so what is borrowing the GPU is a read of existing state rather
	// than a second counter that could disagree with it.
	ctx := WithCostAttribution(r.Context(), "peer:"+k.Label)
	release, err := acquirePeerModelSlot(ctx, tier.Provider, "peer:"+k.Label)
	if err != nil {
		// Queued and the caller went away, or the deadline passed while
		// waiting. Not an upstream failure, and saying so keeps a busy GPU from
		// reading as a broken one.
		peerDeny(w, http.StatusServiceUnavailable,
			"gave up waiting for a slot on this instance's model: "+err.Error())
		return
	}
	// Held until the RESPONSE BODY is done, not until headers arrive. A
	// streaming generation returns headers in milliseconds and runs for
	// minutes; releasing at the header would free the slot immediately and make
	// the serialization fictional for exactly the requests that need it most.
	defer release()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		peerDeny(w, http.StatusInternalServerError, "could not build the upstream request: "+err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(tier.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}

	// No client Timeout: a long generation is not a stuck one, and a deadline
	// here would cut a working answer off mid-token. The caller's context and
	// the idle watchdog below bound it instead.
	client := &http.Client{}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		peerDeny(w, http.StatusBadGateway, "the local model did not answer: "+err.Error())
		return
	}
	defer resp.Body.Close()
	// Fail on silence rather than on elapsed time — see peerModelStreamIdle.
	// The read loop below then returns an error instead of blocking forever,
	// which is what lets the deferred release actually run.
	body_ := iotimeout.NewReadCloser(resp.Body, peerModelStreamIdle)
	defer body_.Close()

	// Copy the upstream's own headers and status through, so an upstream error
	// reaches the caller as that error rather than as a peer-flavoured
	// paraphrase of it.
	for _, h := range []string{"Content-Type", "Cache-Control", "X-Accel-Buffering"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if peek.Stream {
		// SSE has to reach the caller as it is produced. Buffering it would turn
		// a streaming turn into a single delayed blob and lose the only property
		// the caller asked for.
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(resp.StatusCode)

	n, cerr := streamPeerBody(w, body_, peek.Stream)
	if cerr != nil {
		// The status and some bytes are already on the wire, so this cannot
		// become an error response. Logged instead — a truncated stream that
		// leaves no trace is the hardest kind of failure to chase.
		Debug("[peer] %q inference stream broke after %d bytes: %v", k.Label, n, cerr)
	}
	Debug("[peer] %q ran %s (%s tier) in %s (%d bytes, stream=%v)",
		k.Label, tier.Model, tier.Tier, time.Since(started).Round(time.Millisecond), n, peek.Stream)
	touchPeerKey(k)
}

// acquirePeerModelSlot queues this request behind the scheduler for the backend
// that will serve it, returning the release func.
//
// Which scheduler depends on the provider: llama.cpp and ollama each run their
// own, sized differently, and charging the wrong one would leave the real
// backend unprotected while throttling against a limit nothing enforces. An
// unstarted scheduler passes straight through, which is what a deployment with
// no configured cap has always done.
func acquirePeerModelSlot(ctx context.Context, provider, caller string) (func(), error) {
	switch provider {
	case "llama.cpp", "vllm":
		if err := AcquireLlamacppSlot(ctx, caller); err != nil {
			return nil, err
		}
		return func() { ReleaseLlamacppSlot(caller) }, nil
	case "ollama":
		if err := AcquireOllamaSlot(ctx, caller); err != nil {
			return nil, err
		}
		return func() { ReleaseOllamaSlot(caller) }, nil
	}
	// Unreachable while peerLendableProviders holds these two, and a no-op
	// rather than a refusal if it ever grows: failing to schedule is a
	// performance bug, refusing to serve is an outage.
	return func() {}, nil
}

// streamPeerBody copies the upstream response to the caller, flushing as it
// goes when streaming.
//
// io.Copy alone would be correct for a buffered response and wrong for SSE:
// without an explicit Flush the events sit in the writer's buffer and arrive in
// clumps, which is indistinguishable to the caller from a model that produces
// nothing and then everything.
func streamPeerBody(w http.ResponseWriter, src io.Reader, stream bool) (int64, error) {
	flusher, canFlush := w.(http.Flusher)
	if !stream || !canFlush {
		return io.Copy(w, src)
	}
	buf := make([]byte, 8<<10)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			written, werr := w.Write(buf[:n])
			total += int64(written)
			flusher.Flush()
			if werr != nil {
				return total, werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return total, nil
			}
			return total, err
		}
	}
}

// patchJSONModel replaces the top-level "model" field, leaving every other
// field byte-identical where the encoder allows.
func patchJSONModel(body []byte, model string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	enc, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	m["model"] = enc
	return json.Marshal(m)
}

// peerMaxTokens bounds one peer generation. A peer borrows this instance's
// model; an unbounded max_tokens holds the shared slot for as long as the
// model will run, and n>1 multiplies the work behind one request.
const peerMaxTokens = 16384

// clampPeerModelBody holds a peer's request inside those bounds, rewriting
// only when something is over. The body is otherwise passed through as sent.
func clampPeerModelBody(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	changed := false
	for _, f := range []string{"max_tokens", "max_completion_tokens", "num_predict"} {
		var n float64
		if raw, ok := m[f]; ok && json.Unmarshal(raw, &n) == nil && (n > peerMaxTokens || n < 0) {
			m[f] = json.RawMessage(strconv.Itoa(peerMaxTokens))
			changed = true
		}
	}
	if raw, ok := m["n"]; ok {
		var n float64
		if json.Unmarshal(raw, &n) == nil && n > 1 {
			m["n"] = json.RawMessage("1")
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// --- consuming half ----------------------------------------------------------

// ModelsURL is where a peer's OpenAI-shaped chat client should point. The
// client appends /chat/completions itself, exactly as it does for a local
// llama.cpp.
func (p RemotePeer) ModelsURL() string {
	return strings.TrimRight(p.BaseURL, "/") + "/api/peer/v1"
}

// ResolveModelProvider turns a submitted LLM tier config into the one to store.
//
// A peer selection becomes an ordinary llama.cpp-provider config pointed at
// that peer with the peer key as the bearer, so every existing code path —
// streaming, tool calling, no-think signals, the retry layer — keeps working
// with no knowledge that a peer is involved.
//
// The peer is looked up by NAME at save time and the endpoint is derived from
// it, but the KEY is copied. That copy is the known sharp edge: rotating a peer
// key leaves this config pointing at the old one, and the symptom is a 401 from
// a config that looks correct. Same shape as ResolveEmbeddingProvider, and the
// same fix applies to both — resolve at use time rather than at save time.
// peerModelAuth authorizes ONE request to a peer-backed model endpoint, reading
// the credential at the moment it is sent.
//
// The LLM client is built once and lives for the process; its credential does
// not. An access token is good for fifteen minutes and a re-key invalidates
// everything immediately, so a client that captured a string at construction
// starts answering 401 to every turn — which is what happened live, minutes
// after a peer was successfully re-paired. Baking a rotating credential into a
// long-lived client is the same snapshot bug that bit embeddings, search and
// transcription, in the one consumer that had no read-time path at all.
//
// Returns a function rather than a string for exactly that reason: snugforge's
// APIClient calls AuthFunc per request, so the token is resolved on the way out
// the door and a rotation between two turns costs nothing.
func peerModelAuth(name string) func(*http.Request) {
	return func(req *http.Request) {
		p, ok := lookupPeerCached(name)
		if !ok {
			// The peer was forgotten while a built client still points at it.
			// Sending nothing earns a clean 401 rather than a stale secret.
			return
		}
		// PeerCredentialNow, not PeerCredential: this is the moment of sending,
		// and the non-blocking form answers with the static key when there is
		// no token yet — a request the far side refuses by design. The first
		// message after a peer has been idle past its token's life was losing
		// the whole turn to that.
		if cred := strings.TrimSpace(PeerCredentialNow(req.Context(), p)); cred != "" {
			req.Header.Set("Authorization", "Bearer "+cred)
		}
	}
}

// peerNameFromProvider returns the peer a provider string names, or "".
func peerNameFromProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if !strings.HasPrefix(provider, peerProviderPrefix) {
		return ""
	}
	return strings.TrimPrefix(provider, peerProviderPrefix)
}

func ResolveModelProvider(cfg LLMProviderConfig, provider string) (LLMProviderConfig, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" || !strings.HasPrefix(provider, peerProviderPrefix) {
		return cfg, nil
	}
	p, ok := PeerFromProvider(provider)
	if !ok {
		return cfg, fmt.Errorf("no peer named %q is registered: add it under Peers first",
			strings.TrimPrefix(provider, peerProviderPrefix))
	}
	if !p.Offers(PeerCapModels) {
		return cfg, fmt.Errorf("peer %q does not offer inference (it offers: %s)",
			p.Name, strings.Join(p.Caps, ", "))
	}
	cfg.Provider = "llama.cpp"
	cfg.Endpoint = p.ModelsURL()
	cfg.APIKey = PeerCredential(p)
	return cfg, nil
}

// --- the model's wording -----------------------------------------------------
//
// A model is tuned where it runs. The tuning harness on the instance that
// serves a model writes prompt-block overrides fitted to that model, and an
// instance borrowing the model as its worker should send it the same words.
// So the serving side publishes its block overrides next to the model, and the
// borrowing side reads them and lets them govern (core/prompts/peer_layer.go).

// peerPromptsBody is what GET /api/peer/v1/prompts answers.
type peerPromptsBody struct {
	Model  string            `json:"model"`
	Blocks map[string]string `json:"blocks"`
}

// handlePeerPrompts serves GET /api/peer/v1/prompts: this instance's overrides
// of its registered prompt blocks, for a peer whose worker is this instance's
// model.
//
// Gated like /models, because the wording is only of use to a peer running
// the model, and refused the same way when there is no model to lend. Only
// registered blocks: the Style and global rules are this operator's own, and
// per-tier text is fitted to this instance's tiers, not to a peer's.
func handlePeerPrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		peerDeny(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapModels)
	if !ok {
		return
	}
	tiers := peerServableTiers()
	if len(tiers) == 0 {
		_, err := resolvePeerTier("")
		peerDeny(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	out := peerPromptsBody{Model: tiers[0].Model, Blocks: prompts.SharedBlockOverrides()}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
	touchPeerKey(k)
}

const (
	// peerPromptsRefresh is how often a borrowing instance re-reads its worker
	// peer's wording. Tuning moves in hours, so minutes is fresh enough.
	peerPromptsRefresh = 10 * time.Minute
	// peerPromptsCheck is how often it looks at whether the worker is still
	// that peer, so a saved worker config is followed within a minute without
	// a hook in the save path. The look is two config reads.
	peerPromptsCheck = time.Minute
	// peerPromptsTimeout bounds one fetch. The answer is a few kilobytes.
	peerPromptsTimeout = 30 * time.Second
)

// fetchPeerPrompts reads a peer's wording. A 404 is an older build that does
// not publish any, which is an empty answer rather than a failure: it is the
// truth about that peer, and keeping some earlier copy would not be.
func fetchPeerPrompts(ctx context.Context, p RemotePeer) (peerPromptsBody, error) {
	ctx, cancel := context.WithTimeout(ctx, peerPromptsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.ModelsURL()+"/prompts", nil)
	if err != nil {
		return peerPromptsBody{}, err
	}
	resp, err := PeerClientFor(p, peerPromptsTimeout).Do(req)
	if err != nil {
		return peerPromptsBody{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return peerPromptsBody{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		msg := peerErrorBody(resp.Body)
		if msg == "" {
			msg = resp.Status
		}
		return peerPromptsBody{}, fmt.Errorf("answered %d: %s", resp.StatusCode, msg)
	}
	var out peerPromptsBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return peerPromptsBody{}, fmt.Errorf("unreadable answer: %w", err)
	}
	return out, nil
}

// peerPromptSync keeps the peer layer in step with the worker's peer.
type peerPromptSync struct {
	mu      sync.Mutex
	peer    string    // the peer the last look was for, "" when not a peer
	last    time.Time // when that peer's wording was last asked for
	failing bool      // in a failure streak that has been logged
}

var (
	peerPrompts     peerPromptSync
	peerPromptsOnce sync.Once
)

// workerPeerName is the peer the worker tier's provider names, or "". Read
// from the stored config the way NewLLMFromConfig sees it, before resolution
// rewrites "peer:<name>" to llama.cpp.
func workerPeerName() string {
	provider, _ := tierProviderEndpoint(LLMTable)
	return peerNameFromProvider(provider)
}

// run looks once now and then every peerPromptsCheck, for the process's life.
// Not on any request's context: the layer outlives every request.
func (s *peerPromptSync) run() {
	ctx := context.Background()
	s.tick(ctx, time.Now())
	t := time.NewTicker(peerPromptsCheck)
	defer t.Stop()
	for now := range t.C {
		s.tick(ctx, now)
	}
}

// tick fetches when the worker's peer changed or its wording is due a
// refresh, and clears the layer when the worker is no longer a peer's model.
func (s *peerPromptSync) tick(ctx context.Context, now time.Time) {
	name := workerPeerName()
	s.mu.Lock()
	changed := name != s.peer
	due := changed || now.Sub(s.last) >= peerPromptsRefresh
	if due {
		s.peer, s.last = name, now
	}
	if changed {
		s.failing = false
	}
	s.mu.Unlock()
	if !due {
		return
	}
	if name == "" {
		if prompts.ClearPeerPromptLayer() {
			Log("[peer] the worker is no longer a peer's model, so this instance's own prompt wording governs again")
		}
		return
	}
	s.sync(ctx, name)
}

// sync fetches one peer's wording and puts it in force. On failure the last
// copy stays, unless it came from a different peer: wording fitted to a model
// this instance no longer runs is not a fallback worth keeping.
func (s *peerPromptSync) sync(ctx context.Context, name string) {
	var body peerPromptsBody
	p, ok := GetRemotePeer(name)
	err := fmt.Errorf("no peer named %q is registered", name)
	if ok {
		body, err = fetchPeerPrompts(ctx, p)
	}
	if err != nil {
		if prompts.PeerPromptLayer().Source != name {
			prompts.ClearPeerPromptLayer()
		}
		s.mu.Lock()
		first := !s.failing
		s.failing = true
		s.mu.Unlock()
		if first {
			Log("[peer] prompts from %s: %v. Keeping the last wording it sent until it answers.", name, err)
		}
		return
	}
	s.mu.Lock()
	recovered := s.failing
	s.failing = false
	s.mu.Unlock()
	res := prompts.SetPeerPromptLayer(name, body.Model, time.Now(), body.Blocks)
	if res.Changed || recovered {
		Log("[peer] prompts from %s (%s): %d blocks%s", name, body.Model, res.Kept, peerPromptsSkipped(res))
	}
}

// peerPromptsSkipped says what a refresh left out, or "" when nothing.
func peerPromptsSkipped(res prompts.PeerLayerResult) string {
	var parts []string
	if n := len(res.Unknown); n > 0 {
		parts = append(parts, fmt.Sprintf("%d not registered here", n))
	}
	if n := len(res.Broken); n > 0 {
		parts = append(parts, fmt.Sprintf("%d naming a placeholder the block cannot fill", n))
	}
	if len(parts) == 0 {
		return ""
	}
	return ", skipped " + strings.Join(parts, " and ")
}

// Started by the scheduler, which runs only once the database and the peer
// records are loaded. The reconciler fires every 30 minutes; the Once keeps it
// to one loop.
func init() {
	RegisterReconciler("peer_prompts", func(context.Context) error {
		peerPromptsOnce.Do(func() { go peerPrompts.run() })
		return nil
	})
}
