package sandbox

import (
	"strings"
	"testing"
)

// A folder granted for writing is mounted read-write at its own path under
// bubblewrap, joins the writable set under Seatbelt, and is mounted without
// :ro in a container; one inside the workspace needs nothing.
func TestWritableFoldersAreWritableOnEveryBackend(t *testing.T) {
	args := []string{"--bind", "/ws", "/ws", "--", "sh", "-c", "true"}
	got := strings.Join(withWritableBinds(args, []string{"/stores/intake", "/ws/inside", "/stores/intake"}, "/ws"), " ")
	if strings.Count(got, "--bind-try /stores/intake /stores/intake") != 1 {
		t.Errorf("the store should be bound read-write once: %s", got)
	}
	if strings.Contains(got, "/ws/inside") {
		t.Errorf("a folder inside the workspace was bound again: %s", got)
	}
	if !strings.HasSuffix(got, "-- sh -c true") {
		t.Errorf("the bind went after the command: %s", got)
	}

	p := seatbeltProfile(seatbeltSpec{Workspace: "/ws", Writable: []string{"/stores/intake"}})
	write := p[strings.Index(p, "(allow file-read* file-write*"):]
	if !strings.Contains(write, `(subpath "/stores/intake")`) {
		t.Errorf("Seatbelt does not let the command write the store:\n%s", p)
	}
	if strings.Contains(seatbeltProfile(seatbeltSpec{Workspace: "/ws"}), "/stores/intake") {
		t.Error("a store nobody granted became writable")
	}
}
