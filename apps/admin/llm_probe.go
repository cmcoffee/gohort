package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// What a local model server says about itself, so the LLM test can say what
// is actually at the endpoint rather than only that something answered.
//
// The case it exists for: a worker set to llama.cpp that had been pointed at
// a vLLM server. Chat worked, so nothing looked wrong, while the thinking
// budget oddjob sent was silently ignored for as long as it ran. The server
// names itself in /v1/models; asking costs one request.

// serverProbe is what a model server reported. Kind is "" when it could not
// be told.
type serverProbe struct {
	Kind    string // "vllm" | "llama.cpp" | "ollama"
	Model   string // the model it serves, as it names it
	Context int    // the context window it allows, in tokens; 0 = not reported
}

// probeTimeout bounds each probe request. The chat check that follows has its
// own, longer deadline; this only has to read a small JSON document.
const probeTimeout = 5 * time.Second

// probeModelServer asks the server at endpoint what it is. It never fails the
// test: a server that will not say is reported as unknown, and the chat check
// still decides whether the endpoint works.
func probeModelServer(ctx context.Context, provider, endpoint, apiKey string) serverProbe {
	base := strings.TrimSuffix(strings.TrimSpace(endpoint), "/")
	if base == "" {
		base = map[string]string{
			"llama.cpp": "http://localhost:8080/v1",
			"vllm":      "http://localhost:8000/v1",
			"ollama":    "http://localhost:11434",
		}[provider]
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return serverProbe{}
	}
	root := u.Scheme + "://" + u.Host

	// Ollama answers /api/version and nothing else does.
	var ver struct {
		Version string `json:"version"`
	}
	if probeJSON(ctx, root+"/api/version", apiKey, &ver) && ver.Version != "" {
		return serverProbe{Kind: "ollama"}
	}

	modelsURL := base + "/models"
	if !strings.HasSuffix(u.Path, "/v1") {
		modelsURL = root + "/v1/models"
	}
	var models struct {
		Data []struct {
			ID          string `json:"id"`
			OwnedBy     string `json:"owned_by"`
			Root        string `json:"root"`          // vLLM: the weights' path
			MaxModelLen int    `json:"max_model_len"` // vLLM
			Meta        struct {
				NCtx int `json:"n_ctx"` // llama.cpp
			} `json:"meta"`
		} `json:"data"`
	}
	if !probeJSON(ctx, modelsURL, apiKey, &models) || len(models.Data) == 0 {
		return serverProbe{}
	}
	m := models.Data[0]
	out := serverProbe{Model: m.ID}
	switch strings.ToLower(m.OwnedBy) {
	case "vllm":
		out.Kind = "vllm"
		out.Context = m.MaxModelLen
		if r := path.Base(strings.TrimSuffix(m.Root, "/")); r != "" && r != "." && r != "/" && r != m.ID {
			out.Model = m.ID + " (" + r + ")"
		}
	case "llamacpp":
		out.Kind = "llama.cpp"
		out.Model = path.Base(m.ID)
		out.Context = m.Meta.NCtx
		if out.Context == 0 {
			var props struct {
				Settings struct {
					NCtx int `json:"n_ctx"`
				} `json:"default_generation_settings"`
			}
			if probeJSON(ctx, root+"/props", apiKey, &props) {
				out.Context = props.Settings.NCtx
			}
		}
	}
	return out
}

// probeJSON GETs url and decodes a JSON body into v, reporting whether that
// worked. Bounded, and cancelled with the test request.
func probeJSON(ctx context.Context, rawURL, apiKey string, v any) bool {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v) == nil
}

// serverKindLabel is how a server kind is written for a person.
func serverKindLabel(kind string) string {
	switch kind {
	case "vllm":
		return "vLLM"
	case "ollama":
		return "Ollama"
	}
	return kind
}

// localContextDefault is the context oddjob assumes for a local provider when
// the field is blank (core's ollamaDefaultCtx).
const localContextDefault = 65536

// describe says what the probe found and what, given the form's settings,
// should change. provider and contextSize are what the form holds.
func (p serverProbe) describe(provider string, contextSize int) string {
	if p.Kind == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(" Server: " + serverKindLabel(p.Kind))
	if p.Model != "" {
		b.WriteString(", serving " + p.Model)
	}
	if p.Context > 0 {
		b.WriteString(fmt.Sprintf(", context %s tokens", commaInt(p.Context)))
	}
	b.WriteString(".")
	if p.Kind != provider {
		b.WriteString(fmt.Sprintf(" The provider is set to %s, but this endpoint is %s: pick %s, so oddjob speaks to it the way it expects.",
			serverKindLabel(provider), serverKindLabel(p.Kind), serverKindLabel(p.Kind)))
		if p.Kind == "vllm" {
			b.WriteString(" (vLLM ignores the thinking budget; effort is what controls its thinking.)")
		}
	}
	if p.Context > 0 && p.Kind != "ollama" {
		switch {
		case contextSize <= 0 && p.Context != localContextDefault:
			b.WriteString(fmt.Sprintf(" Context size is blank, so oddjob works within %s tokens; the server allows %s.",
				commaInt(localContextDefault), commaInt(p.Context)))
		case contextSize > p.Context:
			b.WriteString(fmt.Sprintf(" Context size is %s, more than the server's %s: the longest conversations would be refused. Set it to %s.",
				commaInt(contextSize), commaInt(p.Context), commaInt(p.Context)))
		}
	}
	return b.String()
}

// commaInt writes n with thousands separators.
func commaInt(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
