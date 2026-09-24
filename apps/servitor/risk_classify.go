package servitor

// The risk classifier, rebuilt to fail CLOSED.
//
// The old one was a denylist: it knew the commands that were dangerous and
// called everything else read-only. A program it had never heard of, a line
// it could not parse, an option value sitting where it expected the program
// ("sudo -u root rm -rf /" read as the program "root"), a substitution, a
// heredoc into a shell, "systemctl restart", "kubectl apply" - all of them
// came back RiskNone and ran without a prompt.
//
// Now the question is the other way round. A line runs without asking only
// when every simple command in it is POSITIVELY recognized as read-only (or as
// a write confined to the run's scratch directory). A known risky command gets
// its category as before. Anything else - an unknown program, a script or
// interpreter the gate cannot read, shell syntax it cannot see through - is
// RiskUnverified, which prompts like any other category and can be granted
// like any other category by an operator who wants the old behaviour back.
//
// Every risk found on a line is kept (see assess_command), so a grant has to
// cover ALL of them: "rm x; python3 y" needs both file_delete and unverified.

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// risk_hit is one reason a command line is not read-only.
type risk_hit struct {
	cat    RiskCategory
	reason string
}

// risk_ctx accumulates the risks found while walking one line.
type risk_ctx struct {
	scratch string
	hits    []risk_hit
	depth   int
}

// max_shell_depth bounds recursion through bash -c / $(...) / sudo sh -c.
const max_shell_depth = 8

// assess_command returns every risk in cmd, one per category (the first reason
// found for each), highest-ranked first. Empty means the whole line is
// read-only or confined to scratch.
func assess_command(cmd, scratch string) []risk_hit {
	rc := &risk_ctx{scratch: scratch}
	rc.line(cmd)
	sort.SliceStable(rc.hits, func(i, j int) bool {
		return riskRank(rc.hits[i].cat) > riskRank(rc.hits[j].cat)
	})
	return rc.hits
}

// classify_command returns the risk CATEGORY of cmd with no scratch directory
// in play - every write outside a /dev sink is gated. Callers that own a run
// scratch directory should use classify_command_scoped.
func classify_command(cmd string) (RiskCategory, string) {
	return classify_command_scoped(cmd, "")
}

// classify_command_scoped returns the highest-ranked risk in cmd (RiskNone
// when there is none) plus a short human reason.
//
// scratch is the run's private scratch directory (see scratch_dir). Writing,
// redirecting into and deleting inside it is ungated, because that is the
// sanctioned place to work and gating the cleanup is what leaves artifacts
// behind on the host. Outside it, a redirect or tee that lands on a real file
// is an overwrite and gates the same as an explicit delete.
func classify_command_scoped(cmd, scratch string) (RiskCategory, string) {
	hits := assess_command(cmd, scratch)
	if len(hits) == 0 {
		return RiskNone, ""
	}
	return hits[0].cat, hits[0].reason
}

// risk_needing_approval returns the highest-ranked hit whose category allowed
// does not cover, or RiskNone when every risk on the line is covered. A grant
// for one category must never carry a second, ungranted one through with it.
func risk_needing_approval(hits []risk_hit, allowed func(RiskCategory) bool) (RiskCategory, string) {
	for _, h := range hits {
		if allowed == nil || !allowed(h.cat) {
			return h.cat, h.reason
		}
	}
	return RiskNone, ""
}

// hit_categories names the categories in hits, for status lines.
func hit_categories(hits []risk_hit) string {
	var names []string
	for _, h := range hits {
		names = append(names, string(h.cat))
	}
	return strings.Join(names, "+")
}

func (rc *risk_ctx) flag(cat RiskCategory, reason string) {
	for _, h := range rc.hits {
		if h.cat == cat {
			return
		}
	}
	rc.hits = append(rc.hits, risk_hit{cat: cat, reason: reason})
}

func (rc *risk_ctx) unverified(reason string) { rc.flag(RiskUnverified, reason) }

// line classifies a whole shell line, including the bodies of substitutions.
func (rc *risk_ctx) line(s string) {
	if rc.depth >= max_shell_depth {
		rc.unverified("nests commands deeper than the gate follows")
		return
	}
	rc.depth++
	defer func() { rc.depth-- }()
	p := sh_lex(s)
	if p.opaque != "" {
		rc.unverified("the gate cannot read this line: it " + p.opaque)
	}
	for _, n := range p.nested {
		rc.line(n)
	}
	for i := range p.cmds {
		rc.command(&p.cmds[i])
	}
}

// shell_keywords open or close compound commands; the program follows them.
var shell_keywords = map[string]bool{
	"!": true, "{": true, "}": true, "if": true, "then": true, "else": true, "elif": true,
	"fi": true, "do": true, "done": true, "while": true, "until": true, "esac": true,
}

// command classifies one simple command.
func (rc *risk_ctx) command(c *sh_command) {
	for _, r := range c.redirects {
		rc.redirect(r)
	}
	words := c.words
	for len(words) > 0 && words[0].raw == words[0].text && shell_keywords[words[0].text] {
		words = words[1:]
	}
	if len(words) > 0 && words[0].raw == words[0].text {
		switch words[0].text {
		case "for", "select", "case":
			// A loop or case header runs nothing itself; its word list was
			// already searched for substitutions.
			return
		}
	}
	for len(words) > 0 && is_assignment(words[0]) {
		rc.assignment(words[0])
		words = words[1:]
	}
	rc.invoke(words, c, false)
}

// safe_env_names are the variables a command may be given inline without the
// gate caring. Anything else can change WHICH code runs (PATH, LD_PRELOAD,
// BASH_ENV, PYTHONPATH, GIT_SSH_COMMAND, PAGER, ...), so it is not read-only
// however harmless the command after it looks.
var safe_env_names = map[string]bool{
	"LANG": true, "LANGUAGE": true, "TZ": true, "TERM": true, "COLUMNS": true, "LINES": true,
	"NO_COLOR": true, "SYSTEMD_COLORS": true, "SYSTEMD_LESS": true,
	"PGPASSWORD": true, "PGHOST": true, "PGPORT": true, "PGUSER": true, "PGDATABASE": true,
	"MYSQL_PWD": true, "REDISCLI_AUTH": true, "KUBECONFIG": true,
}

// assignment gates a NAME=value word. A lowercase name is a shell-local
// variable by convention - programs read their settings from UPPERCASE names -
// so "pid=$(pgrep x)" stays free. The proxy variables are the lowercase
// exception: curl and friends honour them.
func (rc *risk_ctx) assignment(w sh_word) {
	name := assignment_name(w)
	if safe_env_names[name] || strings.HasPrefix(name, "LC_") {
		return
	}
	if strings.ToLower(name) == name && !strings.HasSuffix(name, "_proxy") {
		return
	}
	rc.unverified("sets " + name + ", which can change what a command runs (a lowercase shell variable would not)")
}

// write_sinks are redirect targets that discard output or route it back to the
// caller's own streams rather than landing on a real file.
var write_sinks = map[string]bool{
	"/dev/null": true, "/dev/stdout": true, "/dev/stderr": true,
	"/dev/tty": true, "/dev/fd/1": true, "/dev/fd/2": true,
}

func (rc *risk_ctx) redirect(r sh_redirect) {
	switch r.op {
	case "<", "<<", "<<-", "<<<", "<&":
		return // reads
	case ">&":
		// ">&2" / "2>&1" duplicate a descriptor; ">&-" closes one. Anything
		// else is the csh spelling of "&>": a file write.
		if isAllDigits(r.target.text) || r.target.text == "-" {
			return
		}
	}
	rc.write(r.target, strings.Contains(r.op, ">>"))
}

// write gates one file the command writes to.
func (rc *risk_ctx) write(target sh_word, appends bool) {
	if !target.expanded && !target.glob && write_sinks[path.Clean(target.text)] {
		return
	}
	if rc.in_scratch(target) {
		return
	}
	verb := "overwrites"
	if appends {
		verb = "appends to"
	}
	rc.flag(RiskFileDelete, fmt.Sprintf("redirect %s a file outside the scratch directory: %s", verb, target.raw))
}

// in_scratch reports whether w is a path the gate can vouch for as inside the
// run's scratch directory. Only an absolute, literal path qualifies: "..",
// variables and substitutions are refused outright, because the gate can only
// judge the text it sees. A glob is allowed in the last component alone (it
// cannot climb out), unless that component could match a dotfile.
func (rc *risk_ctx) in_scratch(w sh_word) bool {
	if rc.scratch == "" || w.expanded {
		return false
	}
	p := w.text
	if !strings.HasPrefix(p, "/") {
		return false
	}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if seg == ".." {
			return false
		}
		if w.glob && strings.ContainsAny(seg, "*?[]{}~") && (i != len(segs)-1 || strings.HasPrefix(seg, ".") || strings.ContainsAny(seg, "{}~")) {
			return false
		}
	}
	return under_scratch(p, rc.scratch)
}

// all_in_scratch reports whether ws is non-empty and every entry is inside the
// scratch directory. An empty list means no target could be identified, which
// is never treated as safe.
func (rc *risk_ctx) all_in_scratch(ws []sh_word) bool {
	if len(ws) == 0 {
		return false
	}
	for _, w := range ws {
		if !rc.in_scratch(w) {
			return false
		}
	}
	return true
}

// under_scratch reports whether p resolves inside the run's scratch directory.
// An empty scratch matches nothing.
func under_scratch(p, scratch string) bool {
	if scratch == "" || p == "" {
		return false
	}
	c := path.Clean(p)
	s := path.Clean(scratch)
	return c == s || strings.HasPrefix(c, s+"/")
}

// system_bin_dirs are the directories a program may be named from by path and
// still be judged by its name. "/tmp/x/ls" is not ls: it is whatever somebody
// put there, and the gate cannot vouch for it.
var system_bin_dirs = map[string]bool{
	"/bin": true, "/sbin": true, "/usr/bin": true, "/usr/sbin": true,
	"/usr/local/bin": true, "/usr/local/sbin": true,
}

var versioned_name = regexp.MustCompile(`^(python|pip|perl|ruby|php|node|lua)[0-9][0-9.]*$`)

// program_name resolves the command word to the name the rules know it by.
func program_name(word string) (string, bool) {
	if word == "" {
		return "", false
	}
	name := word
	if strings.Contains(word, "/") {
		if !system_bin_dirs[path.Dir(word)] {
			return "", false
		}
		name = path.Base(word)
	}
	// python3.11 -> python, pip3 -> pip, nodejs -> node: same rules.
	if m := versioned_name.FindStringSubmatch(name); m != nil {
		name = m[1]
	}
	if name == "nodejs" {
		name = "node"
	}
	return name, true
}

// invocation is one program with its arguments.
type invocation struct {
	name string
	args []sh_word
	cmd  *sh_command
	// more: xargs or find -exec append operands the gate never sees.
	more bool
}

// invoke classifies words as a command: program first, then its arguments.
func (rc *risk_ctx) invoke(words []sh_word, c *sh_command, more bool) {
	if len(words) == 0 {
		return
	}
	w := words[0]
	if w.glob && (w.raw == "[" || w.raw == "[[") {
		w.glob = false // the test builtin, not a pattern
	}
	if w.expanded || w.glob {
		rc.unverified("the program name is only decided at run time: " + w.raw)
		return
	}
	name, ok := program_name(w.text)
	if !ok {
		rc.unverified("runs a program by a path the gate cannot vouch for: " + w.text)
		return
	}
	inv := &invocation{name: name, args: words[1:], cmd: c, more: more}
	if rule, ok := risk_rules[name]; ok {
		rule(rc, inv)
		return
	}
	if read_only_cmds[name] {
		return
	}
	rc.unverified("not a command the gate recognizes as read-only: " + name)
}

// stdin_fed reports whether the command's stdin carries data the caller chose:
// a pipe, a heredoc, a herestring or a file.
func (inv *invocation) stdin_fed() bool {
	return inv.cmd != nil && (inv.cmd.piped_in || inv.cmd.has_stdin)
}

// parsed_args splits a command's arguments into options and operands.
type parsed_args struct {
	flags  []string           // every option as written
	values map[string]sh_word // option -> value, for options that take one
	ops    []sh_word          // operands
}

// parse_args reads options GNU-style (they may follow operands; "--" ends
// them). takes_value names the options whose value is the next word, or the
// rest of the same word for a short option ("-ofile").
func parse_args(args []sh_word, takes_value ...string) parsed_args {
	tv := map[string]bool{}
	for _, t := range takes_value {
		tv[t] = true
	}
	pa := parsed_args{values: map[string]sh_word{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		t := a.text
		if t == "--" {
			pa.ops = append(pa.ops, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(t, "-") || t == "-" {
			pa.ops = append(pa.ops, a)
			continue
		}
		pa.flags = append(pa.flags, t)
		if strings.HasPrefix(t, "--") {
			if eq := strings.IndexByte(t, '='); eq > 0 {
				v := a
				v.text = t[eq+1:]
				pa.values[t[:eq]] = v
				continue
			}
			if tv[t] && i+1 < len(args) {
				pa.values[t] = args[i+1]
				i++
			}
			continue
		}
		short := t[:2]
		if tv[short] {
			if len(t) > 2 {
				v := a
				v.text = t[2:]
				pa.values[short] = v
			} else if i+1 < len(args) {
				pa.values[short] = args[i+1]
				i++
			}
			continue
		}
		if tv[t] && i+1 < len(args) {
			pa.values[t] = args[i+1]
			i++
		}
	}
	return pa
}

// has reports whether any of the named options was given, in any of its
// forms: exact, "--opt=value", or (for a single-letter "-x") bundled as in
// "-xvf" - bundling only counts when the letter is not the value of an
// earlier option in the bundle, which the callers accept as a fail-closed
// approximation.
func (pa parsed_args) has(names ...string) bool {
	for _, f := range pa.flags {
		for _, n := range names {
			if f == n || strings.HasPrefix(f, n+"=") {
				return true
			}
			if len(n) == 2 && n[0] == '-' && n[1] != '-' && len(f) > 2 && f[0] == '-' && f[1] != '-' && strings.IndexByte(f[1:], n[1]) >= 0 {
				return true
			}
		}
	}
	return false
}

// has_prefix reports whether any option starts with p.
func (pa parsed_args) has_prefix(p string) bool {
	for _, f := range pa.flags {
		if strings.HasPrefix(f, p) {
			return true
		}
	}
	return false
}

// value returns the value given to the first of names that has one.
func (pa parsed_args) value(names ...string) (sh_word, bool) {
	for _, n := range names {
		if v, ok := pa.values[n]; ok {
			return v, true
		}
	}
	return sh_word{}, false
}

func op_texts(ws []sh_word) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.text
	}
	return out
}

// verb returns the first operand (the sub-command of a multiplexer), or "".
func (pa parsed_args) verb() string {
	if len(pa.ops) == 0 {
		return ""
	}
	return pa.ops[0].text
}

// read_only_cmds only read, report or compute: nothing they do survives them.
// A program with options that write is NOT here - it has a rule instead.
var read_only_cmds = set_of(
	"cat", "tac", "head", "tail", "less", "more", "grep", "egrep", "fgrep", "zgrep", "zegrep", "zfgrep",
	"zcat", "zless", "zmore", "bzcat", "xzcat", "zstdcat", "lz4cat", "rg", "ag",
	"ls", "dir", "vdir", "stat", "file", "wc", "cut", "tr", "column", "nl", "od", "hexdump", "strings",
	"diff", "cmp", "comm", "md5sum", "sha1sum", "sha224sum", "sha256sum", "sha384sum", "sha512sum",
	"b2sum", "cksum", "sum", "base64", "base32", "jq", "fold", "fmt", "expand", "unexpand", "paste",
	"join", "rev", "shuf", "numfmt", "look", "iconv",
	"locate", "which", "whereis", "type", "whoami", "id", "groups", "uname", "uptime", "cal", "w", "who",
	"users", "last", "lastb", "lastlog", "ps", "pgrep", "pidof", "pstree", "free", "vmstat", "iostat",
	"mpstat", "pidstat", "df", "du", "lsblk", "blkid", "lscpu", "lsmem", "lspci", "lsusb", "lsmod",
	"lsof", "lsns", "lsipc", "lslocks", "lslogins", "lsscsi", "ss", "netstat", "nstat",
	"ping", "ping6", "traceroute", "traceroute6", "tracepath", "mtr", "dig", "nslookup", "host",
	"getent", "printenv", "echo", "printf", "true", "false", "test", "[", "[[", "sleep", "yes", "seq",
	"basename", "dirname", "realpath", "readlink", "pwd", "cd", "pushd", "popd", "dirs", "nproc", "arch",
	"tty", "locale", "getconf", "tree", "findmnt", "mountpoint", "namei", "lsattr", "getfacl",
	"getenforce", "sestatus", "aa-status", "ausearch", "aureport", "top", "htop", "iotop", "iftop",
	"vnstat", "systemd-analyze", "systemd-cgls", "systemd-cgtop", "systemd-detect-virt", "virt-what",
	"dmidecode", "lshw", "hwinfo", "inxi", "sensors", "iptables-save", "ip6tables-save", "runlevel",
	"lvs", "vgs", "pvs", "lvdisplay", "vgdisplay", "pvdisplay", "lvscan", "vgscan", "pvscan",
	"named-checkconf", "named-checkzone", "showmount", "atq", "whatis", "apropos", "man", "help",
	"dpkg-query", "apt-cache", "set", "unset", "shopt", "umask", "ulimit", "read", "wait", "jobs",
	"exit", "return", "logout", ":", "history", "hash", "let", "shift", "getopts", "local", "declare",
	"typeset", "readonly", "alias", "unalias", "clear", "reset", "tput", "stty", "sync", "env-update-check",
)

func set_of(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// net_egress_cmds make an outbound connection FROM the appliance - the vector
// for pulling in code or exfiltrating data.
var net_egress_cmds = set_of(
	"curl", "wget", "nc", "ncat", "netcat", "telnet", "ssh", "scp", "sftp", "rsync",
	"ftp", "lftp", "tftp", "socat", "aria2c", "http", "https", "lynx", "links", "w3m",
	"nmap", "masscan", "ssh-keyscan", "ldapsearch", "snmpwalk", "snmpget", "snmpbulkwalk",
	"smbclient", "mosquitto_pub", "mosquitto_sub", "kcat", "grpcurl", "xh", "websocat",
)

// sys_control_cmds stop/kill services, reboot the host, or reconfigure
// users/kernel - control-plane changes, not file or database data.
var sys_control_cmds = set_of(
	"kill", "killall", "pkill", "skill", "killall5", "shutdown", "reboot", "halt", "poweroff",
	"init", "telinit", "userdel", "deluser", "groupdel", "delgroup", "useradd", "adduser",
	"groupadd", "addgroup", "gpasswd", "passwd", "chpasswd", "usermod", "groupmod", "chsh", "chfn",
	"vipw", "vigr", "visudo", "sudoedit", "insmod", "rmmod", "modprobe", "depmod", "renice",
	"umount", "swapoff", "ifup", "ifdown", "dhclient", "update-grub", "grub-install",
	"grub2-mkconfig", "dracut", "mkinitramfs", "update-initramfs", "setenforce", "semanage",
	"restorecon", "setsebool", "aa-enforce", "aa-complain", "aa-disable", "apparmor_parser",
	"ntpdate", "exportfs", "quotaon", "quotaoff", "setquota", "atrm", "wall",
)

// risk_rule classifies one invocation of a known program.
type risk_rule func(rc *risk_ctx, inv *invocation)

// risk_rules is filled in init: the rules call rc.invoke, which reads this map,
// and a package-level initializer that did so would be an initialization cycle.
var risk_rules map[string]risk_rule

func init() {
	risk_rules = map[string]risk_rule{}
	for n := range net_egress_cmds {
		risk_rules[n] = func(rc *risk_ctx, inv *invocation) {
			rc.flag(RiskNetEgress, "outbound call from the appliance: "+inv.name)
		}
	}
	for n := range sys_control_cmds {
		risk_rules[n] = func(rc *risk_ctx, inv *invocation) {
			rc.flag(RiskSysControl, "stops/reconfigures the host: "+inv.name)
		}
	}
	register_wrapper_rules()
	register_file_rules()
	register_text_rules()
	register_system_rules()
	register_platform_rules()
	register_package_rules()
	register_data_rules()
	register_interpreter_rules()
}

// ---------------------------------------------------------------- wrappers

// register_wrapper_rules: programs that run ANOTHER program. The wrapped
// command is what gets classified; an option the gate does not know could be
// the one that changes that, so it fails closed.
func register_wrapper_rules() {
	risk_rules["sudo"] = func(rc *risk_ctx, inv *invocation) {
		i := 0
		shell := false
		for i < len(inv.args) {
			t := inv.args[i].text
			if t == "--" {
				i++
				break
			}
			if !strings.HasPrefix(t, "-") {
				break
			}
			switch {
			case t == "-u" || t == "-g" || t == "-h" || t == "-p" || t == "-C" || t == "-D" ||
				t == "-r" || t == "-t" || t == "-T" || t == "-U" || t == "-R":
				i += 2
				continue
			case strings.HasPrefix(t, "--user=") || strings.HasPrefix(t, "--group=") ||
				strings.HasPrefix(t, "--host=") || strings.HasPrefix(t, "--prompt=") ||
				strings.HasPrefix(t, "--close-from=") || strings.HasPrefix(t, "--chdir=") ||
				strings.HasPrefix(t, "--preserve-env="):
			case t == "-e" || t == "--edit":
				rc.flag(RiskFileDelete, "sudo -e edits files as root")
				return
			case t == "-s" || t == "--shell" || t == "-i" || t == "--login":
				shell = true
			case t == "-l" || t == "--list" || t == "-v" || t == "--validate" || t == "-k" ||
				t == "--reset-timestamp" || t == "-K" || t == "--remove-timestamp" || t == "-V" || t == "--version":
				if i+1 >= len(inv.args) {
					return // lists or refreshes privileges; runs nothing
				}
			case t == "-A" || t == "-b" || t == "-E" || t == "-H" || t == "-n" || t == "-P" || t == "-S" ||
				t == "--askpass" || t == "--background" || t == "--preserve-env" || t == "--set-home" ||
				t == "--non-interactive" || t == "--preserve-groups" || t == "--stdin":
			default:
				rc.unverified("sudo option the gate does not know: " + t)
				return
			}
			i++
		}
		if i >= len(inv.args) {
			if shell {
				return // an interactive root shell: what is typed into it is gated line by line
			}
			return
		}
		rc.invoke(inv.args[i:], inv.cmd, inv.more)
	}
	risk_rules["doas"] = func(rc *risk_ctx, inv *invocation) {
		i := 0
		for i < len(inv.args) && strings.HasPrefix(inv.args[i].text, "-") {
			switch inv.args[i].text {
			case "-u", "-C":
				i += 2
				continue
			case "-n", "-s", "-L":
			default:
				rc.unverified("doas option the gate does not know: " + inv.args[i].text)
				return
			}
			i++
		}
		rc.invoke(inv.args[min(i, len(inv.args)):], inv.cmd, inv.more)
	}
	risk_rules["env"] = func(rc *risk_ctx, inv *invocation) {
		args := inv.args
		i := 0
		for i < len(args) {
			t := args[i].text
			if t == "--" {
				i++
				break
			}
			if is_assignment(args[i]) {
				rc.assignment(args[i])
				i++
				continue
			}
			if !strings.HasPrefix(t, "-") {
				break
			}
			switch {
			case t == "-i" || t == "--ignore-environment" || t == "-0" || t == "--null" || t == "-v" ||
				t == "--debug" || t == "-" || strings.HasPrefix(t, "--unset=") || strings.HasPrefix(t, "--chdir="):
			case t == "-u" || t == "--unset" || t == "-C" || t == "--chdir":
				i++
			case t == "-S" || t == "--split-string":
				if i+1 < len(args) {
					rc.line(args[i+1].text)
				}
				return
			case strings.HasPrefix(t, "--split-string="):
				rc.line(strings.TrimPrefix(t, "--split-string="))
				return
			default:
				rc.unverified("env option the gate does not know: " + t)
				return
			}
			i++
		}
		rc.invoke(args[min(i, len(args)):], inv.cmd, inv.more)
	}
	// Wrappers whose options never carry the command.
	simple_wrapper := func(value_opts ...string) risk_rule {
		tv := set_of(value_opts...)
		return func(rc *risk_ctx, inv *invocation) {
			i := 0
			for i < len(inv.args) {
				t := inv.args[i].text
				if t == "--" {
					i++
					break
				}
				if !strings.HasPrefix(t, "-") || t == "-" {
					break
				}
				if tv[t] {
					i++
				}
				i++
			}
			rc.invoke(inv.args[min(i, len(inv.args)):], inv.cmd, inv.more)
		}
	}
	risk_rules["nice"] = simple_wrapper("-n", "--adjustment")
	risk_rules["nohup"] = simple_wrapper()
	risk_rules["stdbuf"] = simple_wrapper("-i", "-o", "-e")
	risk_rules["setsid"] = simple_wrapper()
	risk_rules["unbuffer"] = simple_wrapper()
	risk_rules["builtin"] = simple_wrapper()
	risk_rules["exec"] = simple_wrapper("-a")
	risk_rules["busybox"] = simple_wrapper()
	risk_rules["ionice"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-c", "-n", "-p", "-P", "-u", "--class", "--classdata", "--pid", "--pgid", "--uid")
		if _, ok := pa.value("-p", "-P", "-u", "--pid", "--pgid", "--uid"); ok {
			rc.flag(RiskSysControl, "ionice changes a running process's I/O priority")
			return
		}
		simple_wrapper("-c", "-n", "--class", "--classdata")(rc, inv)
	}
	risk_rules["timeout"] = func(rc *risk_ctx, inv *invocation) {
		i := 0
		for i < len(inv.args) && strings.HasPrefix(inv.args[i].text, "-") {
			switch inv.args[i].text {
			case "-k", "-s", "--kill-after", "--signal":
				i++
			}
			i++
		}
		i++ // the duration
		if i < len(inv.args) {
			rc.invoke(inv.args[i:], inv.cmd, inv.more)
		}
	}
	risk_rules["time"] = func(rc *risk_ctx, inv *invocation) {
		i := 0
		for i < len(inv.args) && strings.HasPrefix(inv.args[i].text, "-") {
			switch t := inv.args[i].text; {
			case t == "-o" || t == "--output":
				if i+1 < len(inv.args) {
					rc.write(inv.args[i+1], false)
				}
				i++
			case t == "-f" || t == "--format":
				i++
			case strings.HasPrefix(t, "--output="):
				w := inv.args[i]
				w.text = strings.TrimPrefix(t, "--output=")
				rc.write(w, false)
			}
			i++
		}
		rc.invoke(inv.args[min(i, len(inv.args)):], inv.cmd, inv.more)
	}
	risk_rules["command"] = func(rc *risk_ctx, inv *invocation) {
		i := 0
		for i < len(inv.args) && strings.HasPrefix(inv.args[i].text, "-") {
			switch inv.args[i].text {
			case "-v", "-V":
				return // looks a name up; runs nothing
			}
			i++
		}
		rc.invoke(inv.args[min(i, len(inv.args)):], inv.cmd, inv.more)
	}
	risk_rules["watch"] = func(rc *risk_ctx, inv *invocation) {
		i := 0
		exec_form := false
		for i < len(inv.args) && strings.HasPrefix(inv.args[i].text, "-") {
			switch t := inv.args[i].text; t {
			case "-n", "--interval", "-q", "--equexit":
				i++
			case "-x", "--exec":
				exec_form = true
			}
			i++
		}
		rest := inv.args[min(i, len(inv.args)):]
		if exec_form {
			rc.invoke(rest, inv.cmd, inv.more)
			return
		}
		// Without -x, watch hands its arguments to sh -c as one string.
		rc.line(strings.Join(op_texts(rest), " "))
	}
	risk_rules["xargs"] = func(rc *risk_ctx, inv *invocation) {
		value_opts := set_of("-I", "-L", "-l", "-n", "-P", "-s", "-d", "-E", "-e", "-a", "--arg-file",
			"--delimiter", "--eof", "--max-lines", "--max-args", "--max-procs", "--max-chars", "--replace", "--process-slot-var")
		i := 0
		for i < len(inv.args) {
			t := inv.args[i].text
			if t == "--" {
				i++
				break
			}
			if !strings.HasPrefix(t, "-") {
				break
			}
			if value_opts[t] {
				i++
			}
			i++
		}
		rest := inv.args[min(i, len(inv.args)):]
		if len(rest) == 0 {
			return // xargs alone runs echo
		}
		rc.invoke(rest, inv.cmd, true)
	}
	for _, n := range []string{"su", "runuser"} {
		risk_rules[n] = func(rc *risk_ctx, inv *invocation) {
			pa := parse_args(inv.args, "-c", "--command", "-s", "--shell", "-g", "--group", "-G", "-w", "--whitelist-environment")
			if c, ok := pa.value("-c", "--command"); ok {
				rc.line(c.text)
				return
			}
			if _, ok := pa.value("-s", "--shell"); ok {
				rc.unverified(inv.name + " with a chosen shell")
				return
			}
			// No -c: an interactive shell, whose input is gated line by line.
		}
	}
	for _, n := range []string{"chroot", "nsenter", "unshare", "systemd-run", "taskset", "chrt", "flock",
		"numactl", "script", "at", "batch", "strace", "ltrace", "gdb", "perf", "valgrind", "firejail"} {
		risk_rules[n] = func(rc *risk_ctx, inv *invocation) {
			rc.unverified(inv.name + " runs or attaches to another program in a way the gate does not follow")
		}
	}
}

// ---------------------------------------------------------------- files

// register_file_rules: commands that create, change or remove files. Each is
// free when every file it touches is inside the scratch directory, and a
// file_delete otherwise.
func register_file_rules() {
	// all_operands: every operand is a path the command changes.
	all_operands := func(skip_first bool, value_opts ...string) risk_rule {
		return func(rc *risk_ctx, inv *invocation) {
			pa := parse_args(inv.args, value_opts...)
			ops := pa.ops
			if skip_first && len(ops) > 0 && !pa.has_prefix("--reference") {
				ops = ops[1:] // chmod's mode, chown's owner
			}
			if inv.more || !rc.all_in_scratch(ops) {
				rc.flag(RiskFileDelete, "deletes or overwrites files: "+inv.name)
			}
		}
	}
	for _, n := range []string{"rm", "rmdir", "unlink", "shred", "wipe", "mkdir", "touch", "mkfifo", "mknod"} {
		risk_rules[n] = all_operands(false, "-m", "--mode", "-d", "--date", "-r", "--reference", "-t", "-n", "--iterations", "-s", "--size", "-Z", "--context")
	}
	risk_rules["mv"] = all_operands(false, "-t", "--target-directory", "-S", "--suffix")
	risk_rules["ln"] = all_operands(false, "-t", "--target-directory", "-S", "--suffix")
	risk_rules["truncate"] = all_operands(false, "-s", "--size", "-r", "--reference")
	for _, n := range []string{"chmod", "chown", "chgrp", "chattr", "chcon"} {
		risk_rules[n] = all_operands(true, "-R", "--from", "-v", "-p")
	}
	risk_rules["setfacl"] = all_operands(false, "-m", "-M", "-x", "-X", "--modify", "--modify-file", "--remove", "--remove-file", "--set", "--set-file")
	for _, n := range []string{"mkfs", "mke2fs", "mkntfs", "mkswap", "fdisk", "gdisk", "parted", "sfdisk", "cfdisk", "wipefs", "blkdiscard", "rename", "patch"} {
		n := n
		risk_rules[n] = func(rc *risk_ctx, inv *invocation) {
			if n == "patch" && parse_args(inv.args).has("--dry-run") {
				return
			}
			rc.flag(RiskFileDelete, "deletes or overwrites files: "+n)
		}
	}
	for _, n := range []string{"mkfs.ext4", "mkfs.xfs", "mkfs.ext3", "mkfs.vfat", "mkfs.btrfs"} {
		risk_rules[n] = risk_rules["mkfs"]
	}
	// cp and install write their LAST operand (or the -t directory).
	last_target := func(rc *risk_ctx, inv *invocation, links bool) {
		pa := parse_args(inv.args, "-t", "--target-directory", "-S", "--suffix", "-m", "--mode", "-o", "--owner", "-g", "--group")
		var target []sh_word
		if t, ok := pa.value("-t", "--target-directory"); ok {
			target = []sh_word{t}
		} else if len(pa.ops) > 0 {
			target = pa.ops[len(pa.ops)-1:]
		}
		if inv.more && len(target) == 0 {
			rc.flag(RiskFileDelete, "copies files onto targets the gate cannot see: "+inv.name)
			return
		}
		if !rc.all_in_scratch(target) {
			rc.flag(RiskFileDelete, "deletes or overwrites files: "+inv.name)
			return
		}
		// A copy that keeps symlinks can plant one in the scratch directory
		// pointing out of it; the next "harmless" write into scratch would
		// then land wherever it points.
		if links && (pa.has("-a", "-d", "-P", "-R", "-r", "-s", "--archive", "--no-dereference", "--recursive", "--symbolic-link") ||
			pa.has_prefix("--preserve")) && !pa.has("-L", "--dereference") {
			rc.flag(RiskFileDelete, "copies symlinks into the scratch directory (use cp -rL to copy what they point at)")
		}
	}
	risk_rules["cp"] = func(rc *risk_ctx, inv *invocation) { last_target(rc, inv, true) }
	risk_rules["install"] = func(rc *risk_ctx, inv *invocation) { last_target(rc, inv, false) }
	risk_rules["tee"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "--output-error")
		for _, w := range pa.ops {
			rc.write(w, pa.has("-a", "--append"))
		}
	}
	risk_rules["dd"] = func(rc *risk_ctx, inv *invocation) {
		for _, a := range inv.args {
			if strings.HasPrefix(a.text, "of=") {
				w := a
				w.text = strings.TrimPrefix(a.text, "of=")
				if !rc.in_scratch(w) && (w.expanded || !write_sinks[path.Clean(w.text)]) {
					rc.flag(RiskFileDelete, "deletes or overwrites files: dd")
				}
			}
		}
	}
	risk_rules["sort"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-o", "--output", "-k", "--key", "-t", "--field-separator", "-T", "--temporary-directory", "-S", "--buffer-size", "--batch-size", "--parallel", "--files0-from", "--random-source")
		if pa.has_prefix("--compress-program") {
			rc.unverified("sort --compress-program runs another program")
		}
		if o, ok := pa.value("-o", "--output"); ok {
			rc.write(o, false)
		}
	}
	risk_rules["uniq"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-f", "-s", "-w", "--skip-fields", "--skip-chars", "--check-chars", "--group", "--all-repeated")
		if len(pa.ops) > 1 {
			rc.write(pa.ops[1], false) // uniq IN OUT writes OUT
		}
	}
	risk_rules["xxd"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-c", "-g", "-l", "-o", "-s", "-n", "-C", "-cols", "-len", "-seek")
		if len(pa.ops) > 1 {
			rc.write(pa.ops[1], false)
		}
	}
	risk_rules["split"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-a", "-b", "-C", "-l", "-n", "-t", "--additional-suffix", "--filter")
		if pa.has_prefix("--filter") {
			rc.unverified("split --filter runs a shell command")
			return
		}
		if len(pa.ops) < 2 || !rc.in_scratch(pa.ops[1]) {
			rc.flag(RiskFileDelete, "split writes files outside the scratch directory")
		}
	}
	risk_rules["csplit"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-f", "--prefix", "-b", "--suffix-format", "-n", "--digits")
		if p, ok := pa.value("-f", "--prefix"); !ok || !rc.in_scratch(p) {
			rc.flag(RiskFileDelete, "csplit writes files outside the scratch directory")
		}
	}
	risk_rules["tar"] = tar_rule
	for _, n := range []string{"gzip", "gunzip", "bzip2", "bunzip2", "xz", "unxz", "zstd", "unzstd", "lz4", "lzma", "unlzma", "compress", "uncompress"} {
		risk_rules[n] = func(rc *risk_ctx, inv *invocation) {
			pa := parse_args(inv.args, "-S", "--suffix", "-o", "-b", "-T", "--threads")
			if pa.has("-c", "--stdout", "--to-stdout", "-l", "--list", "-t", "--test") {
				return
			}
			if o, ok := pa.value("-o"); ok {
				rc.write(o, false)
				return
			}
			if len(pa.ops) == 0 && !inv.more {
				return // stdin to stdout
			}
			if inv.more || !rc.all_in_scratch(pa.ops) {
				rc.flag(RiskFileDelete, inv.name+" replaces files in place")
			}
		}
	}
	risk_rules["unzip"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-d", "-x", "-P")
		if pa.has("-l", "-t", "-v", "-Z", "-p", "-c", "-z") {
			return
		}
		rc.flag(RiskFileDelete, "unzip extracts files (and can create symlinks)")
	}
	risk_rules["zip"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-b", "-n", "-t", "-tt", "-x", "-i", "-P", "-O", "--out", "-TT", "--unzip-command")
		if pa.has("-TT", "--unzip-command") {
			rc.unverified("zip -TT runs another program")
			return
		}
		if pa.has("-m", "--move") {
			rc.flag(RiskFileDelete, "zip -m deletes the files it archives")
			return
		}
		if len(pa.ops) == 0 || !rc.in_scratch(pa.ops[0]) {
			rc.flag(RiskFileDelete, "zip writes an archive outside the scratch directory")
		}
	}
	risk_rules["find"] = find_rule
}

// tar_rule: listing is a read; creating is a write to the archive; extracting
// can place files and symlinks anywhere, so it always asks.
func tar_rule(rc *risk_ctx, inv *invocation) {
	args := op_texts(inv.args)
	mode := ""
	file := ""
	have_file := false
	danger := ""
	set_mode := func(m string) {
		if mode == "" {
			mode = m
		}
	}
	letters := func(s string) {
		for _, c := range s {
			switch c {
			case 't':
				set_mode("t")
			case 'x':
				set_mode("x")
			case 'c', 'r', 'u', 'A':
				set_mode("c")
			case 'O':
				if mode == "x" || mode == "" {
					mode = "xO"
				}
			case 'F':
				danger = "tar -F runs a script"
			}
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case i == 0 && !strings.HasPrefix(a, "-"):
			letters(a) // old-style bundle: "tar czf out.tgz dir"
			if strings.ContainsRune(a, 'f') && i+1 < len(args) {
				file, have_file = args[i+1], true
			}
		case a == "-f" || a == "--file":
			if i+1 < len(args) {
				file, have_file = args[i+1], true
				i++
			}
		case strings.HasPrefix(a, "--file="):
			file, have_file = strings.TrimPrefix(a, "--file="), true
		case a == "--list":
			set_mode("t")
		case a == "--extract" || a == "--get":
			set_mode("x")
		case a == "--create" || a == "--append" || a == "--update" || a == "--catenate" || a == "--concatenate" || a == "--delete":
			set_mode("c")
		case a == "--to-stdout":
			mode = "xO"
		case strings.HasPrefix(a, "--to-command") || strings.HasPrefix(a, "--use-compress-program") ||
			strings.HasPrefix(a, "--checkpoint-action") || strings.HasPrefix(a, "--rsh-command") ||
			strings.HasPrefix(a, "--info-script") || strings.HasPrefix(a, "--new-volume-script") ||
			a == "-I":
			danger = "tar " + a + " runs another program"
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--"):
			letters(a[1:])
			if strings.HasSuffix(a, "f") && i+1 < len(args) {
				file, have_file = args[i+1], true
				i++
			}
		}
	}
	if danger != "" {
		rc.unverified(danger)
		return
	}
	switch mode {
	case "t", "xO":
		return
	case "c":
		if !have_file || file == "-" {
			return // to stdout
		}
		w := sh_word{text: file, raw: file}
		for _, a := range inv.args {
			if a.text == file {
				w = a
				break
			}
		}
		if !rc.in_scratch(w) {
			rc.flag(RiskFileDelete, "tar writes an archive outside the scratch directory")
		}
	case "x":
		rc.flag(RiskFileDelete, "tar extracts files (and can create symlinks)")
	default:
		rc.unverified("tar invocation the gate cannot read")
	}
}

// find_rule: find reads, except for the actions that delete, write or run.
func find_rule(rc *risk_ctx, inv *invocation) {
	args := inv.args
	i := 0
	for i < len(args) && (args[i].text == "-H" || args[i].text == "-L" || args[i].text == "-P" || strings.HasPrefix(args[i].text, "-O") || args[i].text == "-D") {
		if args[i].text == "-D" {
			i++
		}
		i++
	}
	var roots []sh_word
	for i < len(args) {
		t := args[i].text
		if strings.HasPrefix(t, "-") || t == "(" || t == "!" || t == "," {
			break
		}
		roots = append(roots, args[i])
		i++
	}
	follows := parse_args(inv.args[:min(i, len(inv.args))]).has("-L", "-H") // symlinks followed
	for ; i < len(args); i++ {
		switch t := args[i].text; t {
		case "-exec", "-execdir", "-ok", "-okdir":
			j := i + 1
			for j < len(args) && args[j].text != ";" && args[j].text != "+" {
				j++
			}
			rc.invoke(args[i+1:j], inv.cmd, true)
			i = j
		case "-delete":
			if follows || !rc.all_in_scratch(roots) {
				rc.flag(RiskFileDelete, "find -delete removes files")
			}
		case "-fprint", "-fprint0", "-fprintf", "-fls":
			if i+1 < len(args) {
				rc.write(args[i+1], false)
				i++
			}
		}
	}
}

// ---------------------------------------------------------------- text tools

func register_text_rules() {
	for _, n := range []string{"awk", "gawk", "mawk", "nawk"} {
		risk_rules[n] = awk_rule
	}
	risk_rules["sed"] = sed_rule
	risk_rules["xmllint"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "--output", "-o", "--path", "--xpath", "--schema", "--relaxng", "--dtdvalid")
		if o, ok := pa.value("--output", "-o"); ok {
			rc.write(o, false)
		}
	}
}

var awk_print_redirect = regexp.MustCompile(`\bprintf?\b[^;{}]*>`)

// awk_rule: awk is how most one-liners slice output, so a plain program stays
// read-only. One that can run a command (system, a pipe) or write a file
// (print > "f") does not.
func awk_rule(rc *risk_ctx, inv *invocation) {
	pa := parse_args(inv.args, "-F", "-v", "-f", "-i", "-e", "--field-separator", "--assign", "--file", "--include", "--source", "-l", "--load")
	if _, ok := pa.value("-f", "--file", "-i", "--include", "-l", "--load"); ok {
		rc.unverified(inv.name + " runs a program file the gate cannot read")
		return
	}
	prog := ""
	if p, ok := pa.value("-e", "--source"); ok {
		prog = p.text
	} else if len(pa.ops) > 0 {
		prog = pa.ops[0].text
	}
	lower := strings.ToLower(prog)
	switch {
	case wordPresent(lower, "system") || strings.Contains(prog, "|"):
		rc.unverified(inv.name + " program can run commands")
	case awk_print_redirect.MatchString(prog):
		rc.flag(RiskFileDelete, inv.name+" program writes to a file")
	}
}

// sed_rule: read-only unless it edits in place, or its script uses the
// commands that write a file (w, W) or run one (e).
func sed_rule(rc *risk_ctx, inv *invocation) {
	pa := parse_args(inv.args, "-e", "--expression", "-f", "--file", "-l", "--line-length")
	if _, ok := pa.value("-f", "--file"); ok {
		rc.unverified("sed runs a script file the gate cannot read")
		return
	}
	var scripts []string
	for i, a := range inv.args {
		if (a.text == "-e" || a.text == "--expression") && i+1 < len(inv.args) {
			scripts = append(scripts, inv.args[i+1].text)
		} else if strings.HasPrefix(a.text, "--expression=") {
			scripts = append(scripts, strings.TrimPrefix(a.text, "--expression="))
		}
	}
	files := pa.ops
	if len(scripts) == 0 && len(files) > 0 {
		scripts = append(scripts, files[0].text)
		files = files[1:]
	}
	if !pa.has("--sandbox") {
		for _, s := range scripts {
			if !sed_script_safe(s) {
				rc.unverified("sed script writes files or runs commands (w/W/e), or is not one the gate can read")
				return
			}
		}
	}
	if pa.has("-i", "-I") || pa.has_prefix("--in-place") || pa.has_prefix("-i") {
		if inv.more || !rc.all_in_scratch(files) {
			rc.flag(RiskFileDelete, "sed -i edits files in place")
		}
	}
}

// sed_script_safe walks a sed script and reports whether it only uses commands
// that print, delete from the pattern space, or otherwise stay in memory.
func sed_script_safe(s string) bool {
	i := 0
	skip_delim := func() bool { // s[i] is a delimiter; skip to the matching one
		if i >= len(s) {
			return false
		}
		d := s[i]
		i++
		for i < len(s) && s[i] != d {
			if s[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(s) {
			return false
		}
		i++
		return true
	}
	to_eol := func() {
		for i < len(s) && s[i] != '\n' && s[i] != ';' {
			i++
		}
	}
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '{' || c == '}' || c == '!' ||
			c == ',' || c == '$' || c == '~' || c == '+' || (c >= '0' && c <= '9'):
			i++
		case c == '/':
			if !skip_delim() {
				return false
			}
			for i < len(s) && (s[i] == 'I' || s[i] == 'M') {
				i++
			}
		case c == '\\':
			i++
			if !skip_delim() {
				return false
			}
		case c == 's':
			i++
			if !skip_delim() { // pattern (the delimiter opens it)
				return false
			}
			i-- // the closing delimiter also opens the replacement
			if !skip_delim() {
				return false
			}
			for i < len(s) && strings.IndexByte("gpiImM0123456789", s[i]) >= 0 {
				i++
			}
			if i < len(s) && (s[i] == 'w' || s[i] == 'W' || s[i] == 'e') {
				return false
			}
		case c == 'y':
			i++
			if !skip_delim() {
				return false
			}
			i--
			if !skip_delim() {
				return false
			}
		case strings.IndexByte("pdDnNgGhHxlqQzP=F", c) >= 0:
			i++
		case c == 'a' || c == 'i' || c == 'c' || c == 'b' || c == 't' || c == 'T' || c == ':' || c == 'r' || c == 'R' || c == 'v':
			i++
			to_eol()
		default:
			return false // w, W, e, or something the walker does not know
		}
	}
	return true
}

// ---------------------------------------------------------------- system

// verb_rule is a multiplexer (systemctl, docker, kubectl, ...): the first
// operand picks what it does. read verbs are read-only; mapped verbs get their
// category; any other verb gets deflt. No verb at all prints a list or help.
func verb_rule(value_opts []string, read []string, mapped map[string]RiskCategory, deflt RiskCategory) risk_rule {
	rs := set_of(read...)
	return func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, value_opts...)
		v := pa.verb()
		if v == "" || rs[v] {
			return
		}
		if c, ok := mapped[v]; ok {
			rc.flag(c, inv.name+" "+v)
			return
		}
		rc.flag(deflt, inv.name+" "+v)
	}
}

func register_system_rules() {
	risk_rules["systemctl"] = verb_rule(
		[]string{"-t", "--type", "--state", "-p", "--property", "-n", "--lines", "-o", "--output", "-H", "--host", "-M", "--machine", "--root", "--signal", "-s", "--kill-whom", "--what", "--job-mode", "--boot-loader-menu", "--boot-loader-entry", "--timestamp", "--check-inhibitors", "--when"},
		[]string{"status", "show", "is-active", "is-enabled", "is-failed", "is-system-running", "list-units",
			"list-unit-files", "list-sockets", "list-timers", "list-jobs", "list-dependencies", "list-machines",
			"list-automounts", "list-paths", "cat", "get-default", "show-environment", "help"},
		nil, RiskSysControl)
	risk_rules["service"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args)
		if pa.has("--status-all") || len(pa.ops) == 0 {
			return
		}
		if len(pa.ops) >= 2 && pa.ops[1].text == "status" {
			return
		}
		rc.flag(RiskSysControl, "service "+strings.Join(op_texts(pa.ops), " "))
	}
	risk_rules["journalctl"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args)
		switch {
		case pa.has_prefix("--vacuum"):
			rc.flag(RiskFileDelete, "journalctl --vacuum deletes journal files")
		case pa.has("--rotate", "--flush", "--relinquish-var", "--smart-relinquish-var", "--sync", "--setup-keys", "--update-catalog"):
			rc.flag(RiskSysControl, "journalctl changes the journal")
		}
	}
	risk_rules["dmesg"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-f", "--facility", "-l", "--level", "-s", "--buffer-size", "-n", "--console-level", "-F", "--file", "--time-format")
		if pa.has("-c", "-C", "-D", "-E", "-n", "--read-clear", "--clear", "--console-off", "--console-on", "--console-level") {
			rc.flag(RiskSysControl, "dmesg clears the ring buffer or changes the console")
		}
	}
	risk_rules["date"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-d", "--date", "-f", "--file", "-r", "--reference", "-I", "--rfc-3339")
		if pa.has("-s", "--set") {
			rc.flag(RiskSysControl, "date -s sets the clock")
			return
		}
		for _, o := range pa.ops {
			if !strings.HasPrefix(o.text, "+") {
				rc.flag(RiskSysControl, "date with an operand sets the clock")
				return
			}
		}
	}
	risk_rules["hostname"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-F", "--file")
		if pa.has("-F", "--file", "-b", "--boot") || len(pa.ops) > 0 {
			rc.flag(RiskSysControl, "hostname sets the host name")
		}
	}
	risk_rules["hostnamectl"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-H", "--host", "-M", "--machine")
		switch pa.verb() {
		case "", "status", "show":
		case "hostname", "icon-name", "chassis", "deployment", "location":
			if len(pa.ops) > 1 {
				rc.flag(RiskSysControl, "hostnamectl sets "+pa.verb())
			}
		default:
			rc.flag(RiskSysControl, "hostnamectl "+pa.verb())
		}
	}
	risk_rules["timedatectl"] = verb_rule([]string{"-H", "--host", "-M", "--machine", "-p", "--property"},
		[]string{"status", "show", "list-timezones", "timesync-status", "show-timesync"}, nil, RiskSysControl)
	risk_rules["localectl"] = verb_rule([]string{"-H", "--host", "-M", "--machine"},
		[]string{"status", "list-locales", "list-keymaps", "list-x11-keymap-models", "list-x11-keymap-layouts",
			"list-x11-keymap-variants", "list-x11-keymap-options"}, nil, RiskSysControl)
	risk_rules["loginctl"] = verb_rule([]string{"-H", "--host", "-M", "--machine", "-p", "--property", "-n", "--lines", "-o", "--output"},
		[]string{"list-sessions", "list-users", "list-seats", "session-status", "show-session", "user-status",
			"show-user", "seat-status", "show-seat"}, nil, RiskSysControl)
	risk_rules["resolvectl"] = verb_rule([]string{"-i", "--interface", "-t", "--type", "-c", "--class", "-p", "--protocol"},
		[]string{"status", "query", "statistics", "show-cache", "show-server-state"}, nil, RiskSysControl)
	risk_rules["systemd-resolve"] = risk_rules["resolvectl"]
	risk_rules["networkctl"] = verb_rule(nil, []string{"list", "status", "lldp", "label"}, nil, RiskSysControl)
	risk_rules["chronyc"] = verb_rule([]string{"-h", "-p", "-f"}, []string{"tracking", "sources", "sourcestats",
		"activity", "ntpdata", "clients", "serverstats", "sourcename", "authdata", "selectdata", "rtcdata", "manual"}, nil, RiskSysControl)
	risk_rules["hwclock"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "--date", "-f", "--rtc", "--adjfile", "--epoch")
		if pa.has("-w", "-s", "--systohc", "--hctosys", "--set", "--systz", "-a", "--adjust", "--setepoch") {
			rc.flag(RiskSysControl, "hwclock sets a clock")
		}
	}
	risk_rules["mount"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-t", "--types", "-O", "--test-opts", "-L", "--label", "-U", "--uuid")
		if len(pa.ops) > 0 || pa.has("-a", "--all", "-o", "--options", "--bind", "--move", "--rbind", "-B", "-M", "-R") {
			rc.flag(RiskSysControl, "mount changes what is mounted")
		}
	}
	risk_rules["swapon"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "--show", "--bytes")
		if len(pa.ops) > 0 || pa.has("-a", "--all", "-e") {
			rc.flag(RiskSysControl, "swapon enables swap")
		}
	}
	risk_rules["sysctl"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-r", "--pattern")
		if pa.has("-w", "--write", "-p", "--load", "--system") {
			rc.flag(RiskSysControl, "sysctl changes kernel settings")
			return
		}
		for _, o := range pa.ops {
			if strings.Contains(o.text, "=") {
				rc.flag(RiskSysControl, "sysctl changes kernel settings")
				return
			}
		}
	}
	risk_rules["crontab"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-u")
		if pa.has("-l") && !pa.has("-r", "-e", "-i") && len(pa.ops) == 0 {
			return
		}
		rc.flag(RiskSysControl, "crontab changes scheduled jobs")
	}
	risk_rules["chage"] = func(rc *risk_ctx, inv *invocation) {
		if pa := parse_args(inv.args); pa.has("-l", "--list") && len(pa.flags) == 1 {
			return
		}
		rc.flag(RiskSysControl, "chage changes password ageing")
	}
	risk_rules["fuser"] = func(rc *risk_ctx, inv *invocation) {
		if parse_args(inv.args, "-n", "--namespace").has("-k", "--kill") {
			rc.flag(RiskSysControl, "fuser -k kills processes")
		}
	}
	risk_rules["update-alternatives"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "--altdir", "--admindir", "--log")
		if pa.has("--display", "--list", "--query", "--get-selections") {
			return
		}
		rc.flag(RiskSysControl, "update-alternatives changes system defaults")
	}
	risk_rules["alternatives"] = risk_rules["update-alternatives"]
	// Networking.
	risk_rules["ip"] = ip_rule
	risk_rules["ifconfig"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args)
		if len(pa.ops) > 1 {
			rc.flag(RiskSysControl, "ifconfig changes an interface")
		}
	}
	risk_rules["route"] = func(rc *risk_ctx, inv *invocation) {
		if pa := parse_args(inv.args, "-A", "-4", "-6"); len(pa.ops) > 0 {
			rc.flag(RiskSysControl, "route changes the routing table")
		}
	}
	risk_rules["arp"] = func(rc *risk_ctx, inv *invocation) {
		if parse_args(inv.args, "-i", "-H", "-t", "--device", "--hw-type").has("-d", "-s", "-f", "--delete", "--set", "--file") {
			rc.flag(RiskSysControl, "arp changes the neighbour table")
		}
	}
	risk_rules["iptables"] = iptables_rule
	risk_rules["ip6tables"] = iptables_rule
	risk_rules["nft"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-f", "--file", "-I", "--includepath")
		if _, ok := pa.value("-f", "--file"); ok {
			rc.flag(RiskSysControl, "nft -f loads a ruleset")
			return
		}
		switch pa.verb() {
		case "", "list", "describe", "monitor":
		default:
			rc.flag(RiskSysControl, "nft "+pa.verb())
		}
	}
	risk_rules["ufw"] = verb_rule(nil, []string{"status", "show", "version", "--version"}, nil, RiskSysControl)
	risk_rules["ipset"] = verb_rule(nil, []string{"list", "-L", "save", "-S", "test", "-T", "version", "-V", "help"}, nil, RiskSysControl)
	risk_rules["conntrack"] = func(rc *risk_ctx, inv *invocation) {
		if parse_args(inv.args).has("-D", "-F", "-U", "-I", "--delete", "--flush", "--update", "--create") {
			rc.flag(RiskSysControl, "conntrack changes the connection table")
		}
	}
	risk_rules["tc"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-b", "-batch", "-n", "-netns")
		ops := op_texts(pa.ops)
		if _, ok := pa.value("-b", "-batch"); ok {
			rc.flag(RiskSysControl, "tc -batch applies changes")
			return
		}
		if len(ops) <= 1 || ops[1] == "show" || ops[1] == "list" || ops[1] == "ls" {
			return
		}
		rc.flag(RiskSysControl, "tc changes traffic control")
	}
	risk_rules["bridge"] = func(rc *risk_ctx, inv *invocation) {
		ops := op_texts(parse_args(inv.args).ops)
		if len(ops) <= 1 || ops[1] == "show" || ops[1] == "list" || ops[1] == "monitor" {
			return
		}
		rc.flag(RiskSysControl, "bridge changes the bridge configuration")
	}
	risk_rules["ethtool"] = func(rc *risk_ctx, inv *invocation) {
		read := set_of("-i", "-S", "-k", "-g", "-a", "-c", "-l", "-m", "-T", "-P", "-d", "-e", "-x", "-h", "--version",
			"--driver", "--statistics", "--show-features", "--show-offload", "--show-ring", "--show-pause", "--show-coalesce",
			"--show-channels", "--module-info", "--dump-module-eeprom", "--show-time-stamping", "--show-permaddr",
			"--register-dump", "--eeprom-dump", "--show-priv-flags", "--show-eee", "--show-fec", "--show-rxfh",
			"--show-rxfh-indir", "--json", "--debug", "--include-statistics", "--all-groups", "--groups")
		for _, f := range parse_args(inv.args).flags {
			if !read[f] {
				rc.flag(RiskSysControl, "ethtool "+f+" changes the interface")
				return
			}
		}
	}
	risk_rules["nmcli"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-f", "--fields", "-g", "--get-values", "-m", "--mode", "-c", "--colors", "-e", "--escape", "-w", "--wait")
		ops := op_texts(pa.ops)
		if len(ops) <= 1 {
			return
		}
		switch ops[1] {
		case "status", "show", "list", "permissions", "s", "sh", "l":
			return
		case "wifi", "w":
			if len(ops) <= 2 || ops[2] == "list" {
				return
			}
		}
		rc.flag(RiskSysControl, "nmcli "+strings.Join(ops[:2], " "))
	}
	risk_rules["firewall-cmd"] = func(rc *risk_ctx, inv *invocation) {
		for _, f := range parse_args(inv.args).flags {
			switch {
			case strings.HasPrefix(f, "--list"), strings.HasPrefix(f, "--get"), strings.HasPrefix(f, "--query"),
				strings.HasPrefix(f, "--info"), f == "--state", strings.HasPrefix(f, "--zone"), f == "--permanent",
				f == "-h", f == "--help", f == "-V", f == "--version", f == "-q", f == "--quiet":
			default:
				rc.flag(RiskSysControl, "firewall-cmd "+f)
				return
			}
		}
	}
	risk_rules["smartctl"] = func(rc *risk_ctx, inv *invocation) {
		if parse_args(inv.args, "-d", "--device", "-l", "--log").has("-t", "--test", "-s", "--smart", "-o", "--offlineauto", "-S", "--saveauto", "-X", "--abort", "--set") {
			rc.flag(RiskSysControl, "smartctl changes the drive")
		}
	}
	risk_rules["mdadm"] = func(rc *risk_ctx, inv *invocation) {
		if pa := parse_args(inv.args); pa.has("--detail", "-D", "--examine", "-E", "--query", "-Q", "--detail-platform", "--version", "-V") &&
			!pa.has("--create", "-C", "--assemble", "-A", "--manage", "--grow", "-G", "--fail", "-f", "--remove", "-r", "--add", "-a", "--stop", "-S", "--zero-superblock") {
			return
		}
		rc.flag(RiskSysControl, "mdadm changes an array")
	}
	risk_rules["zpool"] = verb_rule(nil, []string{"status", "list", "iostat", "get", "history", "events", "version", "help"}, nil, RiskSysControl)
	risk_rules["zfs"] = verb_rule(nil, []string{"list", "get", "holds", "userspace", "groupspace", "projectspace", "diff", "version", "help"}, nil, RiskSysControl)
	risk_rules["nginx"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-c", "-p", "-g", "-e", "-s")
		if s, ok := pa.value("-s"); ok {
			rc.flag(RiskSysControl, "nginx -s "+s.text)
			return
		}
		if pa.has("-t", "-T", "-v", "-V", "-h", "-?") {
			return
		}
		rc.flag(RiskSysControl, "nginx with no test flag starts a server")
	}
	for _, n := range []string{"apachectl", "apache2ctl", "httpd", "apache2"} {
		risk_rules[n] = func(rc *risk_ctx, inv *invocation) {
			pa := parse_args(inv.args, "-f", "-C", "-c", "-d", "-D", "-e", "-E", "-k")
			if k, ok := pa.value("-k"); ok {
				rc.flag(RiskSysControl, inv.name+" -k "+k.text)
				return
			}
			switch pa.verb() {
			case "configtest", "status", "fullstatus", "-t", "-S", "-M", "-v", "-V", "-l", "-L":
				return
			case "":
				if pa.has("-t", "-S", "-M", "-v", "-V", "-l", "-L", "-h") {
					return
				}
			}
			rc.flag(RiskSysControl, inv.name+" starts, stops or reloads the server")
		}
	}
	risk_rules["haproxy"] = func(rc *risk_ctx, inv *invocation) {
		if parse_args(inv.args, "-f", "-L", "-p", "-sf", "-st", "-x", "-S").has("-c", "-v", "-vv", "-V") {
			return
		}
		rc.flag(RiskSysControl, "haproxy starts a server")
	}
	risk_rules["tcpdump"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-i", "-c", "-s", "-w", "-r", "-C", "-G", "-W", "-z", "-Z", "-F", "-y", "-E", "-B", "-j", "-T", "-V", "--interface")
		if _, ok := pa.value("-z"); ok {
			rc.unverified("tcpdump -z runs a command on each capture file")
			return
		}
		if w, ok := pa.value("-w"); ok && w.text != "-" {
			rc.write(w, false)
		}
	}
	risk_rules["sar"] = func(rc *risk_ctx, inv *invocation) {
		if o, ok := parse_args(inv.args, "-o", "-f", "-s", "-e", "-i", "-P", "-n", "-I", "-m").value("-o"); ok {
			rc.write(o, false)
		}
	}
	risk_rules["ssh-keygen"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-f", "-F", "-E", "-t", "-b", "-C", "-N", "-P")
		if pa.has("-l", "-F", "-L", "-y", "-B", "-Q") && !pa.has("-R") {
			return
		}
		rc.flag(RiskFileDelete, "ssh-keygen creates or changes key files")
	}
	risk_rules["openssl"] = openssl_rule
}

// ip_rule: "ip OBJECT [COMMAND]": show/list/get read; anything else changes
// the network. "ip netns exec NAME cmd" runs another command.
func ip_rule(rc *risk_ctx, inv *invocation) {
	// ip's options are single-dash words ("-br", "-batch"), so they are read
	// whole rather than as bundles of letters.
	var ops []string
	var words []sh_word
	for i := 0; i < len(inv.args); i++ {
		t := inv.args[i].text
		switch {
		case t == "-b" || t == "-batch" || t == "-force":
			rc.flag(RiskSysControl, "ip -batch applies changes")
			return
		case t == "-n" || t == "-netns" || t == "-f" || t == "-family" || t == "-rc" || t == "-rcvbuf" || t == "-l" || t == "-loops":
			i++
		case strings.HasPrefix(t, "-"):
		default:
			ops = append(ops, t)
			words = append(words, inv.args[i])
		}
	}
	if len(ops) == 0 {
		return
	}
	if ops[0] == "monitor" {
		return
	}
	if len(ops) == 1 {
		return // "ip a", "ip route": the default command is show
	}
	cmd := ops[1]
	if (ops[0] == "netns" || ops[0] == "net") && cmd == "exec" {
		if len(words) > 3 {
			rc.invoke(words[3:], inv.cmd, inv.more)
		}
		return
	}
	switch cmd {
	case "show", "list", "ls", "lst", "get", "help", "sh", "s", "l", "identify", "pids", "monitor":
		return
	}
	if strings.HasPrefix(cmd, "-") || isAllDigits(strings.ReplaceAll(cmd, ".", "")) {
		return // "ip route 10.0.0.0/8" style selectors fall back to show
	}
	rc.flag(RiskSysControl, "ip "+ops[0]+" "+cmd)
}

// iptables_rule: only listing flags are read-only.
func iptables_rule(rc *risk_ctx, inv *invocation) {
	read := set_of("-L", "--list", "-S", "--list-rules", "-n", "--numeric", "-v", "--verbose", "-x", "--exact",
		"--line-numbers", "-t", "--table", "-w", "--wait", "-h", "--help", "-V", "--version", "-c", "--check", "-C")
	pa := parse_args(inv.args, "-t", "--table", "-w", "--wait")
	for _, f := range pa.flags {
		if read[f] || strings.HasPrefix(f, "--table=") {
			continue
		}
		if len(f) > 2 && f[0] == '-' && f[1] != '-' && strings.Trim(f[1:], "LSnvx") == "" {
			continue // bundled "-nvL"
		}
		rc.flag(RiskSysControl, inv.name+" "+f+" changes the firewall")
		return
	}
}

// openssl_rule: inspecting certificates and keys reads; s_client talks to the
// network; writing output files is a write.
func openssl_rule(rc *risk_ctx, inv *invocation) {
	pa := parse_args(inv.args, "-in", "-out", "-keyout", "-inform", "-outform", "-passin", "-passout", "-connect",
		"-servername", "-CAfile", "-CApath", "-key", "-cert", "-config", "-days", "-subj", "-newkey", "-signkey", "-certfile", "-name")
	switch sub := pa.verb(); sub {
	case "s_client", "s_server", "ocsp", "s_time":
		rc.flag(RiskNetEgress, "openssl "+sub+" makes a network connection")
		return
	case "", "x509", "req", "crl", "verify", "version", "ciphers", "asn1parse", "dgst", "rsa", "ec", "pkey",
		"pkcs12", "pkcs7", "crl2pkcs7", "list", "errstr", "speed", "prime", "rand", "base64", "enc",
		"sess_id", "storeutl", "cms", "smime", "genrsa", "genpkey", "ecparam", "dhparam", "passwd":
	default:
		rc.unverified("openssl " + sub + " is not a subcommand the gate recognizes")
		return
	}
	for _, o := range []string{"-out", "-keyout"} {
		if w, ok := pa.value(o); ok {
			rc.write(w, false)
		}
	}
}

// ---------------------------------------------------------------- platforms

func register_platform_rules() {
	docker := func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-H", "--host", "--context", "-c", "--config", "-l", "--log-level",
			"--tlscacert", "--tlscert", "--tlskey", "-f", "--file", "-p", "--project-name", "--project-directory",
			"--env-file", "--profile", "--format", "--filter", "-n", "--tail", "--since", "--until")
		ops := op_texts(pa.ops)
		if len(ops) == 0 {
			return
		}
		verb := ops[0]
		group := ""
		switch verb {
		case "compose", "container", "image", "network", "volume", "system", "node", "service", "stack",
			"context", "buildx", "secret", "config", "plugin", "trust", "manifest", "swarm", "builder":
			group = verb + " "
			if len(ops) < 2 {
				return
			}
			verb = ops[1]
		}
		if inv.name == "docker-compose" || inv.name == "podman-compose" {
			group = "compose "
		}
		switch verb {
		case "ps", "ls", "list", "images", "inspect", "logs", "stats", "top", "port", "diff", "history",
			"version", "info", "events", "df", "config", "search", "show", "help", "--version", "-v":
			return
		case "exec", "run", "build", "attach", "debug", "cp", "commit", "import", "load":
			rc.unverified(inv.name + " " + group + verb + " runs or brings in code the gate cannot read")
		case "pull", "push", "login", "logout":
			rc.flag(RiskNetEgress, inv.name+" "+group+verb+" contacts a registry")
		default:
			rc.flag(RiskSysControl, inv.name+" "+group+verb)
		}
	}
	for _, n := range []string{"docker", "podman", "nerdctl", "docker-compose", "podman-compose"} {
		risk_rules[n] = docker
	}
	kube := func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-n", "--namespace", "--context", "--cluster", "--kubeconfig", "--user", "-s",
			"--server", "--token", "--as", "--as-group", "--request-timeout", "-v", "-l", "--selector", "-o", "--output",
			"-c", "--container", "--since", "--tail", "-f", "--filename", "--field-selector", "--sort-by")
		ops := op_texts(pa.ops)
		if len(ops) == 0 {
			return
		}
		sub := ""
		if len(ops) > 1 {
			sub = ops[1]
		}
		switch ops[0] {
		case "get", "describe", "logs", "top", "explain", "version", "api-resources", "api-versions",
			"cluster-info", "events", "diff", "wait", "completion", "options", "help", "plugin":
			return
		case "config":
			switch sub {
			case "", "view", "get-contexts", "current-context", "get-clusters", "get-users":
				return
			}
			rc.unverified(inv.name + " config " + sub + " changes the local kubeconfig")
		case "auth":
			if sub == "can-i" || sub == "whoami" {
				return
			}
			rc.flag(RiskSysControl, inv.name+" auth "+sub)
		case "rollout":
			if sub == "status" || sub == "history" {
				return
			}
			rc.flag(RiskSysControl, inv.name+" rollout "+sub)
		case "exec", "attach", "run", "debug", "cp", "port-forward", "proxy":
			rc.unverified(inv.name + " " + ops[0] + " runs something the gate cannot read")
		default:
			rc.flag(RiskSysControl, inv.name+" "+ops[0])
		}
	}
	risk_rules["kubectl"] = kube
	risk_rules["oc"] = kube
	risk_rules["helm"] = verb_rule([]string{"-n", "--namespace", "--kube-context", "--kubeconfig", "-o", "--output", "--repo", "--version"},
		[]string{"list", "ls", "status", "get", "history", "hist", "show", "inspect", "search", "version", "env",
			"template", "lint", "verify", "help"},
		map[string]RiskCategory{"pull": RiskNetEgress, "fetch": RiskNetEgress}, RiskSysControl)
	risk_rules["terraform"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-chdir", "-var", "-var-file", "-target", "-out", "-state")
		ops := op_texts(pa.ops)
		if len(ops) == 0 {
			return
		}
		sub := ""
		if len(ops) > 1 {
			sub = ops[1]
		}
		switch ops[0] {
		case "plan", "show", "validate", "version", "output", "providers", "graph", "help":
			return
		case "state":
			if sub == "list" || sub == "show" || sub == "pull" {
				return
			}
			rc.flag(RiskSysControl, "terraform state "+sub)
		case "workspace":
			if sub == "list" || sub == "show" {
				return
			}
			rc.flag(RiskSysControl, "terraform workspace "+sub)
		case "fmt":
			if pa.has("-check", "--check") {
				return
			}
			rc.flag(RiskFileDelete, "terraform fmt rewrites files")
		case "init", "get":
			rc.flag(RiskNetEgress, "terraform "+ops[0]+" downloads providers and modules")
		case "apply", "destroy", "import", "taint", "untaint", "refresh", "force-unlock", "login", "logout":
			rc.flag(RiskSysControl, "terraform "+ops[0])
		default:
			rc.unverified("terraform " + ops[0] + " is not a subcommand the gate recognizes")
		}
	}
	risk_rules["ansible"] = func(rc *risk_ctx, inv *invocation) {
		if pa := parse_args(inv.args); pa.has("--version", "--list-hosts") {
			return
		}
		rc.flag(RiskSysControl, "ansible runs modules on other hosts")
	}
	risk_rules["ansible-playbook"] = func(rc *risk_ctx, inv *invocation) {
		if pa := parse_args(inv.args); pa.has("--syntax-check", "--list-tasks", "--list-hosts", "--list-tags", "--version") {
			return
		}
		rc.flag(RiskSysControl, "ansible-playbook runs a playbook")
	}
	risk_rules["ansible-inventory"] = func(rc *risk_ctx, inv *invocation) {
		if o, ok := parse_args(inv.args, "--output", "-i", "--inventory").value("--output"); ok {
			rc.write(o, false)
		}
	}
	risk_rules["ansible-doc"] = func(*risk_ctx, *invocation) {}
	risk_rules["ansible-config"] = verb_rule(nil, []string{"list", "dump", "view"}, nil, RiskUnverified)
	risk_rules["flux"] = verb_rule([]string{"-n", "--namespace", "--context", "--kubeconfig"},
		[]string{"get", "logs", "check", "stats", "tree", "trace", "version", "diff", "export", "events", "help"}, nil, RiskSysControl)
	risk_rules["argocd"] = func(rc *risk_ctx, inv *invocation) {
		ops := op_texts(parse_args(inv.args, "--server", "--auth-token", "--grpc-web-root-path", "-o", "--output").ops)
		if len(ops) == 0 || ops[0] == "version" || ops[0] == "context" {
			return
		}
		if len(ops) > 1 {
			switch ops[1] {
			case "list", "get", "diff", "history", "logs", "manifests", "resources", "get-user-info":
				return
			}
		}
		rc.flag(RiskSysControl, "argocd "+strings.Join(ops[:min(2, len(ops))], " "))
	}
	risk_rules["crictl"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-r", "--runtime-endpoint", "-i", "--image-endpoint", "-c", "--config", "-o", "--output", "--timeout")
		switch v := pa.verb(); v {
		case "", "ps", "pods", "images", "img", "inspect", "inspecti", "inspectp", "logs", "stats", "statsp", "info",
			"version", "imagefsinfo", "config", "help":
		case "exec", "attach", "run", "runp", "port-forward":
			rc.unverified("crictl " + v + " runs something the gate cannot read")
		case "pull":
			rc.flag(RiskNetEgress, "crictl pull contacts a registry")
		default:
			rc.flag(RiskSysControl, "crictl "+v)
		}
	}
	risk_rules["virsh"] = verb_rule([]string{"-c", "--connect"},
		[]string{"list", "dominfo", "domstate", "domblklist", "domiflist", "dumpxml", "nodeinfo", "capabilities",
			"version", "net-list", "pool-list", "vol-list", "domblkinfo", "domifaddr", "domstats", "hostname",
			"sysinfo", "uri", "net-info", "pool-info", "help"}, nil, RiskSysControl)
	risk_rules["git"] = git_rule
	risk_rules["gh"] = func(rc *risk_ctx, inv *invocation) {
		switch parse_args(inv.args).verb() {
		case "":
		case "extension":
			rc.flag(RiskPkgInstall, "gh extension installs or removes software")
		default:
			rc.flag(RiskNetEgress, "gh calls the GitHub API")
		}
	}
}

// git_rule sorts git's verbs: reads, those that contact a remote, those that
// discard work, and the rest, which change the repository.
func git_rule(rc *risk_ctx, inv *invocation) {
	args := inv.args
	i := 0
	for i < len(args) && strings.HasPrefix(args[i].text, "-") {
		t := args[i].text
		switch {
		case t == "-C" || t == "--git-dir" || t == "--work-tree" || t == "--namespace":
			i++
		case t == "-c":
			// "-c key=value" can set a pager, an editor or an ssh command:
			// every one of them runs a program. Colour settings are the only
			// ones worth letting through.
			if i+1 >= len(args) || !strings.HasPrefix(args[i+1].text, "color.") {
				rc.unverified("git -c can make git run another program")
				return
			}
			i++
		case strings.HasPrefix(t, "--config-env") || strings.HasPrefix(t, "--exec-path="):
			rc.unverified("git " + t + " can make git run another program")
			return
		}
		i++
	}
	if i >= len(args) {
		return
	}
	verb := args[i].text
	pa := parse_args(args[i+1:], "-n", "--max-count", "--format", "--pretty", "-S", "-G", "--grep", "--author", "--since", "--until", "-L", "-o", "--output")
	sub := pa.verb()
	if o, ok := pa.value("--output", "-o"); ok && verb != "archive" && verb != "format-patch" {
		rc.write(o, false)
	}
	switch verb {
	case "status", "log", "diff", "show", "blame", "annotate", "shortlog", "describe", "rev-parse", "rev-list",
		"ls-files", "ls-tree", "cat-file", "grep", "whatchanged", "count-objects", "for-each-ref", "show-ref",
		"show-branch", "name-rev", "merge-base", "cherry", "verify-commit", "verify-tag", "fsck", "help",
		"version", "var", "check-ignore", "check-attr", "check-ref-format", "diff-tree", "diff-files",
		"diff-index", "range-diff", "--version", "--help":
		return
	case "reflog":
		if sub == "" || sub == "show" || sub == "list" || sub == "exists" {
			return
		}
		rc.flag(RiskFileDelete, "git reflog "+sub+" discards history")
	case "branch":
		if pa.has("-d", "-D", "--delete", "-m", "-M", "--move", "-f", "--force") {
			rc.flag(RiskFileDelete, "git branch deletes or moves a branch")
		} else if pa.has("-c", "-C", "--copy", "-u", "--set-upstream-to", "--unset-upstream", "--edit-description", "-t", "--track") ||
			(len(pa.ops) > 0 && !pa.has("-l", "--list", "--contains", "--merged", "--no-merged", "--points-at")) {
			rc.unverified("git branch creates or changes a branch")
		}
	case "tag":
		if pa.has("-d", "--delete") {
			rc.flag(RiskFileDelete, "git tag -d deletes a tag")
		} else if len(pa.ops) > 0 && !pa.has("-l", "--list", "--contains", "--merged", "--no-merged", "--points-at", "-v", "--verify") {
			rc.unverified("git tag creates a tag")
		}
	case "stash":
		if sub == "list" || sub == "show" {
			return
		}
		rc.flag(RiskFileDelete, "git stash changes the working tree")
	case "remote":
		switch sub {
		case "", "-v", "--verbose", "get-url":
			if !pa.has("-v", "--verbose") || sub == "" || sub == "get-url" {
				return
			}
			return
		case "show", "update", "prune":
			rc.flag(RiskNetEgress, "git remote "+sub+" contacts a remote")
		default:
			rc.unverified("git remote " + sub + " changes the repository")
		}
	case "config":
		if pa.has("--get", "--get-all", "--get-regexp", "--get-urlmatch", "--get-color", "--get-colorbool", "--list", "-l") ||
			sub == "get" || sub == "list" || (len(pa.ops) == 1 && !pa.has("--unset", "--unset-all", "--add", "--replace-all", "-e", "--edit", "--rename-section", "--remove-section")) {
			return
		}
		rc.unverified("git config changes configuration (which can make git run programs)")
	case "worktree":
		if sub == "list" {
			return
		}
		rc.unverified("git worktree " + sub + " changes the repository")
	case "submodule":
		switch sub {
		case "", "status", "summary":
			return
		case "foreach":
			rc.unverified("git submodule foreach runs a command in each submodule")
		default:
			rc.flag(RiskNetEgress, "git submodule "+sub+" contacts a remote")
		}
	case "archive":
		if pa.has_prefix("--remote") {
			rc.flag(RiskNetEgress, "git archive --remote contacts a remote")
			return
		}
		if o, ok := pa.value("--output", "-o"); ok {
			rc.write(o, false)
		}
	case "push", "fetch", "pull", "clone", "ls-remote", "fetch-pack", "send-pack", "request-pull":
		rc.flag(RiskNetEgress, "git "+verb+" contacts a remote")
	case "clean", "reset", "checkout", "restore", "rm", "mv", "gc", "prune", "switch":
		rc.flag(RiskFileDelete, "git "+verb+" discards files/changes")
	default:
		rc.unverified("git " + verb + " changes the repository")
	}
}

// ---------------------------------------------------------------- packages

// pkg_policy is one package manager: the verbs that only read, and the verbs
// that change what is installed. A verb in neither is unverified - "npm run",
// "cargo build", "go generate" all execute code from the project.
type pkg_policy struct {
	read    []string
	install []string
	egress  []string // refresh an index from the network
	values  []string // options that take a value
}

// pkg_policies is split by verb on purpose: reading the package list is most
// of what an investigation does - "dpkg -l", "apt list --installed", "pip
// freeze", "npm ls" - and gating those would make every survey stop for
// permission while teaching nobody anything about risk.
var pkg_policies = map[string]pkg_policy{
	"apt": {read: []string{"list", "show", "search", "policy", "depends", "rdepends", "showsrc", "madison", "help"},
		install: []string{"install", "remove", "purge", "upgrade", "full-upgrade", "dist-upgrade", "autoremove", "reinstall", "autopurge", "build-dep", "satisfy", "edit-sources"},
		egress:  []string{"update"}, values: []string{"-o", "-c", "-t"}},
	"apt-get": {read: []string{"check", "changelog", "help"},
		install: []string{"install", "remove", "purge", "upgrade", "dist-upgrade", "autoremove", "build-dep", "reinstall", "autoclean", "clean", "source", "download", "satisfy"},
		egress:  []string{"update"}, values: []string{"-o", "-c", "-t"}},
	"aptitude": {read: []string{"search", "show", "versions", "why", "why-not", "changelog", "help"},
		install: []string{"install", "remove", "purge", "full-upgrade", "safe-upgrade", "reinstall", "markauto", "unmarkauto", "hold", "unhold"},
		egress:  []string{"update"}},
	"yum": {read: []string{"list", "info", "search", "provides", "whatprovides", "repolist", "history", "deplist", "repoquery", "check-update", "version", "help", "repoinfo"},
		install: []string{"install", "remove", "erase", "update", "upgrade", "downgrade", "reinstall", "autoremove", "distro-sync", "swap", "group", "groupinstall", "groupremove", "localinstall"},
		egress:  []string{"makecache"}, values: []string{"--enablerepo", "--disablerepo", "-c", "--config", "--setopt", "--installroot"}},
	"dnf": {read: []string{"list", "info", "search", "provides", "whatprovides", "repolist", "history", "deplist", "repoquery", "check-update", "version", "help", "repoinfo", "module"},
		install: []string{"install", "remove", "erase", "update", "upgrade", "downgrade", "reinstall", "autoremove", "distro-sync", "swap", "group", "mark"},
		egress:  []string{"makecache"}, values: []string{"--enablerepo", "--disablerepo", "-c", "--config", "--setopt", "--installroot"}},
	"apk":    {read: []string{"info", "search", "list", "version", "policy", "stats", "dot"}, install: []string{"add", "del", "upgrade", "fix", "cache"}, egress: []string{"update"}},
	"zypper": {read: []string{"search", "se", "info", "if", "list-updates", "lu", "patches", "products", "repos", "lr", "packages", "pa", "what-provides", "wp", "patterns", "pt"}, install: []string{"install", "in", "remove", "rm", "update", "up", "dup", "dist-upgrade", "patch", "addrepo", "ar"}, egress: []string{"refresh", "ref"}},
	"snap":   {read: []string{"list", "info", "find", "version", "connections", "services", "changes", "tasks", "warnings", "known", "help"}, install: []string{"install", "remove", "refresh", "revert", "enable", "disable", "connect", "disconnect"}},
	"flatpak": {read: []string{"list", "info", "search", "remotes", "history", "ps", "remote-ls"},
		install: []string{"install", "uninstall", "update", "remote-add", "remote-delete"}},
	"brew": {read: []string{"list", "ls", "info", "search", "deps", "leaves", "outdated", "config", "doctor", "--prefix", "--cellar", "uses", "desc", "home", "--version", "commands"},
		install: []string{"install", "uninstall", "remove", "rm", "upgrade", "reinstall", "tap", "untap", "link", "unlink", "cleanup"}, egress: []string{"update"}},
	"pkg":   {read: []string{"info", "search", "query", "audit", "version", "which", "stats", "check"}, install: []string{"install", "delete", "remove", "upgrade", "autoremove"}, egress: []string{"update"}},
	"pip":   {read: []string{"list", "freeze", "show", "check", "--version", "-V", "debug", "inspect", "help", "index", "config"}, install: []string{"install", "uninstall", "download", "wheel"}, values: []string{"-r", "--requirement", "-c", "--constraint", "-i", "--index-url", "--extra-index-url", "-t", "--target", "--prefix", "--root"}},
	"npm":   {read: []string{"ls", "list", "ll", "la", "view", "info", "show", "v", "outdated", "--version", "-v", "root", "prefix", "why", "explain", "search", "help", "doctor", "fund", "config", "get"}, install: []string{"install", "i", "uninstall", "remove", "rm", "update", "ci", "link", "add", "un", "up", "upgrade", "dedupe", "prune", "rebuild"}},
	"yarn":  {read: []string{"list", "info", "why", "--version", "outdated", "licenses", "config"}, install: []string{"add", "remove", "upgrade", "install", "global", "link"}},
	"pnpm":  {read: []string{"list", "ls", "why", "outdated", "--version", "licenses", "root"}, install: []string{"add", "remove", "update", "install", "i", "link", "rm", "up"}},
	"gem":   {read: []string{"list", "query", "search", "info", "specification", "contents", "environment", "--version", "which", "outdated", "help", "dependency"}, install: []string{"install", "uninstall", "update", "cleanup", "pristine"}},
	"cargo": {read: []string{"--version", "version", "tree", "metadata", "search", "pkgid", "locate-project", "help", "--list"}, install: []string{"install", "uninstall"}},
	"go":    {read: []string{"version", "list", "doc", "help"}, install: []string{"install", "get"}},
	"composer": {read: []string{"show", "info", "outdated", "licenses", "depends", "why", "why-not", "--version", "diagnose", "search", "validate", "check-platform-reqs"},
		install: []string{"install", "require", "remove", "update", "upgrade", "global", "create-project"}},
	"luarocks":       {read: []string{"list", "search", "show", "doc", "path", "config", "--version"}, install: []string{"install", "remove", "build", "make"}},
	"ansible-galaxy": {read: []string{"list", "info", "search", "--version"}, install: []string{"install", "collection", "role"}},
	"nix-env":        {install: []string{"-i", "--install", "-e", "--uninstall", "-u", "--upgrade"}},
}

func register_package_rules() {
	for name, p := range pkg_policies {
		name, p := name, p
		read, install, egress := set_of(p.read...), set_of(p.install...), set_of(p.egress...)
		risk_rules[name] = func(rc *risk_ctx, inv *invocation) {
			pa := parse_args(inv.args, p.values...)
			// Flag-style verbs ("--version", nix-env "-i").
			for _, f := range pa.flags {
				if install[f] {
					rc.flag(RiskPkgInstall, fmt.Sprintf("%s %s installs or removes software", name, f))
					return
				}
			}
			v := pa.verb()
			switch {
			case install[v]:
				// Exact match only. A prefix test would catch "installed" in
				// "apt list --installed", which is the read this split exists
				// to leave alone.
				rc.flag(RiskPkgInstall, fmt.Sprintf("%s %s installs or removes software", name, v))
			case egress[v]:
				rc.flag(RiskNetEgress, fmt.Sprintf("%s %s refreshes package lists from the network", name, v))
			case v == "" || read[v]:
				if name == "go" && v == "" {
					return
				}
				if name == "npm" && v == "config" && len(pa.ops) > 1 && pa.ops[1].text != "get" && pa.ops[1].text != "list" && pa.ops[1].text != "ls" {
					rc.unverified("npm config " + pa.ops[1].text + " changes configuration")
				}
			case name == "go" && v == "env":
				if pa.has("-w", "-u") {
					rc.unverified("go env -w changes configuration")
				}
			default:
				rc.unverified(fmt.Sprintf("%s %s is not a read the gate recognizes (it may run project code)", name, v))
			}
		}
	}
	risk_rules["dpkg"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "--admindir", "--root", "--instdir")
		if pa.has("-i", "--install", "-r", "--remove", "-P", "--purge", "--unpack", "--configure", "-a", "--set-selections", "--clear-selections", "--update-avail", "--merge-avail") {
			rc.flag(RiskPkgInstall, "dpkg installs or removes software")
			return
		}
		if len(pa.flags) == 0 && len(pa.ops) > 0 {
			rc.unverified("dpkg with no action")
		}
	}
	risk_rules["rpm"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "--root", "--dbpath", "--define", "-D", "--eval", "--qf", "--queryformat")
		if pa.has("-i", "--install", "-U", "--upgrade", "-e", "--erase", "-F", "--freshen", "--import", "--rebuilddb", "--initdb", "--setperms", "--setugids", "--restore") {
			rc.flag(RiskPkgInstall, "rpm installs or removes software")
			return
		}
		if pa.has_prefix("-q") || pa.has("--query", "-V", "--verify", "--version", "-K", "--checksig", "--showrc", "--querytags") {
			return
		}
		rc.unverified("rpm invocation the gate cannot read")
	}
	risk_rules["pacman"] = func(rc *risk_ctx, inv *invocation) {
		for _, f := range parse_args(inv.args).flags {
			switch {
			case strings.HasPrefix(f, "-Q"), strings.HasPrefix(f, "-F") && !strings.ContainsRune(f, 'y'),
				f == "-Ss", f == "-Si", f == "-Sl", f == "-Sg", f == "-Sii", f == "-V", f == "--version", f == "--query":
			case strings.HasPrefix(f, "-S"), strings.HasPrefix(f, "-R"), strings.HasPrefix(f, "-U"),
				f == "--sync", f == "--remove", f == "--upgrade", strings.HasPrefix(f, "-D"), f == "--database":
				rc.flag(RiskPkgInstall, "pacman "+f+" installs or removes software")
				return
			}
		}
	}
	risk_rules["emerge"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args)
		if pa.has("-s", "--search", "-S", "--searchdesc", "--info", "--version", "-p", "--pretend", "-h", "--help") {
			return
		}
		rc.flag(RiskPkgInstall, "emerge installs or removes software")
	}
	risk_rules["cpan"] = func(rc *risk_ctx, inv *invocation) {
		if pa := parse_args(inv.args); pa.has("-l", "-D", "-v", "-V") && !pa.has("-i", "-f", "-T") {
			return
		}
		rc.flag(RiskPkgInstall, "cpan installs software")
	}
}

// ---------------------------------------------------------------- data

// sql_client describes how one SQL client takes its SQL.
type sql_client struct {
	sql_opts    []string // options whose value is SQL
	file_opts   []string // options that run a file the gate cannot see
	write_opts  []string // options that write their value as a file
	value_opts  []string // other options that take a value
	positional  bool     // operands after the first (the database) are SQL
	password_op bool     // bare -p prompts for a password (mysql)
}

var sql_clients = map[string]sql_client{
	"psql": {sql_opts: []string{"-c", "--command"}, file_opts: []string{"-f", "--file"}, write_opts: []string{"-o", "--output", "-L", "--log-file"},
		value_opts: []string{"-h", "--host", "-p", "--port", "-U", "--username", "-d", "--dbname", "-v", "--set", "--variable", "-P", "--pset", "-F", "--field-separator", "-R", "--record-separator", "-T", "--table-attr"}},
	"mysql": {sql_opts: []string{"-e", "--execute", "--init-command"}, file_opts: []string{"--init-file"}, write_opts: []string{"--tee"},
		value_opts: []string{"-h", "--host", "-P", "--port", "-u", "--user", "-D", "--database", "-S", "--socket", "--defaults-file", "--defaults-extra-file", "--default-character-set", "--protocol", "--ssl-ca", "--ssl-cert", "--ssl-key"}, password_op: true},
	"sqlite3": {sql_opts: []string{"-cmd"}, file_opts: []string{"-init"}, value_opts: []string{"-separator", "-newline", "-nullvalue", "-vfs", "-maxsize", "-mmap", "-pagecache", "-lookaside", "-heap"}, positional: true},
	"duckdb":  {sql_opts: []string{"-c", "-s", "-cmd"}, file_opts: []string{"-f", "-init"}, value_opts: []string{"-separator", "-newline", "-nullvalue"}, positional: true},
	"clickhouse-client": {sql_opts: []string{"-q", "--query"}, file_opts: []string{"--queries-file"},
		value_opts: []string{"-h", "--host", "--port", "-u", "--user", "--password", "-d", "--database", "-f", "--format", "--config-file", "-C"}},
	"cqlsh": {sql_opts: []string{"-e", "--execute"}, file_opts: []string{"-f", "--file"}, value_opts: []string{"-u", "--username", "-p", "--password", "-k", "--keyspace", "--cqlshrc", "--encoding", "--cqlversion", "--connect-timeout", "--request-timeout"}},
	"usql":  {sql_opts: []string{"-c", "--command"}, file_opts: []string{"-f", "--file"}, write_opts: []string{"-o", "--out"}, value_opts: []string{"-v", "--set", "-P", "--pset", "-F", "-R"}},
}

func init() {
	sql_clients["mariadb"] = sql_clients["mysql"]
	sql_clients["sqlite"] = sql_clients["sqlite3"]
}

// sql_mutations are the statement keywords that change data or schema.
var sql_mutations = []string{
	"insert", "update", "delete", "drop", "truncate", "alter", "create",
	"replace", "grant", "revoke", "merge", "upsert",
}

// sql_disqualifiers make an otherwise read-shaped statement do something:
// write a file, call a procedure, run a server-side action.
var sql_disqualifiers = []string{
	"into", "outfile", "dumpfile", "copy", "call", "exec", "execute", "do", "lock", "vacuum", "analyze",
	"reindex", "cluster", "refresh", "load", "handler", "kill", "shutdown", "flush", "purge", "reset",
	"import", "attach", "detach", "notify", "discard", "prepare", "security", "comment", "install", "uninstall",
	"pg_terminate_backend", "pg_cancel_backend", "pg_reload_conf", "pg_rotate_logfile", "pg_switch_wal",
	"pg_create_restore_point", "pg_promote", "set_config", "setval", "nextval", "lo_import", "lo_export",
	"lo_unlink", "pg_file_write", "dblink", "dblink_exec", "sys_exec", "sys_eval", "xp_cmdshell", "global", "persist",
}

// sql_read_starts are the statements that only read.
var sql_read_starts = set_of("select", "show", "explain", "describe", "desc", "with", "values", "table",
	"pragma", "help", "use", "set", "begin", "start", "commit", "rollback", "end", "abort", "exit", "quit", "status")

// sql_risk classifies SQL text given to a client. Only statements the gate
// can read as queries pass; anything else is a database write as far as the
// operator is concerned.
func (rc *risk_ctx) sql_risk(client, sql string) {
	for _, line := range strings.Split(sql, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			continue
		case strings.HasPrefix(t, "\\"):
			rc.sql_meta(client, t)
			continue
		case strings.HasPrefix(t, ".") && (client == "sqlite3" || client == "sqlite" || client == "duckdb"):
			rc.sqlite_dot(t)
			continue
		}
		for _, stmt := range strings.Split(t, ";") {
			rc.sql_statement(client, stmt)
		}
	}
}

func (rc *risk_ctx) sql_statement(client, stmt string) {
	s := strings.TrimSpace(stmt)
	for strings.HasPrefix(s, "--") || strings.HasPrefix(s, "/*") {
		if strings.HasPrefix(s, "--") {
			return // the rest of the line is a comment
		}
		end := strings.Index(s, "*/")
		if end < 0 {
			return
		}
		s = strings.TrimSpace(s[end+2:])
	}
	if s == "" {
		return
	}
	lower := strings.ToLower(s)
	first := lower
	if k := strings.IndexFunc(lower, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r == '_') }); k >= 0 {
		first = lower[:k]
	}
	switch {
	case (first == "system" || first == "pager") && (client == "mysql" || client == "mariadb"):
		rest := strings.TrimSpace(s[len(first):])
		if first == "pager" {
			rc.unverified("mysql pager runs a program")
			return
		}
		rc.line(rest)
		return
	case first == "source" || first == "tee":
		rc.flag(RiskDataMutate, "runs or writes a file the gate cannot see: "+first)
		return
	}
	if containsAnyWord(lower, sql_mutations) {
		rc.flag(RiskDataMutate, "database write via "+client)
		return
	}
	if !sql_read_starts[first] || containsAnyWord(lower, sql_disqualifiers) || (first == "pragma" && strings.Contains(s, "=")) {
		rc.flag(RiskDataMutate, "SQL the gate cannot confirm is read-only, via "+client)
	}
}

// psql / mysql backslash commands.
func (rc *risk_ctx) sql_meta(client, t string) {
	word := t
	if k := strings.IndexAny(t, " \t"); k > 0 {
		word = t[:k]
	}
	rest := strings.TrimSpace(t[len(word):])
	switch {
	case word == "\\!":
		rc.line(rest)
	case word == "\\i" || word == "\\ir" || word == "\\include" || word == "\\." || word == "\\gexec" || word == "\\copy" ||
		word == "\\o" || word == "\\out" || word == "\\w" || word == "\\write" || word == "\\T" || word == "\\P" || word == "\\e" || word == "\\edit":
		rc.flag(RiskDataMutate, client+" "+word+" runs or writes something the gate cannot see")
	case word == "\\g" && rest != "":
		rc.flag(RiskDataMutate, client+" \\g writes query output to a file or command")
	case strings.HasPrefix(word, "\\d"), strings.HasPrefix(word, "\\l"), word == "\\x", word == "\\q", word == "\\conninfo",
		word == "\\timing", word == "\\pset", word == "\\z", word == "\\echo", word == "\\?", word == "\\h", word == "\\help",
		word == "\\encoding", word == "\\sf", word == "\\sv", word == "\\c", word == "\\connect", word == "\\set", word == "\\unset",
		word == "\\errverbose", word == "\\a", word == "\\t", word == "\\H", word == "\\f", word == "\\C", word == "\\G",
		word == "\\s", word == "\\u", word == "\\g":
	default:
		rc.flag(RiskDataMutate, client+" command the gate does not recognize: "+word)
	}
}

// sqlite / duckdb dot-commands.
func (rc *risk_ctx) sqlite_dot(t string) {
	word := strings.Fields(t)[0]
	switch word {
	case ".tables", ".schema", ".indexes", ".indices", ".databases", ".dbinfo", ".mode", ".headers", ".header",
		".width", ".show", ".help", ".fullschema", ".quit", ".exit", ".timer", ".eqp", ".explain", ".nullvalue",
		".separator", ".dump", ".stats", ".print", ".echo", ".bail", ".changes", ".lint", ".expert", ".columns", ".maxrows":
	case ".shell", ".system":
		rc.line(strings.TrimSpace(strings.TrimPrefix(t, word)))
	default:
		rc.flag(RiskDataMutate, "sqlite "+word+" reads, writes or runs something the gate cannot see")
	}
}

// redis_reads are the redis-cli commands that only read.
var redis_reads = set_of("get", "mget", "strlen", "getrange", "exists", "type", "ttl", "pttl", "keys", "scan",
	"hget", "hgetall", "hkeys", "hvals", "hlen", "hexists", "hmget", "hscan", "hstrlen", "hrandfield",
	"lrange", "llen", "lindex", "lpos", "smembers", "scard", "sismember", "smismember", "srandmember", "sscan",
	"sinter", "sunion", "sdiff", "zrange", "zrangebyscore", "zrevrange", "zrevrangebyscore", "zrangebylex",
	"zcard", "zscore", "zmscore", "zrank", "zrevrank", "zcount", "zscan", "zlexcount", "zrandmember",
	"xrange", "xrevrange", "xlen", "xinfo", "xpending", "info", "ping", "echo", "dbsize", "time", "lastsave",
	"role", "auth", "hello", "select", "bitcount", "bitpos", "getbit", "pfcount", "geopos", "geodist",
	"geohash", "geosearch", "georadius_ro", "georadiusbymember_ro", "sort_ro", "monitor", "randomkey",
	"dump", "touch", "memory", "latency", "slowlog", "client", "config", "object", "command", "cluster",
	"pubsub", "module", "acl", "lolwut", "readonly", "quit", "exit", "help", "wait")

// redis_read_subs: for the commands above that take a sub-command, the ones
// that read.
var redis_read_subs = map[string]map[string]bool{
	"memory":  set_of("usage", "stats", "doctor", "malloc-stats", "help"),
	"latency": set_of("latest", "history", "doctor", "graph", "histogram", "help"),
	"slowlog": set_of("get", "len", "help"),
	"client":  set_of("list", "info", "getname", "id", "getredir", "trackinginfo", "help"),
	"config":  set_of("get", "help"),
	"object":  set_of("encoding", "freq", "idletime", "refcount", "help"),
	"command": set_of("count", "info", "docs", "list", "getkeys", "help"),
	"cluster": set_of("info", "nodes", "slots", "shards", "myid", "keyslot", "countkeysinslot", "getkeysinslot", "links", "help"),
	"pubsub":  set_of("channels", "numsub", "numpat", "shardchannels", "shardnumsub", "help"),
	"module":  set_of("list", "help"),
	"acl":     set_of("whoami", "list", "users", "cat", "getuser", "log", "help"),
}

// redis_command classifies one redis command given as words.
func (rc *risk_ctx) redis_command(words []string) {
	if len(words) == 0 {
		return
	}
	verb := strings.ToLower(words[0])
	if !redis_reads[verb] {
		rc.flag(RiskDataMutate, "redis write via redis-cli: "+verb)
		return
	}
	if subs, ok := redis_read_subs[verb]; ok {
		sub := ""
		if len(words) > 1 {
			sub = strings.ToLower(words[1])
		}
		if !subs[sub] {
			rc.flag(RiskDataMutate, "redis write via redis-cli: "+verb+" "+sub)
		}
	}
}

// mongo_read_calls are the functions a read-only mongo shell script may call.
var mongo_read_calls = set_of("find", "findone", "count", "countdocuments", "estimateddocumentcount", "distinct",
	"aggregate", "getindexes", "stats", "explain", "getcollectionnames", "getcollectioninfos", "serverstatus",
	"version", "hostinfo", "currentop", "getname", "getusers", "ismaster", "hello", "status", "conf", "config",
	"printreplicationinfo", "printsecondaryreplicationinfo", "print", "printjson", "stringify", "tojson",
	"toarray", "limit", "sort", "skip", "pretty", "itcount", "size", "getsiblingdb", "getmongo", "getdbnames",
	"getcollection", "objectid", "isodate", "date", "numberlong", "numberint", "numberdecimal", "hint",
	"batchsize", "maxtimems", "collation", "readpref", "next", "hasnext", "keys", "tostring", "projection",
	"getreplicationinfo", "isreplicasetmember", "getprofilinglevel", "getprofilingstatus", "getlogcomponents",
	"listcollections", "getcollectionstats", "datasize", "storagesize", "totalindexsize", "totalsize")

var js_call = regexp.MustCompile(`([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)

// mongo_risk: a script is read-only when every function it calls is a read.
func (rc *risk_ctx) mongo_risk(js string) {
	lower := strings.ToLower(js)
	if containsAnyWord(lower, mongo_mutations) || strings.Contains(lower, "$out") || strings.Contains(lower, "$merge") ||
		strings.Contains(lower, "$function") || strings.Contains(lower, "$where") || strings.Contains(lower, "$accumulator") {
		rc.flag(RiskDataMutate, "database write via mongo")
		return
	}
	for _, m := range js_call.FindAllStringSubmatch(js, -1) {
		if !mongo_read_calls[strings.ToLower(m[1])] {
			rc.flag(RiskDataMutate, "mongo script calls "+m[1]+", which the gate does not know as a read")
			return
		}
	}
}

// mongo_mutations: the words that change data in a mongo script.
var mongo_mutations = []string{
	"insert", "insertone", "insertmany", "update", "delete", "remove", "drop", "replaceone",
	"deleteone", "deletemany", "updateone", "updatemany", "createcollection", "createindex", "createindexes",
	"dropdatabase", "dropindex", "dropindexes", "renamecollection", "bulkwrite", "findandmodify",
	"findoneandupdate", "findoneanddelete", "findoneandreplace", "save", "runcommand", "admincommand",
	"eval", "load", "runprogram", "run", "shutdownserver", "fsynclock", "createuser", "dropuser",
	"updateuser", "grantrolestouser", "revokerolesfromuser", "createrole", "droprole", "killop",
	"stepdown", "reconfig", "compact", "repairdatabase", "setprofilinglevel", "clonecollection",
}

func register_data_rules() {
	for name, cl := range sql_clients {
		name, cl := name, cl
		risk_rules[name] = func(rc *risk_ctx, inv *invocation) {
			tv := append(append(append(append([]string{}, cl.sql_opts...), cl.file_opts...), cl.write_opts...), cl.value_opts...)
			pa := parse_args(inv.args, tv...)
			if _, ok := pa.value(cl.file_opts...); ok {
				rc.flag(RiskDataMutate, name+" runs a SQL file the gate cannot see")
				return
			}
			for _, o := range cl.write_opts {
				if w, ok := pa.value(o); ok {
					rc.write(w, false)
				}
			}
			var sql []string
			for i, a := range inv.args {
				for _, o := range cl.sql_opts {
					switch {
					case a.text == o && i+1 < len(inv.args):
						sql = append(sql, inv.args[i+1].text)
					case strings.HasPrefix(a.text, o+"="):
						sql = append(sql, strings.TrimPrefix(a.text, o+"="))
					case len(o) == 2 && o[1] != '-' && strings.HasPrefix(a.text, o) && len(a.text) > 2 && !strings.HasPrefix(a.text, "--"):
						sql = append(sql, a.text[2:]) // -e"SELECT 1"
					}
				}
			}
			if cl.positional && len(pa.ops) > 1 {
				for _, o := range pa.ops[1:] {
					sql = append(sql, o.text)
				}
			}
			for _, s := range sql {
				rc.sql_risk(name, s)
			}
			if inv.stdin_fed() {
				if inv.cmd.stdin != "" && !inv.cmd.piped_in {
					rc.sql_risk(name, inv.cmd.stdin)
				} else {
					rc.flag(RiskDataMutate, name+" reads SQL from its input, which the gate cannot see")
				}
			}
		}
	}
	risk_rules["redis-cli"] = func(rc *risk_ctx, inv *invocation) {
		pa := parse_args(inv.args, "-h", "-p", "-s", "-a", "-u", "-n", "-r", "-i", "-d", "-D", "--user", "--pass",
			"--eval", "--rdb", "--functions-rdb", "--pattern", "--count", "--quoted-pattern", "--tls-cert", "--tls-key", "--cacert", "--sni")
		if _, ok := pa.value("--eval"); ok {
			rc.flag(RiskDataMutate, "redis-cli --eval runs a Lua script")
			return
		}
		if w, ok := pa.value("--rdb", "--functions-rdb"); ok {
			rc.write(w, false)
		}
		if pa.has("--pipe", "-x") {
			rc.flag(RiskDataMutate, "redis-cli reads commands or values from its input")
			return
		}
		if len(pa.ops) > 0 {
			rc.redis_command(op_texts(pa.ops))
			return
		}
		if inv.stdin_fed() {
			if inv.cmd.stdin != "" && !inv.cmd.piped_in {
				for _, l := range strings.Split(inv.cmd.stdin, "\n") {
					rc.redis_command(strings.Fields(l))
				}
				return
			}
			rc.flag(RiskDataMutate, "redis-cli reads commands from its input, which the gate cannot see")
		}
	}
	for _, n := range []string{"mongo", "mongosh"} {
		risk_rules[n] = func(rc *risk_ctx, inv *invocation) {
			pa := parse_args(inv.args, "--eval", "--host", "--port", "-u", "--username", "-p", "--password",
				"--authenticationDatabase", "--authenticationMechanism", "--tlsCAFile", "--tlsCertificateKeyFile", "-f", "--file")
			if _, ok := pa.value("-f", "--file"); ok {
				rc.flag(RiskDataMutate, inv.name+" runs a script file the gate cannot see")
				return
			}
			for _, o := range pa.ops {
				if strings.HasSuffix(o.text, ".js") {
					rc.flag(RiskDataMutate, inv.name+" runs a script file the gate cannot see")
					return
				}
			}
			if e, ok := pa.value("--eval"); ok {
				rc.mongo_risk(e.text)
			}
			if inv.stdin_fed() {
				if inv.cmd.stdin != "" && !inv.cmd.piped_in {
					rc.mongo_risk(inv.cmd.stdin)
				} else {
					rc.flag(RiskDataMutate, inv.name+" reads a script from its input, which the gate cannot see")
				}
			}
		}
	}
}

// ---------------------------------------------------------------- interpreters

var shells = set_of("bash", "sh", "dash", "zsh", "ksh", "mksh", "ash", "fish", "tcsh", "csh", "rbash")

var interpreters = set_of("python", "perl", "ruby", "node", "deno", "bun", "php", "lua", "luajit", "tclsh",
	"wish", "Rscript", "R", "julia", "java", "groovy", "scala", "kotlin", "pwsh", "powershell", "osascript",
	"expect", "irb", "pry", "ghci", "erl", "elixir", "iex", "jshell", "guile", "racket", "sbcl", "clisp")

func register_interpreter_rules() {
	for n := range shells {
		risk_rules[n] = shell_rule
	}
	for n := range interpreters {
		risk_rules[n] = interpreter_rule
	}
	risk_rules["eval"] = func(rc *risk_ctx, inv *invocation) {
		rc.line(strings.Join(op_texts(inv.args), " "))
	}
	for _, n := range []string{"source", "."} {
		risk_rules[n] = func(rc *risk_ctx, inv *invocation) {
			rc.unverified("runs a script the gate cannot read")
		}
	}
	risk_rules["trap"] = func(rc *risk_ctx, inv *invocation) {
		if len(inv.args) > 1 {
			rc.line(inv.args[0].text) // the action runs later, but it runs
		}
	}
	risk_rules["export"] = func(rc *risk_ctx, inv *invocation) {
		for _, a := range inv.args {
			if is_assignment(a) {
				rc.assignment(a)
			}
		}
	}
}

// shell_rule: "bash -c STRING" is classified as the line it runs. A script
// file, or commands arriving on stdin from a pipe, cannot be read, so they
// ask. A heredoc body is right there, so it is classified like any line.
func shell_rule(rc *risk_ctx, inv *invocation) {
	args := inv.args
	for i := 0; i < len(args); i++ {
		t := args[i].text
		switch {
		case t == "-c":
			if i+1 >= len(args) {
				return
			}
			if args[i+1].expanded {
				rc.unverified(inv.name + " -c runs a command string built at run time")
				return
			}
			rc.line(args[i+1].text)
			return
		case t == "--version" || t == "--help":
			return
		case t == "-o" || t == "+o" || t == "--rcfile" || t == "--init-file":
			if t == "--rcfile" || t == "--init-file" {
				rc.unverified(inv.name + " " + t + " runs a file the gate cannot read")
				return
			}
			i++
		case strings.HasPrefix(t, "-") || strings.HasPrefix(t, "+"):
			if strings.ContainsRune(t, 'c') && !strings.HasPrefix(t, "--") {
				if i+1 < len(args) {
					rc.line(args[i+1].text)
				}
				return
			}
		default:
			rc.unverified(inv.name + " runs a script the gate cannot read: " + t)
			return
		}
	}
	if inv.cmd != nil && inv.cmd.stdin != "" && !inv.cmd.piped_in {
		rc.line(inv.cmd.stdin)
		return
	}
	if inv.stdin_fed() {
		rc.unverified(inv.name + " runs commands from its input, which the gate cannot see")
	}
	// No script and no input: an interactive shell, gated line by line.
}

// interpreter_rule: a script or inline program is code the gate cannot read.
// Only version queries and a couple of well-known read-only modules pass.
func interpreter_rule(rc *risk_ctx, inv *invocation) {
	pa := parse_args(inv.args, "-m")
	if len(inv.args) == 1 {
		switch inv.args[0].text {
		case "--version", "-V", "-v", "-version", "--help", "-h":
			return
		}
	}
	if m, ok := pa.value("-m"); ok && inv.name == "python" {
		switch m.text {
		case "json.tool":
			if len(pa.ops) > 1 {
				rc.write(pa.ops[1], false)
			}
			return
		case "pip":
			// "python -m pip ..." is pip.
			i := 0
			for i < len(inv.args) && inv.args[i].text != "-m" {
				i++
			}
			rc.invoke(append([]sh_word{{text: "pip", raw: "pip"}}, inv.args[min(i+2, len(inv.args)):]...), inv.cmd, inv.more)
			return
		}
	}
	rc.unverified(inv.name + " runs code the gate cannot read")
}

// containsAnyWord reports whether any word appears in hay as a whole token
// (bounded by non-identifier characters).
func containsAnyWord(hay string, words []string) bool {
	for _, w := range words {
		if wordPresent(hay, w) {
			return true
		}
	}
	return false
}

// wordPresent reports whether w occurs in hay bounded by non-identifier
// characters on both sides, so "delete" matches "delete from t" but not
// "deleted_at".
func wordPresent(hay, w string) bool {
	from := 0
	for {
		j := strings.Index(hay[from:], w)
		if j < 0 {
			return false
		}
		j += from
		beforeOK := j == 0 || !isIdentByte(hay[j-1])
		end := j + len(w)
		afterOK := end >= len(hay) || !isIdentByte(hay[end])
		if beforeOK && afterOK {
			return true
		}
		from = j + 1
	}
}

func isIdentByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// cmd_base returns the basename of a possibly path-prefixed command.
// "/usr/bin/rm" -> "rm", "rm" -> "rm".
func cmd_base(s string) string {
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		return s[idx+1:]
	}
	return s
}

// shell_segments returns the text of each simple command in cmd - used where
// only a rough per-command view is needed (loop counters, the watch
// allowlist), never for a risk decision.
func shell_segments(cmd string) []string {
	var out []string
	for _, c := range sh_lex(cmd).cmds {
		var parts []string
		for _, w := range c.words {
			parts = append(parts, w.raw)
		}
		if s := strings.TrimSpace(strings.Join(parts, " ")); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parse_cmd returns the effective program name and arguments of one segment,
// looking through the common wrappers. For counting, not for gating.
func parse_cmd(seg string) (name string, args []string) {
	p := sh_lex(seg)
	if len(p.cmds) == 0 {
		return "", nil
	}
	words := p.cmds[0].words
	for len(words) > 0 && is_assignment(words[0]) {
		words = words[1:]
	}
	for len(words) > 0 {
		base := cmd_base(words[0].text)
		switch base {
		case "sudo", "nice", "nohup", "env", "command", "time", "timeout", "doas", "stdbuf", "ionice":
			words = words[1:]
			for len(words) > 0 && (strings.HasPrefix(words[0].text, "-") || is_assignment(words[0])) {
				words = words[1:]
			}
			continue
		}
		return base, op_texts(words[1:])
	}
	return "", nil
}

// redirect_target is one file a command writes to.
type redirect_target struct {
	path    string
	appends bool // ">>" rather than ">"
}

// redirect_targets returns every file path a line WRITES to through shell
// redirections (>, >>, 2>, &>) and tee. Descriptor duplications (2>&1, >&2)
// write to an existing stream, not a file.
func redirect_targets(line string) []redirect_target {
	var out []redirect_target
	for _, c := range sh_lex(line).cmds {
		for _, r := range c.redirects {
			switch r.op {
			case "<", "<<", "<<-", "<<<", "<&":
				continue
			case ">&":
				if isAllDigits(r.target.text) || r.target.text == "-" {
					continue
				}
			}
			out = append(out, redirect_target{path: r.target.text, appends: strings.Contains(r.op, ">>")})
		}
		if len(c.words) > 0 && cmd_base(c.words[0].text) == "tee" {
			pa := parse_args(c.words[1:])
			for _, w := range pa.ops {
				out = append(out, redirect_target{path: w.text, appends: pa.has("-a", "--append")})
			}
		}
	}
	return out
}
