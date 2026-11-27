package decide

// What a local model server actually serves under the name gohort calls it.
//
// A tier's identity was its provider, address and model name. A local server
// is usually started with a fixed served name ("local"), so swapping the
// weights behind it changed nothing the identity could see: the judge map
// kept the mappings the old weights had earned, and System One went on
// answering checks the new weights had not been tested on. On 2026-11-27 the
// new weights got two Channel gatekeeper cases wrong that the old ones never
// had, one of them confidently, with System One still mapped for it.
//
// vLLM's /v1/models says, for each served name, the folder its weights were
// loaded from (root). That goes into the identity, so new weights lapse every
// mapping until the check tests have run on them (admin's map pass looks for
// lapsed mappings on its own tick and runs them).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// servedFresh is how long a server's answer is used before it is asked
// again. A swap means a restart, which takes minutes, so a minute is enough.
const servedFresh = time.Minute

// servedTimeout bounds one ask of a server's model list.
const servedTimeout = 3 * time.Second

type servedEntry struct {
	weights  string
	at       time.Time
	fetching bool
}

var served = struct {
	sync.Mutex
	m map[string]servedEntry
}{m: map[string]servedEntry{}}

// servedLocal reports a provider whose server is started with a served name
// of its own choosing, so the name alone does not say which weights answer.
func servedLocal(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "vllm", "llama.cpp", "mlx":
		return true
	}
	return false
}

// servedWeights is what the server at endpoint serves as model: the weights
// it loaded them from, "" when it does not say or cannot be asked. The first
// ask waits for the answer; after that a stale answer is used while a fresh
// one is fetched, so a decision never waits on it.
func servedWeights(endpoint, model, apiKey string) string {
	key := endpoint + "\x00" + model
	served.Lock()
	e, ok := served.m[key]
	if !ok {
		served.Unlock()
		w := fetchServedWeights(endpoint, model, apiKey)
		served.Lock()
		served.m[key] = servedEntry{weights: w, at: time.Now()}
		served.Unlock()
		return w
	}
	if time.Since(e.at) > servedFresh && !e.fetching {
		e.fetching = true
		served.m[key] = e
		go func() {
			w := fetchServedWeights(endpoint, model, apiKey)
			served.Lock()
			served.m[key] = servedEntry{weights: w, at: time.Now()}
			served.Unlock()
		}()
	}
	served.Unlock()
	return e.weights
}

// fetchServedWeights asks the server's /v1/models which weights model is.
// vLLM names them as root (the path it loaded); a server that says nothing
// beyond the name gives "".
func fetchServedWeights(endpoint, model, apiKey string) string {
	u, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(endpoint), "/"))
	if err != nil || u.Host == "" {
		return ""
	}
	p := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/chat/completions")
	if !strings.HasSuffix(p, "/v1") {
		p += "/v1"
	}
	u.Path, u.RawQuery = p+"/models", ""
	ctx, cancel := context.WithTimeout(context.Background(), servedTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ""
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var list struct {
		Data []struct {
			ID   string `json:"id"`
			Root string `json:"root"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&list) != nil {
		return ""
	}
	pick := func(root, id string) string {
		if root = strings.TrimSpace(root); root == "" || root == id {
			return ""
		}
		return root
	}
	for _, m := range list.Data {
		if m.ID == model {
			return pick(m.Root, m.ID)
		}
	}
	// One model served under another name than the one configured: it is
	// still the one that answers.
	if len(list.Data) == 1 {
		return pick(list.Data[0].Root, list.Data[0].ID)
	}
	return ""
}
