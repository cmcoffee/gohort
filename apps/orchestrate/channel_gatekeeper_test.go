package orchestrate

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The gate judges a voice note by what was said, as it judges typed text. It
// used to see a voice note as an empty message, and every wake rule blocked
// it, so an agent in a gated room could not be spoken to at all.
func TestTheGateHearsAVoiceNote(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: `{"answer":"YES","reason":"asks about the weather"}`, Repeat: true})
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "u", Service: "imessage", Address: "chat-1", AgentID: df.agent.ID,
		Gatekeeper: "Answer only questions about the weather."})

	status := http.StatusOK
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte("what's the weather tomorrow"))
	}))
	defer stt.Close()
	prevSTT := GetTranscribeConfig()
	SetTranscribeConfig(TranscribeConfig{Endpoint: stt.URL, Enabled: true})
	t.Cleanup(func() { SetTranscribeConfig(prevSTT) })

	judged := func(in ChannelInbound) string {
		t.Helper()
		before := df.llm.Calls()
		if !df.app.channelGatekeeperAllow(t.Context(), in) {
			t.Fatal("the gate answered YES; the message should be let through")
		}
		if df.llm.Calls() != before+1 {
			t.Fatalf("the gate should have asked the model once, asked %d times", df.llm.Calls()-before)
		}
		var b strings.Builder
		for _, m := range df.llm.Sent(before) {
			b.WriteString(m.Content)
		}
		return b.String()
	}
	in := ChannelInbound{Owner: "u", AgentID: df.agent.ID, ChatID: "chat-1", SenderName: "Alice", Handle: "+15550100",
		Audios: []string{base64.StdEncoding.EncodeToString([]byte("not really an m4a"))}}

	if got := judged(in); !strings.Contains(got, "[voice note: what's the weather tomorrow]") {
		t.Errorf("the gate should judge the words of the voice note: %s", got)
	}

	status = http.StatusInternalServerError
	if got := judged(in); !strings.Contains(got, "[voice note that could not be transcribed]") {
		t.Errorf("an unheard voice note should still read as one: %s", got)
	}

	video := ChannelInbound{Owner: "u", AgentID: df.agent.ID, ChatID: "chat-1", SenderName: "Alice", Handle: "+15550100",
		Videos: []string{base64.StdEncoding.EncodeToString([]byte("clip"))}}
	if got := judged(video); !strings.Contains(got, "[video, no text: 1 video(s)]") {
		t.Errorf("a video with no text should read as a video, not as nothing: %s", got)
	}
}
