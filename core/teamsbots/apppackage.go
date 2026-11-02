package teamsbots

// The Teams app package: what a person uploads to Teams (Apps > Manage your
// apps > Upload an app) so the bot can be found and added. A zip of a
// manifest naming the bot and two icons Teams requires, made here so nobody
// writes the manifest by hand.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"strings"
)

const (
	manifestVersion = "1.17"
	manifestSchema  = "https://developer.microsoft.com/en-us/json-schemas/teams/v1.17/MicrosoftTeams.schema.json"
	accent          = "#4F46E5"
)

// teamsScopes are the manifest's names for the conversation types.
var teamsScopes = map[string]string{"personal": "personal", "groupChat": "groupChat", "channel": "team"}

// Manifest is b's Teams app manifest, its links on site, this instance's
// https address.
func (b Bot) Manifest(site string) (map[string]any, error) {
	u, err := url.Parse(strings.TrimSpace(site))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("the app package needs an https address for this site (its links and its valid domain): %q is not one", site)
	}
	base := "https://" + u.Host
	var scopes []string
	for _, ct := range ConversationTypes {
		if b.Accepts(ct) {
			scopes = append(scopes, teamsScopes[ct])
		}
	}
	// Never the owner's gohort name, which may be an email address: anyone
	// who finds the app in Teams reads this.
	short := b.Name + ": an assistant's agents in Teams"
	return map[string]any{
		"$schema":         manifestSchema,
		"manifestVersion": manifestVersion,
		"version":         "1.0.0",
		"id":              b.AppID,
		"developer": map[string]string{
			"name": b.Name, "websiteUrl": base, "privacyUrl": base, "termsOfUseUrl": base,
		},
		"name":        map[string]string{"short": b.Name, "full": b.Name},
		"description": map[string]string{"short": short, "full": short + ", through gohort at " + u.Host + "."},
		"icons":       map[string]string{"color": "color.png", "outline": "outline.png"},
		"accentColor": accent,
		"bots": []map[string]any{{
			"botId": b.AppID, "scopes": scopes, "supportsFiles": false, "isNotificationOnly": false,
		}},
		"permissions":  []string{"identity", "messageTeamMembers"},
		"validDomains": []string{u.Host},
	}, nil
}

// AppPackage is b's Teams app package, a zip of its manifest and icons.
func (b Bot) AppPackage(site string) ([]byte, error) {
	m, err := b.Manifest(site)
	if err != nil {
		return nil, err
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"manifest.json", manifest},
		{"color.png", colorIcon()},
		{"outline.png", outlineIcon()},
	} {
		w, err := z.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// colorIcon is the full-colour icon Teams asks for: 192 by 192, the accent
// with a white disc.
func colorIcon() []byte {
	const n = 192
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	bg := color.NRGBA{0x4F, 0x46, 0xE5, 0xFF}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := x-n/2, y-n/2
			if dx*dx+dy*dy < 44*44 {
				img.Set(x, y, color.White)
			} else {
				img.Set(x, y, bg)
			}
		}
	}
	return encodePNG(img)
}

// outlineIcon is the outline icon Teams asks for: 32 by 32, white on
// transparent.
func outlineIcon() []byte {
	const n = 32
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := x-n/2, y-n/2
			if d := dx*dx + dy*dy; d < 13*13 && d >= 10*10 {
				img.Set(x, y, color.White)
			}
		}
	}
	return encodePNG(img)
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}
