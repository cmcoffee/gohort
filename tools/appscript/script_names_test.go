package appscript

import "testing"

// A traceback names the folder's files, not the staged copies at the owner's
// workspace root that the author never wrote and cannot reach.
func TestScriptPathNamesReadAsTheFolder(t *testing.T) {
	r := scriptPathNames("/srv/ws/alice", "data_local_news_news.py", "local-news", "data", "news", "py")
	in := `Traceback (most recent call last):
  File "/srv/ws/alice/data_local_news_news.py", line 230, in <module>
  File "/srv/ws/alice/.applib/local_news/feeds.py", line 12, in parse
python3: can't open file 'data_local_news_news.py'`
	want := `Traceback (most recent call last):
  File "data/news.py", line 230, in <module>
  File "lib/feeds.py", line 12, in parse
python3: can't open file 'data/news.py'`
	if got := r.Replace(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := scriptPathNames("/w", "action_x_go.sh", "x", "action", "go", "sh").Replace("/w/action_x_go.sh: line 3"); got != "actions/go.sh: line 3" {
		t.Errorf("action script read as %q", got)
	}
}
