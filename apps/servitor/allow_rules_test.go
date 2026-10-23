package servitor

import "testing"

// Always saves a rule worked out from the command: what varies from one run
// to the next becomes a placeholder, the program and its flags stay as
// written. Saving the exact command meant one naming a file or a date never
// came round again.
func TestAlwaysSuggestsARuleFromTheCommand(t *testing.T) {
	for cmd, want := range map[string]string{
		"tail -n 200 /var/log/app/2026-10-22.log":                        "tail -n <number> /var/log/app/*.log",
		`grep -i "timeout" /opt/app/logs/worker-3.log`:                   "grep -i <text> /opt/app/logs/*.log",
		"systemctl status nginx":                                         "systemctl status nginx",
		"cat /tmp/servitor-0f8fad5b-d9cb-469f-a165-70867728950e/out.txt": "cat /tmp/servitor-*/*.txt",
		"journalctl --since=2026-10-22 -u app":                           "journalctl --since=* -u app",
		"grep error app.log | head -n 5":                                 "grep error app.log | head -n <number>",
		"echo $(id)":                                                     "echo $(id)",
		"grep -c x f.log > /tmp/count.txt":                               "grep -c x f.log > /tmp/count.txt",
	} {
		if got := suggestAllowRule(cmd); got != want {
			t.Errorf("suggestAllowRule(%q) = %q, want %q", cmd, got, want)
		}
	}
}

// A rule allows only what it says: the same number of words, the program and
// any redirect target as written, a placeholder only for a plain word, never
// a word that runs something, leaves its folder, or is a path where the rule
// said text.
func TestARuleAllowsOnlyWhatItSays(t *testing.T) {
	rule := "tail -n <number> /var/log/app/*.log"
	for cmd, want := range map[string]bool{
		"tail -n 50 /var/log/app/other.log":           true,
		"tail -n 50 /var/log/app/sub/x.log":           false, // a * stays in its folder
		"tail -n 50 /var/log/app/../../etc/x.log":     false,
		"tail -n 50 /var/log/app/x.log; rm -rf /":     false, // more words, and an operator
		"tail -n $(id) /var/log/app/x.log":            false, // a word that runs something
		"tail -n abc /var/log/app/x.log":              false,
		"head -n 50 /var/log/app/x.log":               false, // the program is as written
		"tail -n 50 /var/log/app/x.txt":               false,
		"tail -n 50 /var/log/app/{x.log,/etc/shadow}": false,
	} {
		if got := matchAllowRule(rule, cmd); got != want {
			t.Errorf("rule %q on %q = %v, want %v", rule, cmd, got, want)
		}
	}
	if matchAllowRule("rm <text>", "rm /etc/passwd") {
		t.Error("<text> must not stand for a path")
	}
	if !matchAllowRule("rm <text>", "rm scratch.tmp") {
		t.Error("<text> stands for a plain word")
	}
	if matchAllowRule("<text> status nginx", "reboot status nginx") {
		t.Error("the program word is always as written")
	}
	if matchAllowRule("grep a b > /tmp/*.out", "grep a b > /tmp/x.out") {
		t.Error("a redirect's target is always as written")
	}
	if !matchAllowRule("cat a; cat b", "cat a; cat b") {
		t.Error("an exact command, operators and all, is still allowed as written")
	}
	if err := validAllowRule("tail -n <number> /var/log/*", "tail -n 200 /var/log/app/x.log"); err == nil {
		t.Error("a rule that no longer covers its command must be refused")
	}
	if err := validAllowRule(rule, "tail -n 200 /var/log/app/2026-10-22.log"); err != nil {
		t.Errorf("the suggested rule covers its command: %v", err)
	}
}

// Approval finds an Always answer saved for the exact command or a rule that
// matches it, on that system only.
func TestApprovalFindsAMatchingRule(t *testing.T) {
	saved := map[string]bool{alwaysAllowKey("sys1", "tail -n <number> /var/log/app/*.log"): true}
	keys := []string{alwaysAllowKey("sys1", "tail -n <number> /var/log/app/*.log")}
	get := func(k string) bool { return saved[k] }
	if !allowedByRule(get, keys, "sys1", "tail -n 20 /var/log/app/a.log") {
		t.Error("a command a rule matches is allowed")
	}
	if allowedByRule(get, keys, "sys2", "tail -n 20 /var/log/app/a.log") {
		t.Error("a rule is for its own system")
	}
	if allowedByRule(get, keys, "sys1", "tail -n 20 /etc/a.log") {
		t.Error("a command no rule matches still asks")
	}
}

// A rule edited in Permissions has no command to be held to, so it keeps the
// saved rule's shape: its words may narrow or widen, but not its program, its
// operators, a redirect's target, or how many words it has.
func TestAnEditedRuleKeepsItsShape(t *testing.T) {
	old := "tail -n <number> /var/log/app/*.log"
	for _, ok := range []string{"tail -n <number> /var/log/*/*.log", "tail -n 50 /var/log/app/a.log", "tail -f <number> /var/log/app/*"} {
		if err := validEditedRule(old, ok); err != nil {
			t.Errorf("%q is an edit of the words: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"rm -n <number> /var/log/app/*.log",       // another program
		"tail -n <number> /var/log/app/*.log /etc", // another word
		"tail -n <number>",                         // fewer words
		"tail -n <number> /var/log/app/*.log; rm",  // an operator added
	} {
		if err := validEditedRule(old, bad); err == nil {
			t.Errorf("%q must be refused as an edit", bad)
		}
	}
	if err := validEditedRule("grep x a > /tmp/out.txt", "grep x a > /tmp/*.txt"); err == nil {
		t.Error("a redirect's target stays as written")
	}
	if err := validEditedRule("cat a | grep b", "cat a ; grep b"); err == nil {
		t.Error("an operator stays as written")
	}
}

// The card's answer is checked against the command the card is about, and
// only while that command is waiting.
func TestACardsRuleIsCheckedAgainstItsCommand(t *testing.T) {
	cmd := "tail -n 200 /var/log/app/a.log"
	pendingCmds.Store("rule-card-sess", cmd)
	t.Cleanup(func() { pendingCmds.Delete("rule-card-sess") })
	id := confirmCardID("rule-card-sess", cmd, "n1")
	got, ok := pendingCommand(id)
	if !ok || got != cmd {
		t.Fatalf("the waiting command is found from its card: %q %v", got, ok)
	}
	if err := validAllowRule("tail -n <number> /var/log/app/*.log", got); err != nil {
		t.Errorf("a rule covering it is accepted: %v", err)
	}
	if err := validAllowRule("* -n <number> /var/log/app/*.log", got); err == nil {
		t.Error("a rule with its program wildcarded is refused")
	}
	if _, ok := pendingCommand(confirmCardID("rule-card-sess", "uptime", "n2")); ok {
		t.Error("a card for another command finds nothing")
	}
}
