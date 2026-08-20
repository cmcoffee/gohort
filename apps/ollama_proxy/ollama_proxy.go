// Package ollama_proxy exposes a fair-queued, standalone plain-HTTP server
// that mimics the Ollama API. External clients (Claude Code, open-webui, etc.)
// point their Ollama base URL to http://<gohort-host>:<port> and interact with
// the virtual model "gohort". When the active provider is Ollama the proxy
// forwards requests directly. When the active provider is llama.cpp the proxy
// translates between Ollama's API format and OpenAI's /v1/chat/completions format.
package ollama_proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const virtualModel = "gohort" // model name exposed to proxy clients

// StartOllamaServer starts a standalone plain-HTTP Ollama-compatible server on
// the given port. If port <= 0 or neither Ollama nor llama.cpp is configured,
// this is a no-op. The server shuts down when AppContext is cancelled.
func StartOllamaServer(port int) {
	if port <= 0 {
		return
	}

	// Determine which backend is active.
	hasOllama := OllamaBackendFunc != nil
	hasLlamaCpp := LlamaCppBackendFunc != nil
	if hasOllama {
		b, _, _ := OllamaBackendFunc()
		hasOllama = b != ""
	}
	if hasLlamaCpp {
		b, _ := LlamaCppBackendFunc()
		hasLlamaCpp = b != ""
	}
	if !hasOllama && !hasLlamaCpp {
		return
	}

	p := &ollamaProxy{
		client: &http.Client{
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				ResponseHeaderTimeout: 5 * time.Minute,
			},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		if !p.allow(w, r) {
			return
		}
		p.handleTags(w)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			// The liveness probe every Ollama client makes before it will
			// talk to an endpoint. Left open on purpose: it discloses nothing
			// beyond "something is listening", which the TCP handshake
			// already said, and gating it means a client cannot even discover
			// it needs a key.
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("Ollama is running"))
			return
		}
		if !p.allow(w, r) {
			return
		}
		p.handle(w, r)
	})

	// Bind deliberately. This used to be ":port" — every interface — on a
	// server with no authentication of any kind, which on a host with a public
	// address is an open inference endpoint. Loopback is the default now, and
	// reaching past it is a choice the operator makes and can see.
	host := strings.TrimSpace(defaultBind)
	if OllamaProxyBindFunc != nil {
		if b := strings.TrimSpace(OllamaProxyBindFunc()); b != "" {
			host = b
		}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-AppContext().Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	go func() {
		// Say what is actually exposed, not just that it started. An operator
		// reading "Ollama Proxy: http://localhost:11435" while the thing is
		// bound to every interface has been told the reassuring half.
		if isLoopbackBind(host) {
			Log("Ollama Proxy: http://%s  (model: gohort, loopback only)\n", addr)
		} else {
			Warn("Ollama Proxy listening on %s — reachable from the network, and every request needs an API key (X-API-Key or Authorization: Bearer). Bind it to 127.0.0.1 in Admin if you did not mean to expose it.", addr)
		}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			Warn("ollama proxy: %v", err)
		}
	}()
}

// defaultBind is where the proxy listens when the operator has not said.
// Loopback, because an inference endpoint with no authentication is not a
// thing to expose by omission.
const defaultBind = "127.0.0.1"

// isLoopbackBind reports whether a bind address reaches only this machine.
// The empty string means Go's "all interfaces", which it does not.
func isLoopbackBind(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

type ollamaProxy struct {
	client *http.Client
}

// allow is the proxy's entire access control, and it has to be its own: this
// is a separate http.Server on its own port, so AuthMiddleware, the admin IP
// allowlist and the session cookie never see these requests.
//
// The rule is the one the rest of gohort already uses. A request that genuinely
// arrives on loopback is inside the trust boundary a local tool assumes, and
// Ollama clients speak no authentication at all — requiring a key there would
// break the ordinary case for no gain, since anyone on the box can reach the
// model server directly anyway. Anything from further away presents a
// credential: a personal access token, a desktop key, a bridge key, whatever
// the registered validators recognize.
//
// Checked per REQUEST as well as at bind, so the two cannot disagree. A bind
// widened without the operator thinking about it still needs keys, and a
// loopback bind reached through some forwarding arrangement still gets asked.
func (p *ollamaProxy) allow(w http.ResponseWriter, r *http.Request) bool {
	if IsLoopbackRequest(r) {
		return true
	}
	if user := APIKeyUser(r); user != "" {
		return true
	}
	Warn("[ollama-proxy] refused unauthenticated request from %s %s %s", directPeer(r), r.Method, r.URL.Path)
	w.Header().Set("WWW-Authenticate", `Bearer realm="gohort"`)
	http.Error(w, "this endpoint needs a gohort personal access token in X-API-Key or Authorization: Bearer (create one on your Account page)", http.StatusUnauthorized)
	return false
}

func (p *ollamaProxy) handle(w http.ResponseWriter, r *http.Request) {
	Debug("[ollama-proxy] %s %s from %s", r.Method, r.URL.Path, callerIP(r))
	if OllamaProxyEnabledFunc == nil || !OllamaProxyEnabledFunc() {
		Debug("[ollama-proxy] proxy disabled — returning 503")
		http.Error(w, "ollama proxy disabled", http.StatusServiceUnavailable)
		return
	}

	// Route to the active backend.
	if LlamaCppBackendFunc != nil {
		if ep, model := LlamaCppBackendFunc(); ep != "" {
			p.handleLlamaCpp(w, r, ep, model)
			return
		}
	}
	if OllamaBackendFunc != nil {
		if backend, model, numCtx := OllamaBackendFunc(); backend != "" {
			p.handleOllama(w, r, backend, model, numCtx)
			return
		}
	}

	http.Error(w, "no backend configured", http.StatusServiceUnavailable)
}

// --- Ollama pass-through ---

func (p *ollamaProxy) handleOllama(w http.ResponseWriter, r *http.Request, backend, model string, numCtx int) {
	var (
		bodyBytes []byte
		tag       string
	)
	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		tag = virtualModelTag(bodyBytes)
		bodyBytes = rewriteModel(bodyBytes, virtualModel, model)
		bodyBytes = injectNumCtx(bodyBytes, numCtx)
		if tag == "no-think" {
			bodyBytes = injectOptions(bodyBytes, "think", false)
		}
	}

	callerID := callerIP(r)
	if err := AcquireOllamaSlot(r.Context(), callerID); err != nil && err != ErrOllamaSchedulerDisabled {
		http.Error(w, "queue: "+err.Error(), http.StatusServiceUnavailable)
		return
	} else if err == nil {
		defer ReleaseOllamaSlot(callerID)
	}

	targetURL := backendRoot(backend) + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	req.Header.Set("Content-Type", ct)

	resp, err := p.client.Do(req)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	flusher, canFlush := w.(http.Flusher)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := rewriteModel(scanner.Bytes(), model, virtualModel)
		w.Write(line)
		w.Write([]byte("\n"))
		if canFlush {
			flusher.Flush()
		}
	}
}

// --- llama.cpp translation ---

// ollamaChatRequest is the Ollama /api/chat request shape.
type ollamaChatRequest struct {
	Model    string            `json:"model"`
	Messages []json.RawMessage `json:"messages"`
	Stream   bool              `json:"stream"`
	Think    *bool             `json:"think,omitempty"`
	Options  map[string]any    `json:"options,omitempty"`
}

// oaiChatRequest is the OpenAI /v1/chat/completions request shape.
type oaiChatRequest struct {
	Model                string            `json:"model"`
	Messages             []json.RawMessage `json:"messages"`
	Stream               bool              `json:"stream,omitempty"`
	StreamOptions        *oaiStreamOptions `json:"stream_options,omitempty"`
	Temperature          *float64          `json:"temperature,omitempty"`
	MaxTokens            int               `json:"max_tokens,omitempty"`
	TopP                 *float64          `json:"top_p,omitempty"`
	ThinkingBudgetTokens *int              `json:"thinking_budget_tokens,omitempty"`
}

type oaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func (p *ollamaProxy) handleLlamaCpp(w http.ResponseWriter, r *http.Request, endpoint, model string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	switch r.URL.Path {
	case "/api/chat":
		p.llamaCppChat(w, r.Context(), endpoint, model, bodyBytes)
	case "/api/generate":
		p.llamaCppGenerate(w, r.Context(), endpoint, model, bodyBytes)
	default:
		http.Error(w, "not supported via llama.cpp backend", http.StatusNotFound)
	}
}

func (p *ollamaProxy) llamaCppChat(w http.ResponseWriter, ctx context.Context, endpoint, model string, body []byte) {
	var req ollamaChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	tag := virtualModelTag(body)

	oai := oaiChatRequest{
		Model:    model,
		Messages: req.Messages,
		Stream:   req.Stream,
	}
	if req.Stream {
		t := true
		_ = t
		oai.StreamOptions = &oaiStreamOptions{IncludeUsage: true}
	}

	// Translate Ollama options → OpenAI top-level fields.
	if v, ok := req.Options["temperature"]; ok {
		if f, ok := floatVal(v); ok {
			oai.Temperature = &f
		}
	}
	if v, ok := req.Options["top_p"]; ok {
		if f, ok := floatVal(v); ok {
			oai.TopP = &f
		}
	}
	if v, ok := req.Options["num_predict"]; ok {
		if f, ok := floatVal(v); ok {
			oai.MaxTokens = int(f)
		}
	}

	// Thinking budget: gohort:no-think or think:false → budget 0.
	if tag == "no-think" || (req.Think != nil && !*req.Think) {
		zero := 0
		oai.ThinkingBudgetTokens = &zero
	}

	oaiBody, err := json.Marshal(oai)
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}

	targetURL := strings.TrimRight(endpoint, "/") + "/chat/completions"
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(oaiBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(upstream)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		http.Error(w, string(respBody), resp.StatusCode)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	if req.Stream {
		p.streamOAIToOllama(w, resp.Body, virtualModel)
	} else {
		p.convertOAIToOllama(w, resp.Body, virtualModel)
	}
}

// llamaCppGenerate handles /api/generate by wrapping the prompt as a chat message.
func (p *ollamaProxy) llamaCppGenerate(w http.ResponseWriter, ctx context.Context, endpoint, model string, body []byte) {
	var req struct {
		Model   string         `json:"model"`
		Prompt  string         `json:"prompt"`
		Stream  bool           `json:"stream"`
		Think   *bool          `json:"think,omitempty"`
		Options map[string]any `json:"options,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	msgJSON, _ := json.Marshal(map[string]string{"role": "user", "content": req.Prompt})
	synth := ollamaChatRequest{
		Model:    req.Model,
		Messages: []json.RawMessage{msgJSON},
		Stream:   req.Stream,
		Think:    req.Think,
		Options:  req.Options,
	}
	synthBody, _ := json.Marshal(synth)
	p.llamaCppChat(w, ctx, endpoint, model, synthBody)
}

// convertOAIToOllama reads a non-streaming OpenAI response and writes an Ollama response.
func (p *ollamaProxy) convertOAIToOllama(w http.ResponseWriter, body io.Reader, outModel string) {
	respBody, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadGateway)
		return
	}

	var oaiResp struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &oaiResp); err != nil || len(oaiResp.Choices) == 0 {
		http.Error(w, "bad upstream response", http.StatusBadGateway)
		return
	}

	out := map[string]any{
		"model":      outModel,
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"message": map[string]string{
			"role":    oaiResp.Choices[0].Message.Role,
			"content": oaiResp.Choices[0].Message.Content,
		},
		"done":                 true,
		"done_reason":          oaiResp.Choices[0].FinishReason,
		"prompt_eval_count":    oaiResp.Usage.PromptTokens,
		"eval_count":           oaiResp.Usage.CompletionTokens,
		"total_duration":       0,
		"load_duration":        0,
		"prompt_eval_duration": 0,
		"eval_duration":        0,
	}
	json.NewEncoder(w).Encode(out)
}

// streamOAIToOllama reads an OpenAI SSE stream and writes Ollama NDJSON chunks.
func (p *ollamaProxy) streamOAIToOllama(w http.ResponseWriter, body io.Reader, outModel string) {
	flusher, canFlush := w.(http.Flusher)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)

	var promptTokens, completionTokens int

	writeChunk := func(content string, done bool, doneReason string) {
		chunk := map[string]any{
			"model":      outModel,
			"created_at": time.Now().UTC().Format(time.RFC3339),
			"message": map[string]string{
				"role":    "assistant",
				"content": content,
			},
			"done": done,
		}
		if done {
			chunk["done_reason"] = doneReason
			chunk["prompt_eval_count"] = promptTokens
			chunk["eval_count"] = completionTokens
			chunk["total_duration"] = 0
			chunk["load_duration"] = 0
			chunk["prompt_eval_duration"] = 0
			chunk["eval_duration"] = 0
		}
		line, _ := json.Marshal(chunk)
		w.Write(line)
		w.Write([]byte("\n"))
		if canFlush {
			flusher.Flush()
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			promptTokens = chunk.Usage.PromptTokens
			completionTokens = chunk.Usage.CompletionTokens
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			writeChunk("", true, *choice.FinishReason)
			return
		}
		if choice.Delta.Content != "" {
			writeChunk(choice.Delta.Content, false, "")
		}
	}

	// Emit final done if stream ended without a finish_reason chunk.
	writeChunk("", true, "stop")
}

// --- helpers shared by both backends ---

func (p *ollamaProxy) handleTags(w http.ResponseWriter) {
	resp := map[string]any{
		"models": []map[string]any{
			makeModelEntry("latest"),
			makeModelEntry("no-think"),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func makeModelEntry(tag string) map[string]any {
	name := virtualModel + ":" + tag
	return map[string]any{
		"name":        name,
		"model":       name,
		"modified_at": "2024-01-01T00:00:00Z",
		"size":        0,
		"digest":      "",
		"details": map[string]any{
			"parent_model":       "",
			"format":             "gguf",
			"family":             virtualModel,
			"families":           []string{virtualModel},
			"parameter_size":     "",
			"quantization_level": "",
		},
	}
}

func floatVal(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func virtualModelTag(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return ""
	}
	raw, ok := obj["model"]
	if !ok {
		return ""
	}
	var modelStr string
	if err := json.Unmarshal(raw, &modelStr); err != nil {
		return ""
	}
	if strings.HasPrefix(modelStr, virtualModel+":") {
		return strings.TrimPrefix(modelStr, virtualModel+":")
	}
	return ""
}

func injectNumCtx(data []byte, numCtx int) []byte {
	if numCtx <= 0 {
		return data
	}
	return injectOptions(data, "num_ctx", numCtx)
}

func injectOptions(data []byte, key string, value any) []byte {
	if len(data) == 0 {
		return data
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return data
	}
	var options map[string]json.RawMessage
	if raw, ok := obj["options"]; ok {
		if err := json.Unmarshal(raw, &options); err != nil {
			options = nil
		}
	}
	if options == nil {
		options = make(map[string]json.RawMessage)
	}
	valBytes, err := json.Marshal(value)
	if err != nil {
		return data
	}
	options[key] = valBytes
	optRaw, err := json.Marshal(options)
	if err != nil {
		return data
	}
	obj["options"] = optRaw
	out, err := json.Marshal(obj)
	if err != nil {
		return data
	}
	return out
}

func rewriteModel(data []byte, from, to string) []byte {
	if len(data) == 0 || !bytes.Contains(data, []byte(`"`+from)) {
		return data
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return data
	}
	raw, ok := obj["model"]
	if !ok {
		return data
	}
	var modelStr string
	if err := json.Unmarshal(raw, &modelStr); err != nil {
		return data
	}
	if modelStr != from && !strings.HasPrefix(modelStr, from+":") {
		return data
	}
	toJSON, _ := json.Marshal(to)
	obj["model"] = toJSON
	out, err := json.Marshal(obj)
	if err != nil {
		return data
	}
	return out
}

func backendRoot(backend string) string {
	u, err := url.Parse(backend)
	if err != nil {
		return strings.TrimRight(backend, "/")
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// callerIP identifies the caller for the fair-queue scheduler.
//
// X-Forwarded-For is deliberately NOT honored. The returned value is the key
// the scheduler round-robins on, so trusting a caller-supplied header let one
// client present a fresh identity per request and take as many slots as it
// liked while everyone else waited their turn. The same reasoning
// peerRequestSource documents in core/peer_key.go: behind a proxy every caller
// then shares one key, which schedules them together rather than not at all,
// and that is the safe direction to be wrong in.
func callerIP(r *http.Request) string { return directPeer(r) }

// directPeer returns the real TCP peer address, ignoring forwarding headers.
func directPeer(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
