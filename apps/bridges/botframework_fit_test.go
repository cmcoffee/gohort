package bridges

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/jwcrypt"
)

// A verified token must be for THIS activity: its serviceurl claim names the
// activity's reply address, and its key is endorsed for the activity's channel.
func TestABotTokenMustFitItsActivity(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"keys":[{"kty":"RSA","use":"sig","alg":"RS256","kid":"k1","n":%q,"e":%q,"endorsements":["msteams"]}]}`,
			b64(k.N.Bytes()), b64(big.NewInt(int64(k.E)).Bytes()))
	}))
	defer keys.Close()
	v := BotFrameworkVerifier()
	prev := v.JWKSURL
	v.JWKSURL = keys.URL
	t.Cleanup(func() { v.JWKSURL = prev })

	tok, err := jwcrypt.SignRS256(k, map[string]interface{}{"aud": "app"}, map[string]string{"kid": "k1"})
	if err != nil {
		t.Fatal(err)
	}
	T := &Bridges{}
	ctx := context.Background()
	act := botActivity{ServiceURL: "https://smba.trafficmanager.net/amer/", ChannelID: "msteams"}

	if err := T.botTokenFitsActivity(ctx, tok, jwcrypt.Claims{"serviceurl": "https://smba.trafficmanager.net/amer"}, act); err != nil {
		t.Errorf("a fitting token was refused: %v", err)
	}
	if err := T.botTokenFitsActivity(ctx, tok, jwcrypt.Claims{"serviceurl": "https://elsewhere.example/"}, act); err == nil {
		t.Error("a token for another serviceUrl carried this activity")
	}
	other := act
	other.ChannelID = "webchat"
	if err := T.botTokenFitsActivity(ctx, tok, jwcrypt.Claims{}, other); err == nil {
		t.Error("a key endorsed for msteams signed a webchat activity")
	}
}
