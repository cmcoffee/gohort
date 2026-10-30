package appassets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recordFilesHere(t *testing.T) string {
	t.Helper()
	prev := RecordFilesDir()
	dir := t.TempDir()
	SetRecordFilesDir(dir)
	t.Cleanup(func() { SetRecordFilesDir(prev) })
	return dir
}

func writeSrc(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A kept file is a copy in its record's own folder, listed by a flat name
// of its own, numbered when the record already holds one by that name, and
// titled from its name when no title is given.
func TestARecordKeepsACopyOfAFile(t *testing.T) {
	recordFilesHere(t)
	src := writeSrc(t, "Money in the Cloud!.mp3", []byte("ID3\x04\x00\x00\x00\x00\x00\x00take one"))
	a, err := KeepRecordFile("owner", "songs", "someone@example.com", "song1", src, "First take")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "Money-in-the-Cloud.mp3" || a.Title != "First take" || a.Type != "audio/mpeg" || a.Size == 0 || a.Added == "" {
		t.Errorf("entry %+v", a)
	}
	b, err := KeepRecordFile("owner", "songs", "someone@example.com", "song1", src, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Money-in-the-Cloud-2.mp3" || b.Title != "Money in the Cloud!" {
		t.Errorf("a second take of the same name: %+v", b)
	}
	path, ct, err := RecordFilePath("owner", "songs", "someone@example.com", "song1", a.Name)
	if err != nil || ct != "audio/mpeg" {
		t.Fatal(err, ct)
	}
	if got, _ := os.ReadFile(path); !strings.HasSuffix(string(got), "take one") {
		t.Errorf("the copy holds %q", got)
	}
	// Another person's record of the same id is another folder.
	if other, _, _ := RecordFilePath("owner", "songs", "someone-else", "song1", a.Name); other == path {
		t.Error("two people's records share a folder")
	}
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("the kept copy went with the original")
	}
}

// Only what a browser plays or shows, starting the way its kind does, within
// the size and count a record keeps.
func TestARecordRefusesWhatItDoesNotKeep(t *testing.T) {
	recordFilesHere(t)
	for name, data := range map[string][]byte{
		"page.html":  []byte("<script>"),
		"art.svg":    []byte("<svg>"),
		"fake.png":   []byte("not a png"),
		"empty.mp3":  {},
		"fake.mp3":   []byte("hello"),
		"notpdf.pdf": []byte("hello"),
	} {
		if _, err := KeepRecordFile("owner", "songs", "u", "song1", writeSrc(t, name, data), ""); err == nil {
			t.Errorf("%s was kept", name)
		}
	}
	if _, err := KeepRecordFile("owner", "songs", "u", "song1", t.TempDir(), ""); err == nil {
		t.Error("a folder was kept")
	}
	big := writeSrc(t, "big.wav", nil)
	if err := os.Truncate(big, MaxRecordFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := KeepRecordFile("owner", "songs", "u", "song1", big, ""); err == nil || !strings.Contains(err.Error(), "MiB") {
		t.Errorf("an oversized file: %v", err)
	}
	src := writeSrc(t, "a.pdf", []byte("%PDF-1.4 x"))
	for i := 0; i < MaxRecordFiles; i++ {
		if _, err := KeepRecordFile("owner", "songs", "u", "full", src, ""); err != nil {
			t.Fatalf("file %d: %v", i, err)
		}
	}
	if _, err := KeepRecordFile("owner", "songs", "u", "full", src, ""); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("past the limit: %v", err)
	}
}

// Names and parts that could leave the folder are refused, and nothing is
// kept on a deployment with no folder for it.
func TestARecordsFilesStayInTheirFolder(t *testing.T) {
	recordFilesHere(t)
	for _, name := range []string{"../x.mp3", "a/b.mp3", `a\b.mp3`, ".hidden.mp3", "x.html", ""} {
		if _, _, err := RecordFilePath("owner", "songs", "u", "song1", name); err == nil {
			t.Errorf("%q resolved", name)
		}
	}
	for _, bad := range [][2]string{{"../o", "songs"}, {"owner", "a/b"}, {"", "songs"}, {"owner", ".."}} {
		if _, _, err := RecordFilePath(bad[0], bad[1], "u", "song1", "a.mp3"); err == nil {
			t.Errorf("owner %q slug %q resolved", bad[0], bad[1])
		}
	}
	SetRecordFilesDir("")
	if _, err := KeepRecordFile("owner", "songs", "u", "song1", writeSrc(t, "a.pdf", []byte("%PDF-1.4")), ""); err == nil {
		t.Error("kept with no folder configured")
	}
}

// A deleted record takes its files, and a deleted app every record's.
func TestDeletingARecordOrAppTakesItsFiles(t *testing.T) {
	recordFilesHere(t)
	src := writeSrc(t, "a.pdf", []byte("%PDF-1.4"))
	keep := func(user, rec string) string {
		f, err := KeepRecordFile("owner", "songs", user, rec, src, "")
		if err != nil {
			t.Fatal(err)
		}
		p, _, _ := RecordFilePath("owner", "songs", user, rec, f.Name)
		return p
	}
	one, two, three := keep("u", "song1"), keep("u", "song2"), keep("v", "song1")
	if err := DeleteRecordFiles("owner", "songs", "u", "song1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(one); !os.IsNotExist(err) {
		t.Error("the deleted record's file is still there")
	}
	if _, err := os.Stat(two); err != nil {
		t.Error("another record's file went too")
	}
	if err := DeleteAppRecordFiles("owner", "songs"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{two, three} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the app", p)
		}
	}
}
