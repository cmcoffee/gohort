package servitor

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// runQuickSnapshot runs a fixed set of enumeration commands and returns formatted markdown.
// No LLM — just captures the system's fingerprint so the investigator can decide what to probe.
// snapshotSection is one block of the quick snapshot: a label the model reads,
// the shell that produces it, and the exec batch it travels in.
type snapshotSection struct {
	label, cmd string
	batch      int
}

// snapshotSections is the fixed reconnaissance set. Every command already
// swallows its own stderr and tolerates a missing tool, so a section that
// yields nothing is simply omitted from the snapshot.
//
// Batches group sections into ONE exec each. The old shape ran one SSH session
// per section — nine back to back — and over a slow link or a peer hop the
// session setup dominated the commands themselves. A single script for all
// nine would be simplest, but execOverSSH caps one exec's output at max_output
// and a real process tree plus port list already approaches it, so the sections
// are grouped by expected size: small outputs together, the bulky ones alone.
var snapshotSections = []snapshotSection{
	{"OS & Identity", `uname -a; hostname; cat /etc/os-release 2>/dev/null | grep -E '^(NAME|VERSION|PRETTY_NAME)' | head -5`, 0},
	{"Resources", `nproc; free -h; df -h --output=target,size,avail 2>/dev/null | head -10`, 0},
	{"Running Services", `systemctl list-units --type=service --state=running --no-pager --no-legend 2>/dev/null | awk '{print $1}' | head -40`, 1},
	{"Listening Ports", `ss -tlnp 2>/dev/null | head -30`, 1},
	{"Key Directories", `ls /opt/ /srv/ /var/www/ /home/ /app/ 2>/dev/null; ls /etc/nginx /etc/apache2 /etc/httpd /etc/php* 2>/dev/null`, 0},
	{"App Config Files", `find /opt /srv /var/www /home -maxdepth 5 \( -name 'docker-compose.yml' -o -name '.env' -o -name 'package.json' -o -name 'go.mod' -o -name 'requirements.txt' -o -name 'Gemfile' \) 2>/dev/null | head -25`, 0},
	{"Databases", `which mysql psql redis-cli mongosh mongo sqlite3 2>/dev/null; ss -tlnp 2>/dev/null | grep -E ':3306|:5432|:6379|:27017|:9200'`, 0},
	{"Containers", `docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Ports}}\t{{.Status}}' 2>/dev/null`, 1},
	{"Process Tree", `ps auxf 2>/dev/null | head -50`, 2},
}

// snapshotBatches is how many execs a full snapshot takes: one per distinct
// batch number above.
const snapshotBatches = 3

// snapshotMarker separates sections in a batch's combined output. The shell
// prints it before each section and it never appears in real command output,
// so splitting on it recovers each section exactly.
const snapshotMarker = "@@SNAPSHOT-SECTION@@"

// snapshotScript joins one batch's sections into a single shell script, each
// section preceded by a marker line carrying its index. Sections are joined
// with ";" rather than "&&" so a failing section (no systemd, no docker) never
// suppresses the ones after it — the same tolerance the per-section loop had
// when it skipped an erroring exec.
func snapshotScript(batch int) string {
	var b strings.Builder
	for i, s := range snapshotSections {
		if s.batch != batch {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "echo '%s%d'; %s", snapshotMarker, i, s.cmd)
	}
	return b.String()
}

// snapshotBodies splits a batch's combined output into per-section bodies,
// keyed by section index. A section whose marker is missing (the batch's exec
// died early) or whose body is empty is absent from the result.
func snapshotBodies(raw string) map[int]string {
	bodies := make(map[int]string)
	for i := range snapshotSections {
		open := snapshotMarker + strconv.Itoa(i) + "\n"
		start := strings.Index(raw, open)
		if start < 0 {
			continue
		}
		body := raw[start+len(open):]
		if end := strings.Index(body, snapshotMarker); end >= 0 {
			body = body[:end]
		}
		if body = strings.TrimSpace(body); body != "" {
			bodies[i] = body
		}
	}
	return bodies
}

// renderSnapshot writes the collected bodies as labelled markdown blocks in
// snapshotSections order, so the snapshot reads exactly as the nine-session
// version did regardless of which batch each section came from.
func renderSnapshot(bodies map[int]string) string {
	var out strings.Builder
	for i, s := range snapshotSections {
		body, ok := bodies[i]
		if !ok {
			continue
		}
		out.WriteString(fmt.Sprintf("### %s\n```\n%s\n```\n\n", s.label, body))
	}
	return out.String()
}

// runQuickSnapshot gathers the no-LLM reconnaissance pass in snapshotBatches
// execs. A batch whose exec errors contributes nothing, exactly as a failing
// section was skipped before; the investigator starts from whatever came back.
func runQuickSnapshot(ctx context.Context, execFn func(string) (string, error)) string {
	bodies := make(map[int]string)
	for batch := 0; batch < snapshotBatches; batch++ {
		if ctx.Err() != nil {
			break
		}
		result, err := execFn(snapshotScript(batch))
		if err != nil {
			continue
		}
		for i, body := range snapshotBodies(result) {
			bodies[i] = body
		}
	}
	return renderSnapshot(bodies)
}
