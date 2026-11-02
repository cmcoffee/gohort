package teamsbots

// Replies: a token minted from the bot's own app id and secret (the client
// credentials grant), sent only to an address under the bot's service host.

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
)

// TokenURL is the token endpoint of tenant. A variable so a test can point
// it elsewhere.
var TokenURL = func(tenant string) string {
	return "https://login.microsoftonline.com/" + url.PathEscape(tenant) + "/oauth2/v2.0/token"
}

// tokenTenant is where b's reply token comes from: its own tenant, or Bot
// Framework's for a multi-tenant bot.
func (b Bot) tokenTenant() string {
	if b.TenantID == "" {
		return "botframework.com"
	}
	return b.TenantID
}

// tokenScope is what a reply token is asked for.
const tokenScope = "https://api.botframework.com/.default"

// HTTPClient makes a bot's token and reply requests. A variable so a test can
// trust a stand-in for Microsoft.
var HTTPClient = &http.Client{Timeout: 20 * time.Second}

// token is a reply token for b, from the cache while it has a minute left.
func token(ctx context.Context, b Bot) (string, error) {
	tokens.Lock()
	t, ok := tokens.by[b.Key()]
	tokens.Unlock()
	if ok && time.Now().Before(t.until) {
		return t.value, nil
	}
	secret := Secret(b.Owner, b.Service)
	if secret == "" {
		return "", fmt.Errorf("the bot has no client secret")
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {b.AppID},
		"client_secret": {secret}, "scope": {tokenScope}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL(b.tokenTenant()), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("asking for a reply token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var got struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error_description"`
	}
	json.Unmarshal(body, &got)
	if resp.StatusCode >= 300 || got.AccessToken == "" {
		why := got.Error
		if why == "" {
			why = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", fmt.Errorf("Microsoft refused a reply token for app %s: %s", b.AppID, firstLine(why))
	}
	life := time.Duration(got.ExpiresIn) * time.Second
	if life <= 2*time.Minute {
		life = 2 * time.Minute
	}
	tokens.Lock()
	tokens.by[b.Key()] = cachedToken{value: got.AccessToken, until: time.Now().Add(life - time.Minute)}
	tokens.Unlock()
	return got.AccessToken, nil
}

// Within reports whether serviceURL is under b's service host: the only
// places its replies, and its token, go.
func (b Bot) Within(serviceURL string) bool {
	host := strings.TrimRight(b.ServiceHost, "/")
	if host == "" {
		host = DefaultServiceHost
	}
	return strings.HasPrefix(strings.TrimRight(serviceURL, "/")+"/", host+"/")
}

// Send posts activity into conversation conv behind serviceURL, as b.
func Send(ctx context.Context, b Bot, serviceURL, conv string, activity any) error {
	serviceURL = strings.TrimRight(strings.TrimSpace(serviceURL), "/")
	if !b.Within(serviceURL) {
		return fmt.Errorf("%s is outside the bot's service host %s", serviceURL, b.ServiceHost)
	}
	tok, err := token(ctx, b)
	if err != nil {
		return err
	}
	body, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serviceURL+"/v3/conversations/"+url.PathEscape(conv)+"/activities", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		forgetToken(b.Key())
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return fmt.Errorf("Teams refused the reply: HTTP %d %s", resp.StatusCode, firstLine(string(b)))
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
