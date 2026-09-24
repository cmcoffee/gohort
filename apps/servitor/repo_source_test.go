package servitor

// A repo appliance is cloned by git on the gohort server itself, from a URL
// any user typed. Only plain remote sources may reach that command line.

import "testing"

func TestRepoSourceRefusesWhatGitWouldRunOrReadLocally(t *testing.T) {
	for _, c := range []struct{ url, branch string }{
		{"--upload-pack=touch /tmp/x", ""},
		{"-oProxyCommand=x", ""},
		{"ext::sh -c touch% /tmp/x", ""},
		{"file:///etc", ""},
		{"/var/lib/gohort/repo", ""},
		{"../other", ""},
		{"git://host-a/repo.git", ""},
		{"http://host-a/repo.git", ""},
		{"fd::3", ""},
		{"https://host-a/repo.git\n--upload-pack=x", ""},
		{"https://-oProxyCommand=x/repo", ""},
		{"ssh://-oProxyCommand=x/repo", ""},
		{"https://host-a/repo.git", "--upload-pack=x"},
		{"https://host-a/repo.git", "main..HEAD"},
		{"https://host-a/repo.git", "a b"},
		{"", ""},
	} {
		if err := validateRepoSource(c.url, c.branch, true); err == nil {
			t.Errorf("url %q branch %q was accepted", c.url, c.branch)
		}
	}
}

func TestRepoSourceAcceptsPlainRemotes(t *testing.T) {
	for _, u := range []string{
		"https://host-a/org/repo.git",
		"https://host-a:8443/org/repo",
		"ssh://git@host-a/org/repo.git",
		"git@host-a:org/repo.git",
	} {
		if err := validateRepoSource(u, "release/1.2", true); err != nil {
			t.Errorf("%q refused for an admin owner: %v", u, err)
		}
	}
	// ssh authenticates with the server's own keys: an admin's call.
	for _, u := range []string{"ssh://git@host-a/org/repo.git", "git@host-a:org/repo.git"} {
		if err := validateRepoSource(u, "", false); err == nil {
			t.Errorf("%q accepted for a non-admin owner; it would clone with the server's ssh keys", u)
		}
	}
	if err := validateRepoSource("https://host-a/org/repo.git", "", false); err != nil {
		t.Errorf("https refused for a non-admin owner: %v", err)
	}
}
