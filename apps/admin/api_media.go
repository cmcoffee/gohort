package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// registerMediaRoutes wires the media API under the admin sub-mux.
func (a *AdminApp) registerMediaRoutes(sub *http.ServeMux) {
	// Embeddings config — GET returns current settings, POST persists +
	// reinstalls the live config so the next ingestion/search call picks
	// up the new endpoint/model without a restart.
	sub.HandleFunc("/api/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req EmbeddingConfig
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			// A peer selection carries no endpoint of its own — the manual
			// fields are hidden while one is picked. Resolve it into a complete,
			// ordinary config here so everything downstream (Embed,
			// EmbedVersion, the vector store) stays peer-unaware.
			resolved, err := ResolveEmbeddingProvider(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			req = resolved
			if a.db != nil {
				a.db.Set(EmbeddingTable, "current", req)
			}
			SetEmbeddingConfig(req)
			Log("[admin] user %q updated embeddings config (enabled=%v provider=%q endpoint=%q model=%q)",
				AuthCurrentUser(r), req.Enabled, req.Provider, req.Endpoint, req.Model)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var cfg EmbeddingConfig
		if a.db != nil {
			a.db.Get(EmbeddingTable, "current", &cfg)
		}
		// Every config stored before peers existed has a blank Provider. Report
		// it as "local" so the dropdown preselects the right option instead of
		// opening on nothing, and so a ShowWhen testing provider:local matches.
		if strings.TrimSpace(cfg.Provider) == "" {
			cfg.Provider = EmbeddingProviderLocal
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	})

	// Embeddings connectivity test — POSTs current form values (NOT the
	// saved DB record), runs a one-shot Embed() against them, returns
	// {ok, message|error} for inline display in the admin FormPanel.
	sub.HandleFunc("/api/embeddings/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req EmbeddingConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeTestResult(w, false, "", "invalid request body")
			return
		}
		if !req.Enabled {
			writeTestResult(w, false, "", "embeddings are disabled — flip the toggle on first")
			return
		}
		// Resolve a peer selection the same way the SAVE path does. Without
		// this the test ran the form's literal fields — and with a peer picked
		// those are hidden and empty, so testing a perfectly good peer failed
		// with "endpoint is required" against a form showing no endpoint field
		// to fill in. A test button that cannot test the thing the form is
		// currently configured for is worse than no test button.
		resolved, err := ResolveEmbeddingProvider(req)
		if err != nil {
			writeTestResult(w, false, "", err.Error())
			return
		}
		req = resolved
		if req.Endpoint == "" {
			writeTestResult(w, false, "", "endpoint is required")
			return
		}
		// Temporarily swap in the form's working config for this one call
		// without persisting. Restore on exit so a failed test doesn't
		// poison live state.
		prev := GetEmbeddingConfig()
		SetEmbeddingConfig(req)
		defer SetEmbeddingConfig(prev)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		vec, err := Embed(ctx, "hello from gohort admin connectivity test")
		if err != nil {
			writeTestResult(w, false, "", err.Error())
			return
		}
		modelLabel := req.Model
		if modelLabel == "" {
			modelLabel = "server default"
		}
		// Name WHERE it embedded. The test now has two possible meanings, and a
		// bare "OK" would not distinguish a working peer from a local embedder
		// that answered because the peer selection never took effect.
		where := "this instance"
		if p, ok := PeerFromProvider(req.Provider); ok {
			where = "peer " + p.Name
			if p.Instance != "" {
				where += " (" + p.Instance + ")"
			}
		}
		writeTestResult(w, true, fmt.Sprintf("OK — %d-dim embedding from %s via %s", len(vec), modelLabel, where), "")
	})

	// /api/embeddings/models — probe the saved embedding endpoint for
	// available models. Returns chip-shaped JSON (id/name/value) so
	// FormField.ChipsSource can render a click-to-fill row above the
	// model input. Tries OpenAI-style /models first (works for OpenAI,
	// vLLM, llama.cpp, hf-tei), falls back to Ollama's /api/tags by
	// transforming the endpoint base. Empty array on either reach
	// failure — the field stays manually editable, no UI error.
	sub.HandleFunc("/api/embeddings/models", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		var cfg EmbeddingConfig
		if a.db != nil {
			a.db.Get(EmbeddingTable, "current", &cfg)
		}
		w.Header().Set("Content-Type", "application/json")
		if cfg.Endpoint == "" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		// Try OpenAI-compat /models first.
		base := strings.TrimRight(cfg.Endpoint, "/")
		names := probeEmbeddingModels(ctx, base+"/models", cfg.APIKey, "openai")
		if len(names) == 0 {
			// Fall back to Ollama /tags. Strip /v1 if present so we
			// don't end up with /v1/tags (Ollama only serves
			// /api/tags, never under /v1). The base already includes
			// /api for canonical Ollama configs.
			tagsBase := strings.TrimSuffix(base, "/v1")
			names = probeEmbeddingModels(ctx, tagsBase+"/tags", cfg.APIKey, "ollama")
		}
		out := make([]map[string]string, 0, len(names))
		for _, n := range names {
			out = append(out, map[string]string{"id": n, "name": n, "value": n})
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	// Audio transcription (STT) — GET/POST. POST persists + reinstalls
	// the live TranscribeConfig so the next Transcribe() call picks up
	// the new endpoint/model/key without a restart.
	sub.HandleFunc("/api/transcribe", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req TranscribeConfig
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			// A peer selection carries no endpoint of its own — the manual
			// fields are hidden while one is picked. Resolve it into a
			// complete, ordinary config here so everything downstream
			// (Transcribe, the transcribe tool, inbound voice notes) stays
			// peer-unaware. Same shape as the embeddings save above.
			resolved, err := ResolveTranscribeProvider(req, req.Provider)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			req = resolved
			if a.db != nil {
				a.db.Set(TranscribeTable, "current", req)
			}
			SetTranscribeConfig(req)
			Log("[admin] user %q updated transcribe config (enabled=%v endpoint=%q model=%q)",
				AuthCurrentUser(r), req.Enabled, req.Endpoint, req.Model)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var cfg TranscribeConfig
		if a.db != nil {
			a.db.Get(TranscribeTable, "current", &cfg)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	})

	// Image generation — provider + api_key live in per-key rows under
	// ImageTable. Shape mirrors the legacy --setup wiring so existing
	// installs read/write the same kvlite keys.
	sub.HandleFunc("/api/image-gen", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				Provider string `json:"provider"`
				APIKey   string `json:"api_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				a.db.Set(ImageTable, "provider", req.Provider)
				a.db.Set(ImageTable, "api_key", req.APIKey)
			}
			Log("[admin] user %q updated image-gen config (provider=%q)",
				AuthCurrentUser(r), req.Provider)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var provider, key string
		if a.db != nil {
			a.db.Get(ImageTable, "provider", &provider)
			a.db.Get(ImageTable, "api_key", &key)
		}
		if provider == "" {
			provider = "gemini"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"provider": provider, "api_key": key})
	})

	// STT connectivity test — GET {endpoint}/models with auth header so
	// the operator can confirm reachability + credentials without needing
	// a sample audio file. Both whisper.cpp and the real OpenAI API
	// expose /models on the OpenAI-compatible base; a 2xx means the
	// endpoint is reachable and (if a key was provided) accepts it.
	sub.HandleFunc("/api/transcribe/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req TranscribeConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeTestResult(w, false, "", "invalid request body")
			return
		}
		if !req.Enabled {
			writeTestResult(w, false, "", "transcription is disabled — flip the toggle on first")
			return
		}
		// Resolve a peer selection the same way the SAVE path does. Without
		// this the test ran the form's literal fields — and with a peer picked
		// those are hidden and empty, so testing a perfectly good peer failed
		// with "endpoint is required" against a form showing no endpoint field
		// to fill in. Exactly the bug the embeddings test button already had.
		resolved, rerr := ResolveTranscribeProvider(req, req.Provider)
		if rerr != nil {
			writeTestResult(w, false, "", rerr.Error())
			return
		}
		req = resolved
		if req.Endpoint == "" {
			writeTestResult(w, false, "", "endpoint is required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		// A peer is probed through its MANIFEST rather than the GET probe
		// below. The peer base serves exactly one path — /audio/transcriptions
		// — so /models would 404 and the root fallback would land on the far
		// side's web UI and report a cheerful success for a link that cannot
		// transcribe. The manifest answers the questions that actually matter
		// (is it reachable, is the key good, is transcribe granted, does that
		// instance still have STT configured) and says which one failed.
		if p, isPeer := PeerFromProvider(req.Provider); isPeer {
			ok, msg, errMsg := peerTranscribeTestResult(ctx, p)
			writeTestResult(w, ok, msg, errMsg)
			return
		}
		// probeURL: GET with optional bearer header. Returns the response status
		// or any transport error. Closing the body inline so callers don't have to.
		probeURL := func(url string) (int, error) {
			httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				return 0, err
			}
			if req.APIKey != "" {
				httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
			}
			resp, err := (&http.Client{}).Do(httpReq)
			if err != nil {
				return 0, err
			}
			defer resp.Body.Close()
			return resp.StatusCode, nil
		}
		// Try /models first (OpenAI-compatible servers expose this and a
		// 200 also validates the bearer key). Fall back to the endpoint
		// root for servers like whisper.cpp that only expose the
		// transcription path and serve an HTML index at /.
		base := strings.TrimRight(req.Endpoint, "/")
		modelsURL := base + "/models"
		status, err := probeURL(modelsURL)
		if err != nil {
			writeTestResult(w, false, "", "reach failed: "+err.Error())
			return
		}
		switch {
		case status >= 200 && status < 300:
			writeTestResult(w, true, fmt.Sprintf("Endpoint reachable + /models OK (HTTP %d)", status), "")
			return
		case status == 401 || status == 403:
			writeTestResult(w, false, "", fmt.Sprintf("HTTP %d — endpoint reached but rejected the API key", status))
			return
		}
		// /models 404/405 → fall back to a plain GET on the endpoint root.
		// Strip any trailing /v1 (or /api) so we hit the actual host root.
		rootBase := base
		for _, suffix := range []string{"/v1", "/api"} {
			if strings.HasSuffix(rootBase, suffix) {
				rootBase = rootBase[:len(rootBase)-len(suffix)]
				break
			}
		}
		rootStatus, err := probeURL(rootBase + "/")
		if err != nil {
			writeTestResult(w, false, "", fmt.Sprintf("HTTP %d at %s, and root probe failed: %s", status, modelsURL, err.Error()))
			return
		}
		if rootStatus >= 200 && rootStatus < 500 {
			writeTestResult(w, true, fmt.Sprintf("Endpoint reachable (HTTP %d at root; %d at /models — server doesn't expose /models, fine for whisper.cpp)", rootStatus, status), "")
			return
		}
		writeTestResult(w, false, "", fmt.Sprintf("HTTP %d at root, HTTP %d at /models", rootStatus, status))
	})

	// Image gen connectivity test — same shape as STT but per-provider
	// (each has its own models URL convention). Validates the API key
	// is recognized; doesn't actually generate an image (which would
	// cost money on every test click).
	sub.HandleFunc("/api/image-gen/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Provider string `json:"provider"`
			APIKey   string `json:"api_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeTestResult(w, false, "", "invalid request body")
			return
		}
		if req.Provider == "" || req.Provider == "none" {
			writeTestResult(w, false, "", "pick a provider first")
			return
		}
		// Connector-backed provider (rest_image: ComfyUI / A1111 / custom): it
		// carries its own SecureAPI credential, so there's no API key to probe
		// here. Report that it's live rather than falling into the built-in
		// gemini/openai key test (which would reject the name as "unknown").
		if ImageBackendRegistered(req.Provider) {
			writeTestResult(w, true, "Connector backend “"+req.Provider+"” is approved and active. It uses its own credential — generate an image to verify it end to end.", "")
			return
		}
		// Fall back to the matching LLM provider's key when blank (same
		// rule the GenerateImage runtime uses).
		key := req.APIKey
		if key == "" && a.db != nil {
			switch req.Provider {
			case "gemini":
				a.db.Get(LLMTable, "api_key", &key) // reuse if Gemini is also worker provider
			case "openai":
				a.db.Get(LLMTable, "api_key", &key)
			}
		}
		if key == "" {
			writeTestResult(w, false, "", "no API key — set one here, or set the matching LLM provider's key")
			return
		}
		var url string
		var authHeader, authPrefix string
		switch req.Provider {
		case "openai":
			url = "https://api.openai.com/v1/models"
			authHeader, authPrefix = "Authorization", "Bearer "
		case "gemini":
			// Gemini takes the key as ?key= rather than a header.
			url = "https://generativelanguage.googleapis.com/v1beta/models?key=" + key
		default:
			writeTestResult(w, false, "", "unknown provider: "+req.Provider)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			writeTestResult(w, false, "", err.Error())
			return
		}
		if authHeader != "" {
			httpReq.Header.Set(authHeader, authPrefix+key)
		}
		resp, err := (&http.Client{}).Do(httpReq)
		if err != nil {
			writeTestResult(w, false, "", "reach failed: "+err.Error())
			return
		}
		defer resp.Body.Close()
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			writeTestResult(w, true, fmt.Sprintf("%s reachable + key accepted (HTTP %d)", req.Provider, resp.StatusCode), "")
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			writeTestResult(w, false, "", fmt.Sprintf("HTTP %d — %s rejected the API key", resp.StatusCode, req.Provider))
		default:
			writeTestResult(w, false, "", fmt.Sprintf("HTTP %d from %s", resp.StatusCode, req.Provider))
		}
	})

}

// probeEmbeddingModels fetches and parses a model list from an
// embedding endpoint's discovery URL. shape controls how to interpret
// the response body:
//   - "openai":  {"data": [{"id": "..."}, ...]}  (OpenAI, vLLM, llama.cpp /v1/models, hf-tei /models)
//   - "ollama":  {"models": [{"name": "..."}, ...]}  (Ollama /api/tags)
//
// Returns an empty slice on any error so the caller can quietly fall
// back to the next probe form without surfacing an error to the UI.
func probeEmbeddingModels(ctx context.Context, url, apiKey, shape string) []string {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	switch shape {
	case "openai":
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil
		}
		out := make([]string, 0, len(body.Data))
		for _, m := range body.Data {
			if m.ID != "" {
				out = append(out, m.ID)
			}
		}
		return out
	case "ollama":
		var body struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil
		}
		out := make([]string, 0, len(body.Models))
		for _, m := range body.Models {
			if m.Name != "" {
				out = append(out, m.Name)
			}
		}
		return out
	}
	return nil
}
