package servitor

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An exec run for a peer returns its capture whole, and the calling side
// spills it into ITS store — so the output_id the agent gets is one it can
// page, and the exec notice stays visible after the spill note.
func TestPeerCaptureIsPagedOnTheCallingSide(t *testing.T) {
	T := &Servitor{}
	local, _ := T.exec_local_ctx(context.Background(), "seq 1 6000", "", nil)
	if !strings.Contains(local, "output_id=") {
		t.Fatalf("a local exec over the cap must spill here:\n%.200s", local)
	}

	raw, err := T.exec_local_ctx(withRawCapture(context.Background()), "seq 1 6000; exit 3", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "output_id=") || !strings.Contains(raw, "\n6000\n") || !strings.HasSuffix(raw, "[exit code 3]") {
		t.Fatalf("an exec for a peer must return the whole capture with its notice, got %d chars ending %q", len(raw), raw[len(raw)-40:])
	}

	reply := spillPeerCapture(raw)
	if !strings.Contains(reply, "output_id=") || !strings.HasSuffix(reply, "\n[exit code 3]") {
		t.Fatalf("the caller must spill the body and keep the notice last:\n...%s", reply[len(reply)-300:])
	}
	if i, j := strings.Index(reply, "TRUNCATED"), strings.Index(reply, "[exit code 3]"); i < 0 || j < i {
		t.Fatalf("the exit notice must follow the spill note")
	}
	id := reply[strings.Index(reply, "output_id=\"")+11:]
	id = id[:strings.Index(id, "\"")]
	page, err := OutputPage{ID: id, Offset: 20000, Max: 500, Tool: "run_command"}.Read()
	if err != nil || !strings.Contains(page, "\n") {
		t.Fatalf("the capture must page on this side: %v %q", err, page)
	}
	if hits, _ := (OutputPage{ID: id, Tool: "run_command", Grep: "^5999$"}).Read(); !strings.HasPrefix(hits, "1 of ") {
		t.Fatalf("and grep on this side: %q", hits)
	}
}

// Only the exec paths' own notices are peeled; a command whose output ends in
// a bracketed line keeps it in the body.
func TestSplitExecTrailer(t *testing.T) {
	body, trailer := splitExecTrailer("line 1\nline 2\n[TIMED OUT after 5m — command killed.]\n[exit code -1]")
	if body != "line 1\nline 2" || trailer != "\n[TIMED OUT after 5m — command killed.]\n[exit code -1]" {
		t.Fatalf("got body %q trailer %q", body, trailer)
	}
	body, trailer = splitExecTrailer("service ok\n[ OK ] started")
	if body != "service ok\n[ OK ] started" || trailer != "" {
		t.Fatalf("a bracketed output line is not a notice: %q %q", body, trailer)
	}
	if b, tr := splitExecTrailer("[exit code 3 — no output]"); b != "[exit code 3 — no output]" || tr != "" {
		t.Fatalf("a notice that is the whole text stays as the body: %q %q", b, tr)
	}
	if !strings.Contains(spillCapture(withRawCapture(context.Background()), strings.Repeat("x\n", peerCaptureMax)), "[capture clipped at") {
		t.Fatal("a whole capture over the bound is clipped with a notice")
	}
}
