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
	region = bedrockRegion(region)

	host := endpoint
	if host == "" {
		host = fmt.Sprintf("bedrock-runtime.%s.amazonaws.com", region)
	}
	host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://"), "/")

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
//
// The colon in versioned model ids ("...-v1:0") has to be percent-encoded, and
// url.PathEscape will not do it: RFC 3986 permits a literal colon inside a
// path segment, so PathEscape leaves it. SigV4 canonicalization does not agree
// — it encodes reserved characters — so AWS would sign %3A while we sent ":"
// and reject the request with a signature mismatch that says nothing about
// paths. Encoding it here makes the sent path and the signed path (both read
// from URL.EscapedPath) identical AND matches AWS's normalization.
func (c *bedrockRuntimeClient) invokePath() string {
	return "/model/" + strings.ReplaceAll(url.PathEscape(c.model), ":", "%3A") + "/invoke"
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
		return nil, noteIfAdaptiveThinking(c.model, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: "bedrock-runtime"})
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
	return "/model/" + strings.ReplaceAll(url.PathEscape(c.model), ":", "%3A") + "/invoke-with-response-stream"
}

// ChatStream streams a response token by token.
//
// The transport differs from every other streaming path here — AWS frames the
// events in its binary event-stream encoding rather than SSE — but the events
// inside are the ordinary Anthropic ones, so the frames are unwrapped and fed
// to the same accumulator the SSE reader uses. See llm_bedrock_eventstream.go.
func (c *bedrockRuntimeClient) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.model, anthDefaultStreamMaxTokens, opts)
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
		return nil, noteIfAdaptiveThinking(c.model, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: "bedrock-runtime"})
	}

	st := &anthStreamState{handler: handler}
	reader := newEventStreamReader(resp.Body)
	for {
		frame, err := reader.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Partial output is still worth returning: the caller has already
			// seen these tokens through the handler, and discarding them would
			// turn a truncated answer into an empty one.
			if st.textContent.Len() > 0 || len(st.toolCalls) > 0 {
				Warn("[bedrock-runtime]: stream ended early (%v) — returning the partial response", err)
				return st.response("bedrock-runtime"), nil
			}
			return nil, fmt.Errorf("stream read error: %w", err)
		}
		event, err := decodeBedrockEvent(frame)
		if err != nil {
			return nil, err
		}
		if event == nil {
			continue // keep-alive, or a frame type that carries no event
		}
		st.feed(event)
		applyBedrockMetrics(st, event)
	}
	return st.response("bedrock-runtime"), nil
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
		Debug("[bedrock-runtime]: invocation metrics present but unreadable — token counts stay as the stream reported them")
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
