package core

// AWS Bedrock, legacy InvokeModel path (bedrock-runtime).
//
// This is the SECOND Bedrock mode. The first (llm_bedrock.go) targets the
// Messages-API endpoint, which is the better integration: same request shape,
// same SSE streaming, everything reused. It needs the IAM action
// `bedrock-mantle:CreateInference`.
//
// Plenty of AWS orgs have not granted that. A permission set provisioned for
// AI coding tools typically grants `bedrock:InvokeModel` and
// `bedrock:InvokeModelWithResponseStream` instead — a different service
// namespace entirely — so those credentials reach this endpoint and are denied
// by the other one. That is not a misconfiguration to fix, it is a different
// API, and an operator whose org grants the older action cannot talk their way
// past it. Hence both modes.
//
// What differs from the Messages API, and it is all of the surface area:
//   - host bedrock-runtime.{region}.amazonaws.com, SigV4 service "bedrock"
//   - the model id rides in the URL, not the body
//   - the body carries anthropic_version instead of model
//   - responses are otherwise the identical Anthropic shape, so the whole
//     parse path is reused unchanged
//
// Streaming works here too, but over a different wire: this endpoint frames its
// events in AWS's binary event-stream encoding rather than SSE. The frames are
// decoded in llm_bedrock_eventstream.go and the events inside — ordinary
// Anthropic stream events — feed the same accumulator the SSE path uses.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
)

const (
	// bedrockRuntimeService is the SigV4 service name for the legacy endpoint.
	// Note it is plain "bedrock", NOT "bedrock-mantle" — the two endpoints sign
	// as different services, and swapping them yields a signature mismatch that
	// reads like a bad key.
	bedrockRuntimeService = "bedrock"

	// bedrockAnthropicVersion is what this endpoint expects in place of a model
	// field. It is a fixed Bedrock-specific string, not an API date to bump.
	bedrockAnthropicVersion = "bedrock-2023-05-31"
)

// bedrockInvokeRequest is the Messages API body as InvokeModel wants it: no
// model (that is in the URL), no stream (that is the endpoint), and an
// anthropic_version instead.
type bedrockInvokeRequest struct {
	AnthropicVersion string            `json:"anthropic_version"`
	Messages         []anthMessage     `json:"messages"`
	MaxTokens        int               `json:"max_tokens"`
	System           []anthSystemBlock `json:"system,omitempty"`
	Tools            []anthTool        `json:"tools,omitempty"`
	// Same block the direct endpoint takes — InvokeModel carries the Messages
	// body verbatim, so thinking crosses unchanged. See anthThinkingFor.
	Thinking     *anthThinking     `json:"thinking,omitempty"`
	OutputConfig *anthOutputConfig `json:"output_config,omitempty"`
	// AnthropicBeta is how a beta is declared on InvokeModel: there is no
	// place for the header the direct API uses, so the body carries it.
	// Only populated when the extended cache TTL is actually on.
	AnthropicBeta []string `json:"anthropic_beta,omitempty"`
}

// bedrockRuntimeClient implements LLM against bedrock-runtime InvokeModel.
type bedrockRuntimeClient struct {
	model  string
	region string
	api    *apiclient.APIClient
	creds  *bedrockCreds
	// contextSize is the operator-configured working context cap (tokens);
	// 0 falls back to anthropicDefaultContextSize (same Claude models, same
	// economics — see the const in llm_anthropic.go).
	contextSize int
	// effort is the tier's default and maximum effort; see resolveEffort.
	effort effortTier
}

// ContextSize implements ContextSizer — see anthropicClient.ContextSize for
// why this is a working cap rather than the model's true 1M window.
func (c *bedrockRuntimeClient) ContextSize() int {
	if c.contextSize > 0 {
		return c.contextSize
	}
	return anthropicDefaultContextSize
}

// newBedrockRuntimeLLM builds a client for the legacy InvokeModel endpoint.
// Credentials resolve exactly as they do for the Messages-API mode, including
// the explicit-profile rules — the difference is the endpoint, not the auth.
func newBedrockRuntimeLLM(bearer, model, region, profile, endpoint string, api *apiclient.APIClient) (LLM, error) {
	configured := region
	region = bedrockRegion(region)

	host := endpoint
	if host == "" {
		host = fmt.Sprintf("bedrock-runtime.%s.amazonaws.com", region)
	}
	host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://"), "/")
	// The HOST is where the call actually goes, and it is not always what the
	// region setting says - an Endpoint value wins over it outright. That is
	// the one case nothing on screen can show, because the region box goes on
	// reading what it was set to.
	Debug("[bedrock-runtime] model=%s host=%s %s", bedrockModelID(model), host,
		bedrockRegionNote(configured, region, endpoint))

	if api == nil {
		api = &apiclient.APIClient{
			VerifySSL:      true,
			ConnectTimeout: llmConnectTimeout(),
			RequestTimeout: llmRequestTimeout(),
		}
	}
	api.Server = host

	c := &bedrockRuntimeClient{
		model:  bedrockModelID(model),
		region: region,
		api:    api,
	}
	if bearer == "" {
		c.creds = &bedrockCreds{profile: bedrockProfile(profile), explicit: profile != ""}
		if _, err := c.creds.get(); err != nil {
			return nil, err
		}
	}

	api.AuthFunc = func(req *http.Request) {
		if bearer != "" {
			// The legacy endpoint takes a Bedrock bearer token as an ordinary
			// Authorization header, unlike the Messages-API endpoint's x-api-key.
			req.Header.Set("Authorization", "Bearer "+bearer)
			return
		}
		var payload []byte
		if req.GetBody != nil {
			if rc, err := req.GetBody(); err == nil {
				payload, _ = io.ReadAll(rc)
				rc.Close()
			}
		}
		creds, err := c.creds.get()
		if err != nil {
			Debug("[bedrock-runtime]: credential refresh failed, sending unsigned: %v", err)
			return
		}
		if err := signAWSV4(req, payload, creds, region, bedrockRuntimeService, time.Now().UTC()); err != nil {
			Debug("[bedrock-runtime]: SigV4 signing failed, sending unsigned: %v", err)
		}
	}
	return c, nil
}

// invokePath builds /model/{id}/invoke.
func (c *bedrockRuntimeClient) invokePath() string {
	return "/model/" + bedrockPathModel(c.model) + "/invoke"
}

// bedrockPathModel escapes a model id for the URL path, which is NOT the same
// rule for every kind of id.
//
// The colon in a VERSIONED model id ("...-v1:0") has to be percent-encoded,
// and url.PathEscape will not do it: RFC 3986 permits a literal colon inside a
// path segment, so PathEscape leaves it. AWS's canonicalization does not agree
// — it encodes reserved characters — so AWS would sign %3A while we sent ":"
// and reject the request with a signature mismatch that says nothing about
// paths. Encoding it makes the sent path and the signed path (both read from
// URL.EscapedPath) identical AND matches AWS's normalization.
//
// AN ARN IS THE OPPOSITE. Its colons are structure, not data, and AWS's own
// SDK leaves them raw while percent-encoding only the slash inside the
// resource id. Encoding them too produces a path AWS normalizes back to
// something else, and the request is refused with exactly that signature
// mismatch — from the other direction.
//
// This stayed invisible until an ARN turned up because no model id before one
// contained a character that needed escaping at all: "us.anthropic.claude-
// opus-5" escapes to itself, so the two rules could not be told apart.
func bedrockPathModel(model string) string {
	if strings.HasPrefix(model, "arn:") {
		return url.PathEscape(model)
	}
	return strings.ReplaceAll(url.PathEscape(model), ":", "%3A")
}

// hoistSystemMessages moves system-role turns out of the message list, which
// Bedrock rejects outright:
//
//	messages.0: use the top-level 'system' parameter for the initial system prompt
//
// The first-party API accepts a mid-conversation system message on current
// models and gohort uses that, so the generic builder emits the role verbatim.
// Bedrock supports it on neither endpoint, so it has to be rendered into
// something the endpoint does accept, per position:
//
//   - LEADING system turns are the initial system prompt wearing a different
//     hat. They join the top-level system field, which is where they were
//     always meant to go.
//   - LATER ones are operator instructions delivered mid-conversation, and
//     folding them into the system prompt would silently move them earlier in
//     the conversation than they were said. They become user turns wrapped in
//     <system-reminder>, the documented fallback for models without the
//     feature: the content survives, in position, marked as not-the-user.
//
// Dropping them was the other option and is worse in a way that never shows up
// as an error — the model simply stops being told something it was told.
func hoistSystemMessages(messages []Message) (lead string, rest []Message) {
	var leading []string
	seenTurn := false
	for _, m := range messages {
		if m.Role != "system" {
			seenTurn = true
			rest = append(rest, m)
			continue
		}
		if !seenTurn {
			if txt := strings.TrimSpace(m.Content); txt != "" {
				leading = append(leading, txt)
			}
			continue
		}
		if txt := strings.TrimSpace(m.Content); txt != "" {
			m.Role = "user"
			m.Content = "<system-reminder>" + txt + "</system-reminder>"
			rest = append(rest, m)
		}
	}
	return strings.Join(leading, "\n\n"), rest
}

// buildBody assembles the InvokeModel envelope from the shared builders, so
// tool marshalling, cache breakpoints, and system blocks stay identical to
// every other Anthropic-shaped path.
func (c *bedrockRuntimeClient) buildBody(messages []Message, cfg ChatConfig) ([]byte, error) {
	systemPrompt := cfg.SystemPrompt
	if cfg.JSONMode {
		jsonInstr := "You must respond with valid JSON only. No markdown, no explanation, just JSON."
		if systemPrompt != "" {
			systemPrompt = systemPrompt + "\n\n" + jsonInstr
		} else {
			systemPrompt = jsonInstr
		}
	}
	lead, messages := hoistSystemMessages(messages)
	if lead != "" {
		if systemPrompt != "" {
			systemPrompt = systemPrompt + "\n\n" + lead
		} else {
			systemPrompt = lead
		}
	}
	msgs, err := buildAnthMessages(messages)
	if err != nil {
		return nil, err
	}
	addCacheBreakpoint(msgs)
	thinking, outCfg, maxTokens := anthThinkingFor(cfg, cfg.MaxTokens)
	return json.Marshal(bedrockInvokeRequest{
		AnthropicVersion: bedrockAnthropicVersion,
		Messages:         msgs,
		MaxTokens:        maxTokens,
		Thinking:         thinking,
		OutputConfig:     outCfg,
		System:           buildSystemBlocks(systemPrompt),
		Tools:            buildAnthTools(cfg.Tools),
		AnthropicBeta:    promptCacheBetas(),
	})
}

// Chat sends a non-streaming InvokeModel request.
func (c *bedrockRuntimeClient) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.model, anthDefaultMaxTokens, opts)
	c.effort.resolve(&cfg)
	body, err := c.buildBody(messages, cfg)
	if err != nil {
		return nil, err
	}

	path := c.invokePath()
	Debug("[bedrock-runtime]: Sending request to %s%s", c.api.Server, path)

	req, err := c.api.NewRequestWithContext(ctx, "POST", path)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Body = io.NopCloser(strings.NewReader(string(body)))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(body))), nil
	}

	resp, err := c.api.SendRawRequest("", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	snoopAnthResponse(resp.StatusCode, respBody)

	if resp.StatusCode != http.StatusOK {
		// AWS errors arrive as {"message": "..."} rather than Anthropic's
		// {"error":{"message":...}}, so try both before falling back to raw.
		msg := string(respBody)
		var apiErr anthError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			msg = apiErr.Error.Message
		} else {
			var awsErr struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(respBody, &awsErr) == nil && awsErr.Message != "" {
				msg = awsErr.Message
			}
		}
		// AWS words this for somebody holding the API reference. Where the
		// refusal has a setting behind it, say which one - see bedrockHint.
		return nil, noteIfAdaptiveThinking(c.model, refused(&APIError{
			StatusCode: resp.StatusCode, Message: withBedrockHint(c.model, msg), Provider: "bedrock-runtime"}, c.model))
	}

	var result anthResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	r := parseAnthResponse(result)
	warnStopReason(r.StopReason)
	Debug("[bedrock-runtime]: Response: model=%s input_tokens=%d output_tokens=%d tool_calls=%d stop=%s",
		r.Model, r.InputTokens, r.OutputTokens, len(r.ToolCalls), r.StopReason)
	return r, nil
}

// streamPath builds /model/{id}/invoke-with-response-stream, escaped the same
// way invokePath is.
func (c *bedrockRuntimeClient) streamPath() string {
	return "/model/" + bedrockPathModel(c.model) + "/invoke-with-response-stream"
}

// ChatStream streams a response token by token.
//
// The transport differs from every other streaming path here — AWS frames the
// events in its binary event-stream encoding rather than SSE — but the events
// inside are the ordinary Anthropic ones, so the frames are unwrapped and fed
// to the same accumulator the SSE reader uses. See llm_bedrock_eventstream.go.
func (c *bedrockRuntimeClient) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.model, anthDefaultStreamMaxTokens, opts)
	c.effort.resolve(&cfg)
	body, err := c.buildBody(messages, cfg)
	if err != nil {
		return nil, err
	}

	path := c.streamPath()
	Debug("[bedrock-runtime]: Sending streaming request to %s%s", c.api.Server, path)

	req, err := c.api.NewRequestWithContext(ctx, "POST", path)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	req.Body = io.NopCloser(strings.NewReader(string(body)))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(body))), nil
	}

	resp, err := c.api.SendRawRequest("", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		msg := string(respBody)
		var awsErr struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(respBody, &awsErr) == nil && awsErr.Message != "" {
			msg = awsErr.Message
		}
		// AWS words this for somebody holding the API reference. Where the
		// refusal has a setting behind it, say which one - see bedrockHint.
		return nil, noteIfAdaptiveThinking(c.model, refused(&APIError{
			StatusCode: resp.StatusCode, Message: withBedrockHint(c.model, msg), Provider: "bedrock-runtime"}, c.model))
	}

	st := &anthStreamState{handler: handler, reasoning: cfg.ReasoningHandler}
	reader := newEventStreamReader(resp.Body)
	// Every way out goes through finish: it is the one place that knows a
	// stream without its terminal stop_reason is a fragment. Partial output
	// is still returned — the caller has already rendered those tokens
	// through the handler — but marked interrupted, so the loop continues
	// the reply instead of delivering the fragment as the answer.
	for {
		frame, err := reader.next()
		if err == io.EOF {
			return st.finish("bedrock-runtime", nil)
		}
		if err != nil {
			return st.finish("bedrock-runtime", err)
		}
		event, err := decodeBedrockEvent(frame)
		if err != nil {
			// An exception event mid-answer (throttling, a model stream error)
			// is the same fragment as a dropped connection; with nothing yet
			// delivered it is the error itself.
			return st.finish("bedrock-runtime", err)
		}
		if event == nil {
			continue // keep-alive, or a frame type that carries no event
		}
		st.feed(event)
		applyBedrockMetrics(st, event)
	}
}

// applyBedrockMetrics reads the billed token counts off the final chunk.
//
// This endpoint does NOT report usage the way the Anthropic wire does. Its
// message_start carries a placeholder input count — observed as 2 against a
// prompt of several thousand tokens — and nothing later corrects it, so a turn
// costing dollars was being recorded as costing nothing. Every per-turn cost
// figure, budget check and usage total drawn from an InvokeModel turn was wrong
// by orders of magnitude, and wrong in the direction nobody investigates.
//
// The real counts arrive once, on the last chunk, under a Bedrock-specific key
// alongside the ordinary message_stop event. They are what AWS bills on, so
// they win over anything the stream said earlier.
func applyBedrockMetrics(st *anthStreamState, event []byte) {
	// Only the final chunk carries it; skip the re-parse for every other frame.
	if !bytes.Contains(event, []byte(bedrockMetricsKey)) {
		return
	}
	var wrapper struct {
		Metrics *struct {
			InputTokenCount  int `json:"inputTokenCount"`
			OutputTokenCount int `json:"outputTokenCount"`
		} `json:"amazon-bedrock-invocationMetrics"`
	}
	if err := json.Unmarshal(event, &wrapper); err != nil || wrapper.Metrics == nil {
		Debug("[bedrock-runtime]: invocation metrics present but unreadable, token counts stay as the stream reported them")
		return
	}
	// Guard on >0 so a metrics block that omits a field cannot zero out a count
	// the stream did report correctly.
	//
	// And do NOT override once the stream has reported cache tokens. This count
	// is the billed TOTAL for the prompt; the Anthropic events break that same
	// prompt into uncached/read/written. Overwriting the uncached part with the
	// total and then summing the three double-counts everything cached — which
	// is most of a long conversation. When there is no cache breakdown, this is
	// the only real number available and it wins, which is the placeholder case
	// this was written for.
	if n := wrapper.Metrics.InputTokenCount; n > 0 {
		if st.cacheRead > 0 || st.cacheWrite > 0 {
			Debug("[bedrock-runtime]: billed input %d; keeping the stream's breakdown (uncached=%d cache_read=%d cache_write=%d)",
				n, st.inputTokens, st.cacheRead, st.cacheWrite)
		} else {
			if st.inputTokens > 0 && n != st.inputTokens {
				Debug("[bedrock-runtime]: input tokens %d -> %d (billed)", st.inputTokens, n)
			}
			st.inputTokens = n
		}
	}
	if n := wrapper.Metrics.OutputTokenCount; n > 0 {
		st.outputTokens = n
	}
}

// bedrockMetricsKey is the JSON key Bedrock appends to the last streamed chunk.
const bedrockMetricsKey = "amazon-bedrock-invocationMetrics"

// bedrockModelList reads the account's Claude models from the Bedrock control
// plane (bedrock.{region}.amazonaws.com, the same for either API setting):
// the inference profiles, which most accounts must call newer models through,
// then the foundation models that can be called by bare id. Only Anthropic's
// are kept on the two modes that speak Claude's format; Converse keeps all. The
// listing needs bedrock:ListInferenceProfiles and bedrock:ListFoundationModels,
// which a role that may only invoke will not have; either one is enough to
// offer something, and the error comes back only when both are refused.
func bedrockModelList(ctx context.Context, cfg LLMProviderConfig) ([]string, error) {
	region := bedrockRegion(cfg.Region)
	host := fmt.Sprintf("https://bedrock.%s.amazonaws.com", region)
	var creds *bedrockCreds
	if cfg.APIKey == "" {
		creds = &bedrockCreds{profile: bedrockProfile(cfg.Profile), explicit: cfg.Profile != ""}
		if _, err := creds.get(); err != nil {
			return nil, err
		}
	}
	auth := func(req *http.Request) {
		if cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
			return
		}
		c, err := creds.get()
		if err != nil {
			return
		}
		// Content-Type is among the headers SigV4 signs here; a GET has
		// none, so it is set rather than signed as blank.
		req.Header.Set("Content-Type", "application/json")
		signAWSV4(req, nil, c, region, bedrockRuntimeService, time.Now().UTC())
	}
	// Claude's only, unless the Converse API is in use: the other two modes
	// speak Claude's format, Converse speaks every model's.
	claude := func(id string) bool { return cfg.BedrockAPI == "converse" || strings.Contains(id, "anthropic.") }

	var ids []string
	var profileErr error
	next := ""
	for i := 0; i < 10; i++ {
		// The wire parameter is "type". NOT "typeEquals" - that is the name in
		// the API model and in the AWS SDKs' own field, which is where the
		// wrong one comes from, and an unrecognised query parameter here is
		// IGNORED rather than rejected. The listing then falls back to its
		// default, which happens to be SYSTEM_DEFINED, so asking for
		// SYSTEM_DEFINED with the wrong parameter name returns exactly the
		// right answer and the mistake is invisible. It stops being invisible
		// the moment somebody asks for APPLICATION profiles and gets a 200,
		// a plausible list, and not one profile their account can invoke.
		// Confirmed against `aws bedrock list-inference-profiles --debug`,
		// which builds "?maxResults=100&type=APPLICATION".
		q := url.Values{"maxResults": {"1000"}, "type": {"SYSTEM_DEFINED"}}
		if next != "" {
			q.Set("nextToken", next)
		}
		var result struct {
			Profiles []struct {
				ID     string `json:"inferenceProfileId"`
				Status string `json:"status"`
			} `json:"inferenceProfileSummaries"`
			Next string `json:"nextToken"`
		}
		if profileErr = getModelList(ctx, "bedrock", host+"/inference-profiles?"+q.Encode(), auth, &result); profileErr != nil {
			break
		}
		for _, p := range result.Profiles {
			if claude(p.ID) && (p.Status == "" || p.Status == "ACTIVE") {
				ids = append(ids, p.ID)
			}
		}
		if next = result.Next; next == "" {
			break
		}
	}

	var result struct {
		Models []struct {
			ID        string   `json:"modelId"`
			Inference []string `json:"inferenceTypesSupported"`
		} `json:"modelSummaries"`
	}
	q := url.Values{"byOutputModality": {"TEXT"}}
	modelErr := getModelList(ctx, "bedrock", host+"/foundation-models?"+q.Encode(), auth, &result)
	for _, m := range result.Models {
		if !claude(m.ID) {
			continue
		}
		for _, t := range m.Inference {
			if t == "ON_DEMAND" {
				ids = append(ids, m.ID)
				break
			}
		}
	}
	if profileErr != nil && modelErr != nil {
		return nil, profileErr
	}
	return ids, nil
}

// ==== Converse API =========================================================

// AWS Bedrock, Converse API (bedrock-runtime /converse and /converse-stream).
//
// The THIRD Bedrock mode, and the default for a new Bedrock setup. The other
// two speak Claude's own Messages format, so they serve Claude only. Converse
// is AWS's one format for every model Bedrock hosts (Claude, Llama, Mistral,
// Nova, ...), and is the API most AWS accounts are set up to use. It is
// authorized by the same IAM actions as InvokeModel (bedrock:InvokeModel and
// bedrock:InvokeModelWithResponseStream), on the same host, signed as the same
// service, so this client reuses the InvokeModel client's construction and
// differs only in the request and response shapes.
//
// Streaming arrives in AWS's binary event-stream framing like InvokeModel's,
// but the events inside are Converse's own (messageStart, contentBlockDelta,
// ...), not Anthropic's. They are translated into the Anthropic events the
// shared accumulator (anthStreamState) already assembles, so tool-call
// assembly, thinking, the interrupted-reply rule and the decode clock are the
// same code on every Claude-capable path.

// converseDefaultContextSize is the working cap for a model that is not
// Claude, when the operator set none. Small on purpose: Bedrock hosts models
// from 8k to 1M tokens, and compacting early costs a summary where
// overrunning costs the call. Set Context size on the tier to use more.
const converseDefaultContextSize = 32_768

// bedrockConverseClient implements LLM against the Converse API.
type bedrockConverseClient struct {
	rt *bedrockRuntimeClient
}

// newBedrockConverseLLM builds a Converse client. Host, credentials and
// signing are InvokeModel's; only the model id rule differs (see
// converseModelID).
func newBedrockConverseLLM(bearer, model, region, profile, endpoint string, api *apiclient.APIClient) (LLM, error) {
	inner, err := newBedrockRuntimeLLM(bearer, model, region, profile, endpoint, api)
	if err != nil {
		return nil, err
	}
	rt := inner.(*bedrockRuntimeClient)
	rt.model = converseModelID(model)
	return &bedrockConverseClient{rt: rt}, nil
}

// converseModelID is the model id as Converse wants it. Every vendor's id
// already names its vendor ("meta.llama3-...", "us.amazon.nova-pro-v1:0"), so
// one with a dot, or an ARN, passes through untouched. Only a bare Claude id
// ("claude-opus-5") takes the anthropic. prefix, as on the other two modes.
func converseModelID(model string) string {
	model = strings.TrimSpace(model)
	if strings.HasPrefix(model, "arn:") || strings.Contains(model, ".") {
		return model
	}
	return bedrockModelID(model)
}

// converseIsClaude reports whether a model id names Claude, which decides the
// Claude-only parts of a request: thinking, and the default output allowance.
// An application inference profile's ARN says nothing about its model; it is
// taken as Claude, which is what such profiles are made for in practice.
func converseIsClaude(model string) bool {
	return strings.HasPrefix(model, "arn:") || strings.Contains(model, "anthropic.") || strings.Contains(model, "claude")
}

// ContextSize implements ContextSizer.
func (c *bedrockConverseClient) ContextSize() int {
	if c.rt.contextSize > 0 {
		return c.rt.contextSize
	}
	if converseIsClaude(c.rt.model) {
		return anthropicDefaultContextSize
	}
	return converseDefaultContextSize
}

// --- request -------------------------------------------------------------

type cvRequest struct {
	Messages        []cvMessage    `json:"messages"`
	System          []cvBlock      `json:"system,omitempty"`
	InferenceConfig *cvInference   `json:"inferenceConfig,omitempty"`
	ToolConfig      *cvToolConfig  `json:"toolConfig,omitempty"`
	AdditionalModel map[string]any `json:"additionalModelRequestFields,omitempty"`
}

type cvInference struct {
	MaxTokens int `json:"maxTokens,omitempty"`
}

type cvMessage struct {
	Role    string    `json:"role"`
	Content []cvBlock `json:"content"`
}

// cvBlock is Converse's content union: exactly one member is set.
type cvBlock struct {
	Text       string        `json:"text,omitempty"`
	Image      *cvImage      `json:"image,omitempty"`
	ToolUse    *cvToolUse    `json:"toolUse,omitempty"`
	ToolResult *cvToolResult `json:"toolResult,omitempty"`
	Reasoning  *cvReasoning  `json:"reasoningContent,omitempty"`
	CachePoint *cvCachePoint `json:"cachePoint,omitempty"`
}

type cvImage struct {
	Format string `json:"format"`
	Source struct {
		Bytes []byte `json:"bytes"` // base64 on the wire, by encoding/json
	} `json:"source"`
}

type cvToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type cvToolResult struct {
	ToolUseID string    `json:"toolUseId"`
	Content   []cvBlock `json:"content"`
	Status    string    `json:"status,omitempty"`
}

type cvReasoning struct {
	ReasoningText *struct {
		Text      string `json:"text"`
		Signature string `json:"signature,omitempty"`
	} `json:"reasoningText,omitempty"`
	// Streamed deltas carry these directly.
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type cvCachePoint struct {
	Type string `json:"type"` // "default"
}

type cvToolConfig struct {
	Tools []cvTool `json:"tools"`
}

// cvTool is a toolSpec, or a cachePoint after the last one.
type cvTool struct {
	ToolSpec   *cvToolSpec   `json:"toolSpec,omitempty"`
	CachePoint *cvCachePoint `json:"cachePoint,omitempty"`
}

type cvToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		JSON json.RawMessage `json:"json"`
	} `json:"inputSchema"`
}

// Prompt caching on Converse is a cachePoint block, and a model that does not
// support it refuses the whole request rather than ignoring the marker. Which
// models do is not derivable from an id that will keep working (ARNs say
// nothing), so caching is tried and the model is BELIEVED when it refuses:
// one 400 per model per process, as with Claude's thinking shape.
var (
	converseNoCacheMu     sync.RWMutex
	converseNoCacheModels = map[string]bool{}
)

func converseCaches(model string) bool {
	converseNoCacheMu.RLock()
	defer converseNoCacheMu.RUnlock()
	return !converseNoCacheModels[model]
}

// noteConverseCacheRefusal records a model that refused cachePoint and says
// whether that is what this error was.
func noteConverseCacheRefusal(model, msg string) bool {
	low := strings.ToLower(msg)
	if !strings.Contains(low, "prompt caching") && !strings.Contains(low, "cachepoint") && !strings.Contains(low, "cache point") {
		return false
	}
	converseNoCacheMu.Lock()
	converseNoCacheModels[model] = true
	converseNoCacheMu.Unlock()
	Debug("[bedrock-converse]: %s refused prompt caching, sending without cache points from now on", model)
	return true
}

// buildBody assembles a Converse request.
func (c *bedrockConverseClient) buildBody(messages []Message, cfg ChatConfig) ([]byte, error) {
	model := c.rt.model
	cache := converseCaches(model)
	claude := converseIsClaude(model)

	systemPrompt := cfg.SystemPrompt
	if cfg.JSONMode {
		systemPrompt = joinNonEmpty(systemPrompt, "You must respond with valid JSON only. No markdown, no explanation, just JSON.")
	}
	lead, messages := hoistSystemMessages(messages)
	systemPrompt = joinNonEmpty(systemPrompt, lead)

	req := cvRequest{Messages: buildConverseMessages(messages, len(cfg.Tools) > 0)}
	if systemPrompt != "" {
		req.System = []cvBlock{{Text: systemPrompt}}
		if cache {
			req.System = append(req.System, cvBlock{CachePoint: &cvCachePoint{Type: "default"}})
		}
	}
	if len(cfg.Tools) > 0 {
		tc := &cvToolConfig{}
		for _, t := range cfg.Tools {
			spec := &cvToolSpec{Name: t.Name, Description: t.Description}
			if strings.TrimSpace(spec.Description) == "" {
				spec.Description = t.Name // required, and may not be blank
			}
			spec.InputSchema.JSON = buildToolParamsSchema(t)
			tc.Tools = append(tc.Tools, cvTool{ToolSpec: spec})
		}
		if cache {
			tc.Tools = append(tc.Tools, cvTool{CachePoint: &cvCachePoint{Type: "default"}})
		}
		req.ToolConfig = tc
	}
	if cache && len(req.Messages) > 0 {
		last := &req.Messages[len(req.Messages)-1]
		last.Content = append(last.Content, cvBlock{CachePoint: &cvCachePoint{Type: "default"}})
	}

	maxTokens := cfg.MaxTokens
	if claude {
		// Claude's thinking travels as Claude's own fields, which Converse
		// passes through to the model untouched.
		thinking, outCfg, mt := anthThinkingFor(cfg, maxTokens)
		maxTokens = mt
		extra := map[string]any{}
		if thinking != nil {
			extra["thinking"] = thinking
		}
		if outCfg != nil {
			extra["output_config"] = outCfg
		}
		if len(extra) > 0 {
			req.AdditionalModel = extra
		}
	}
	// Other models' output limits run from 2k to 32k and a larger request is
	// refused, so they get the model's own default unless a caller asked.
	if maxTokens > 0 {
		req.InferenceConfig = &cvInference{MaxTokens: maxTokens}
	}
	return json.Marshal(req)
}

// joinNonEmpty joins the non-blank parts with a blank line.
func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

// buildConverseMessages converts the generic history. Two rules Converse
// holds that the Messages API does not:
//
//   - Roles must alternate. Consecutive turns from one role (tool results
//     followed by a user message, say) are merged into one turn.
//   - Tool blocks are refused in a request that defines no tools. A call made
//     without tools (a summary of a conversation that used them) gets its
//     tool calls and results as plain text instead.
func buildConverseMessages(messages []Message, tools bool) []cvMessage {
	var out []cvMessage
	add := func(role string, blocks ...cvBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, cvMessage{Role: role, Content: blocks})
	}
	for _, m := range messages {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			var blocks []cvBlock
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, cvBlock{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Args
				if args == nil {
					args = map[string]any{}
				}
				input, _ := json.Marshal(args)
				if !tools {
					blocks = append(blocks, cvBlock{Text: fmt.Sprintf("I called %s with %s.", tc.Name, input)})
					continue
				}
				blocks = append(blocks, cvBlock{ToolUse: &cvToolUse{ToolUseID: anthToolID(tc.ID), Name: tc.Name, Input: input}})
			}
			add("assistant", blocks...)
		case len(m.ToolResults) > 0:
			var blocks []cvBlock
			for _, tr := range m.ToolResults {
				text := tr.Content
				if strings.TrimSpace(text) == "" {
					text = "(no output)" // a blank text block is refused
				}
				if !tools {
					blocks = append(blocks, cvBlock{Text: "Tool result: " + text})
					continue
				}
				status := "success"
				if tr.IsError {
					status = "error"
				}
				blocks = append(blocks, cvBlock{ToolResult: &cvToolResult{
					ToolUseID: anthToolID(tr.ID), Content: []cvBlock{{Text: text}}, Status: status}})
			}
			add("user", blocks...)
		default:
			role := m.Role
			if role != "assistant" {
				role = "user"
			}
			var blocks []cvBlock
			if role == "user" {
				for _, img := range m.Images {
					if converseImageFormat(img) == "" {
						img = resizeImage(img) // converted to an image it reads
					}
					if f := converseImageFormat(img); f != "" {
						b := cvBlock{Image: &cvImage{Format: f}}
						b.Image.Source.Bytes = img
						blocks = append(blocks, b)
					}
				}
			}
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, cvBlock{Text: m.Content})
			}
			add(role, blocks...)
		}
	}
	return out
}

// converseImageFormat is an image's format as Converse names it, or "" for
// one it does not take.
func converseImageFormat(b []byte) string {
	switch http.DetectContentType(b) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpeg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	}
	return ""
}

// --- transport -----------------------------------------------------------

// send posts body to path and returns the response, or the API error with
// Bedrock's hint. retry reports a cache refusal, after which the caller
// rebuilds the body (now without cache points) and sends once more.
func (c *bedrockConverseClient) send(ctx context.Context, path, accept string, body []byte) (resp *http.Response, retry bool, err error) {
	req, err := c.rt.api.NewRequestWithContext(ctx, "POST", path)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Body = io.NopCloser(strings.NewReader(string(body)))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(body))), nil
	}
	resp, err = c.rt.api.SendRawRequest("", req)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp, false, nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	snoopAnthResponse(resp.StatusCode, respBody)
	msg := string(respBody)
	var awsErr struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(respBody, &awsErr) == nil && awsErr.Message != "" {
		msg = awsErr.Message
	}
	if resp.StatusCode == http.StatusBadRequest && converseCaches(c.rt.model) && noteConverseCacheRefusal(c.rt.model, msg) {
		return nil, true, nil
	}
	return nil, false, noteIfAdaptiveThinking(c.rt.model, refused(&APIError{
		StatusCode: resp.StatusCode, Message: withBedrockHint(c.rt.model, msg), Provider: "bedrock-converse"}, c.rt.model))
}

// post builds and sends a request, rebuilding once after a cache refusal.
func (c *bedrockConverseClient) post(ctx context.Context, path, accept string, messages []Message, cfg ChatConfig) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		body, err := c.buildBody(messages, cfg)
		if err != nil {
			return nil, err
		}
		Debug("[bedrock-converse]: Sending request to %s%s", c.rt.api.Server, path)
		resp, retry, err := c.send(ctx, path, accept, body)
		if err != nil || !retry {
			return resp, err
		}
	}
	return nil, fmt.Errorf("bedrock converse: %s refused the request twice over prompt caching", c.rt.model)
}

func (c *bedrockConverseClient) path(suffix string) string {
	return "/model/" + bedrockPathModel(c.rt.model) + "/" + suffix
}

// defaultMaxTokens is the output allowance when a caller sets none: Claude's
// usual one, and the model's own default for anything else (see buildBody).
func (c *bedrockConverseClient) defaultMaxTokens(stream bool) int {
	if !converseIsClaude(c.rt.model) {
		return 0
	}
	if stream {
		return anthDefaultStreamMaxTokens
	}
	return anthDefaultMaxTokens
}

// --- response ------------------------------------------------------------

type cvUsage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadInputTokens"`
	CacheWriteTokens int `json:"cacheWriteInputTokens"`
}

type cvResponse struct {
	Output struct {
		Message cvMessage `json:"message"`
	} `json:"output"`
	StopReason string  `json:"stopReason"`
	Usage      cvUsage `json:"usage"`
}

// converseStopReason maps Converse's stop reasons onto the Anthropic ones the
// loop reads. A guardrail or content filter is a refusal.
func converseStopReason(s string) string {
	switch s {
	case "guardrail_intervened", "content_filtered":
		return "refusal"
	case "model_context_window_exceeded":
		return "max_tokens"
	}
	return s
}

// Chat sends a non-streaming Converse request.
func (c *bedrockConverseClient) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.rt.model, c.defaultMaxTokens(false), opts)
	c.rt.effort.resolve(&cfg)
	resp, err := c.post(ctx, c.path("converse"), "application/json", messages, cfg)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	snoopAnthResponse(resp.StatusCode, respBody)
	var result cvResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	var text, thinking strings.Builder
	var calls []ToolCall
	for _, b := range result.Output.Message.Content {
		switch {
		case b.Text != "":
			text.WriteString(b.Text)
		case b.Reasoning != nil && b.Reasoning.ReasoningText != nil:
			thinking.WriteString(b.Reasoning.ReasoningText.Text)
		case b.ToolUse != nil:
			args, err := decodeToolInput(b.ToolUse.Input)
			argsErr := ""
			if err != nil {
				Warn("[bedrock-converse]: tool %q: %v", b.ToolUse.Name, err)
				argsErr = err.Error()
			}
			calls = append(calls, ToolCall{ID: b.ToolUse.ToolUseID, Name: b.ToolUse.Name, Args: args, ArgsError: argsErr})
		}
	}
	r := &Response{
		Content:          text.String(),
		ToolCalls:        calls,
		Model:            c.rt.model,
		InputTokens:      result.Usage.InputTokens,
		OutputTokens:     result.Usage.OutputTokens,
		CacheReadTokens:  result.Usage.CacheReadTokens,
		CacheWriteTokens: result.Usage.CacheWriteTokens,
		StopReason:       converseStopReason(result.StopReason),
	}
	r.ReasoningTokens = estimateReasoningTokens(r.OutputTokens, thinking.String(), r.Content, calls)
	warnStopReason(r.StopReason)
	Debug("[bedrock-converse]: Response: model=%s input_tokens=%d output_tokens=%d tool_calls=%d stop=%s",
		r.Model, r.InputTokens, r.OutputTokens, len(r.ToolCalls), r.StopReason)
	return r, nil
}

// cvStreamEvent is the payload of one ConverseStream event; which fields are
// set depends on the frame's :event-type.
type cvStreamEvent struct {
	Index int `json:"contentBlockIndex"`
	Start *struct {
		ToolUse *cvToolUse `json:"toolUse"`
	} `json:"start"`
	Delta *struct {
		Text    string `json:"text"`
		ToolUse *struct {
			Input string `json:"input"`
		} `json:"toolUse"`
		Reasoning *cvReasoning `json:"reasoningContent"`
	} `json:"delta"`
	StopReason string   `json:"stopReason"`
	Usage      *cvUsage `json:"usage"`
}

// converseStream translates ConverseStream events into the Anthropic events
// anthStreamState assembles. Converse opens a block only for a tool call; a
// text or reasoning block starts with its first delta, so the start is
// supplied here.
type converseStream struct {
	st      *anthStreamState
	started map[int]bool
}

func (cs *converseStream) emit(ev anthStreamEvent) {
	if b, err := json.Marshal(ev); err == nil {
		cs.st.feed(b)
	}
}

func (cs *converseStream) open(i int, kind string) {
	if cs.started[i] {
		return
	}
	cs.started[i] = true
	cs.emit(anthStreamEvent{Type: "content_block_start", Index: i, ContentBlock: &anthContentBlock{Type: kind}})
}

func (cs *converseStream) apply(kind string, payload []byte) {
	var ev cvStreamEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return
	}
	switch kind {
	case "contentBlockStart":
		if ev.Start != nil && ev.Start.ToolUse != nil {
			cs.started[ev.Index] = true
			cs.emit(anthStreamEvent{Type: "content_block_start", Index: ev.Index, ContentBlock: &anthContentBlock{
				Type: "tool_use", ID: ev.Start.ToolUse.ToolUseID, Name: ev.Start.ToolUse.Name}})
		}
	case "contentBlockDelta":
		if ev.Delta == nil {
			return
		}
		switch {
		case ev.Delta.ToolUse != nil:
			cs.emit(anthStreamEvent{Type: "content_block_delta", Index: ev.Index,
				Delta: &anthContentBlock{Type: "input_json_delta", PartialJSON: ev.Delta.ToolUse.Input}})
		case ev.Delta.Reasoning != nil:
			if ev.Delta.Reasoning.Text != "" {
				cs.open(ev.Index, "thinking")
				cs.emit(anthStreamEvent{Type: "content_block_delta", Index: ev.Index,
					Delta: &anthContentBlock{Type: "thinking_delta", Thinking: ev.Delta.Reasoning.Text}})
			}
		case ev.Delta.Text != "":
			cs.open(ev.Index, "text")
			cs.emit(anthStreamEvent{Type: "content_block_delta", Index: ev.Index,
				Delta: &anthContentBlock{Type: "text_delta", Text: ev.Delta.Text}})
		}
	case "contentBlockStop":
		cs.emit(anthStreamEvent{Type: "content_block_stop", Index: ev.Index})
	case "messageStop":
		cs.st.stopReason = converseStopReason(ev.StopReason)
	case "metadata":
		if u := ev.Usage; u != nil {
			cs.st.inputTokens = u.InputTokens
			cs.st.outputTokens = u.OutputTokens
			cs.st.cacheRead = u.CacheReadTokens
			cs.st.cacheWrite = u.CacheWriteTokens
		}
	}
}

// ChatStream streams a Converse response token by token.
func (c *bedrockConverseClient) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.rt.model, c.defaultMaxTokens(true), opts)
	c.rt.effort.resolve(&cfg)
	resp, err := c.post(ctx, c.path("converse-stream"), "application/vnd.amazon.eventstream", messages, cfg)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	st := &anthStreamState{handler: handler, reasoning: cfg.ReasoningHandler, model: c.rt.model}
	cs := &converseStream{st: st, started: map[int]bool{}}
	reader := newEventStreamReader(resp.Body)
	for {
		frame, err := reader.next()
		if err == io.EOF {
			return st.finish("bedrock-converse", nil)
		}
		if err != nil {
			return st.finish("bedrock-converse", err)
		}
		if mt := frame.messageType(); mt == "exception" || mt == "error" {
			_, err := decodeBedrockEvent(frame)
			return st.finish("bedrock-converse", err)
		}
		cs.apply(frame.Headers[":event-type"], frame.Payload)
	}
}
