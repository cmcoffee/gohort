// Package servitor provides an autonomous agent that connects to a remote
// Linux host via SSH and produces a structured profile of the appliance.
// The LLM drives the exploration — it decides which commands to run, what
// to investigate, and synthesizes everything into a Markdown report.
//
// Destructive commands (rm, dd, shutdown, systemctl stop, etc.) are
// automatically detected and require explicit user authorization before
// execution. Safe read-only probe commands run freely.
package servitor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"golang.org/x/crypto/ssh"
)

// Registration is in web.go so the agent is available as both a CLI app and a web app.

// max_output is the per-command output cap (characters). Commands that return
// more are truncated to protect the LLM context window.
const max_output = 10000

// command_timeout is the per-command wall-clock cap. Without this an LLM that
// runs `tail -f`, `journalctl -f`, `top`, or any other non-terminating command
// would block the worker goroutine forever — the agent loop sits inside the
// tool handler and the user only sees heartbeat status events.
const command_timeout = 90 * time.Second

// always_destructive holds commands considered dangerous regardless of
// arguments. Any command in this set requires user authorization.
var always_destructive = map[string]bool{
	"rm": true, "rmdir": true, "shred": true, "wipe": true,
	"dd": true, "mkfs": true, "mke2fs": true, "mkntfs": true, "mkswap": true,
	"fdisk": true, "gdisk": true, "parted": true, "sfdisk": true, "cfdisk": true,
	"kill": true, "killall": true, "pkill": true, "skill": true,
	"shutdown": true, "reboot": true, "halt": true, "poweroff": true,
	"truncate": true,
	"userdel": true, "deluser": true, "groupdel": true, "delgroup": true,
	"passwd": true, "chpasswd": true, "usermod": true,
	"visudo": true, "sudoedit": true,
	"insmod": true, "rmmod": true,
}

// destructive_verbs maps commands to sub-verb arguments that make them
// destructive. "systemctl status" is safe; "systemctl stop" is not.
var destructive_verbs = map[string][]string{
	"systemctl":      {"stop", "kill", "disable", "mask", "reset-failed"},
	"service":        {"stop"},
	"iptables":       {"-F", "--flush", "-X", "-Z", "--delete-chain"},
	"ip6tables":      {"-F", "--flush", "-X", "-Z"},
	"nft":            {"flush", "delete"},
	"firewall-cmd":   {"--remove-service", "--remove-port", "--remove-rule", "--panic-on"},
	"modprobe":       {"-r", "--remove"},
	// Container / orchestration tools
	"kubectl":        {"delete", "drain", "cordon", "taint", "replace", "rollout"},
	"terraform":      {"destroy", "apply"},
	"helm":           {"uninstall", "delete", "rollback"},
	"docker":         {"rm", "rmi", "stop", "kill", "prune"},
	"docker-compose": {"down", "rm", "kill"},
	"podman":         {"rm", "rmi", "stop", "kill"},
	"git":            {"clean", "reset", "push"},
	"ansible":        {"playbook"},
	"flux":           {"delete", "uninstall"},
	"argocd":         {"delete", "terminate"},
}

// has_file_redirect returns true if cmd writes to a file via shell redirection
// (> or >>) to a path other than /dev/null.
func has_file_redirect(cmd string) bool {
	for _, safe := range []string{
		"2>&1", "1>&2", ">&2", ">&1",
		">/dev/null", "> /dev/null", ">>/dev/null", ">> /dev/null",
	} {
		cmd = strings.ReplaceAll(cmd, safe, " ")
	}
	return strings.Contains(cmd, ">")
}

// cmd_base returns the basename of a possibly path-prefixed command.
// "/usr/bin/rm" → "rm", "rm" → "rm".
func cmd_base(s string) string {
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		return s[idx+1:]
	}
	return s
}

// shell_segments splits a shell command string on operators (;, &&, ||, |)
// to return individual command segments for per-command analysis.
func shell_segments(cmd string) []string {
	cmd = strings.ReplaceAll(cmd, "&&", ";")
	cmd = strings.ReplaceAll(cmd, "||", ";")
	var out []string
	for _, seg := range strings.Split(cmd, ";") {
		for _, part := range strings.Split(seg, "|") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// parse_cmd extracts the effective command name and arguments from a segment,
// stripping leading wrapper commands (sudo, nohup, env, etc.).
func parse_cmd(seg string) (name string, args []string) {
	fields := strings.Fields(seg)
	wrappers := map[string]bool{
		"sudo": true, "nice": true, "nohup": true, "env": true, "command": true,
	}
	i := 0
	for i < len(fields) {
		base := cmd_base(fields[i])
		if !wrappers[base] {
			break
		}
		i++
		for i < len(fields) && strings.HasPrefix(fields[i], "-") {
			i++
		}
	}
	// "timeout <duration> <cmd>" — skip the numeric duration argument.
	if i < len(fields) && cmd_base(fields[i]) == "timeout" {
		i++
		if i < len(fields) && !strings.HasPrefix(fields[i], "-") {
			i++
		}
	}
	if i >= len(fields) {
		return "", nil
	}
	return cmd_base(fields[i]), fields[i+1:]
}

// is_destructive returns (true, reason) if cmd or any sub-command within it
// is potentially destructive. Safe read-only commands return (false, "").
func is_destructive(cmd string) (bool, string) {
	if has_file_redirect(cmd) {
		return true, "writes to a file via shell redirection"
	}
	for _, seg := range shell_segments(cmd) {
		name, args := parse_cmd(seg)
		if name == "" {
			continue
		}
		if always_destructive[name] {
			return true, fmt.Sprintf("destructive command: %s", name)
		}
		if verbs, ok := destructive_verbs[name]; ok {
			for _, arg := range args {
				for _, verb := range verbs {
					if arg == verb || strings.HasPrefix(arg, verb) {
						return true, fmt.Sprintf("destructive sub-command: %s %s", name, arg)
					}
				}
			}
		}
		// ip has multi-word destructive sub-commands (e.g. "ip link set X down").
		if name == "ip" && len(args) >= 2 {
			joined := strings.Join(args, " ")
			switch args[0] {
			case "link":
				if strings.Contains(joined, " down") || strings.Contains(joined, "delete") {
					return true, "destructive: ip link down/delete"
				}
			case "addr":
				if args[1] == "del" || args[1] == "delete" {
					return true, "destructive: ip addr del"
				}
			case "route":
				if args[1] == "del" || args[1] == "delete" {
					return true, "destructive: ip route del"
				}
			}
		}
	}
	return false, ""
}

type Servitor struct {
	input struct {
		host       string
		port       int
		user       string
		key        string
		password   string
		confirm    bool
		output     string
		max_rounds int
	}
	conn *ssh.Client
	AppCore
}

func (T Servitor) Name() string { return "servitor" }
func (T Servitor) Desc() string {
	return "Ops: Map and profile a remote Linux appliance via SSH."
}

func (T Servitor) SystemPrompt() string {
	return `You are a senior Linux systems engineer conducting a deep investigation of a remote appliance via SSH.
Your objective is tier-3 knowledge: not just what is running, but how everything connects — how requests
flow through the stack, what every service depends on, where every log lives, and what the full
application topology looks like. The resulting profile must be complete enough to answer any operational
or diagnostic question without needing to re-connect to the system.

EVIDENCE RULE — enforced strictly:
Every fact in your report must come from actual command output received in this session.
Do NOT use training knowledge, assumptions, or guesses to fill in any value.
If a command failed or returned nothing, write "Not determined."

INVESTIGATION PHILOSOPHY — follow every lead:
When you discover a service or application, investigate it completely before moving on:
  • Read ALL its config files — the main file AND every include/conf.d fragment
  • Extract every upstream, downstream, database connection, API endpoint, and socket path
  • Find its log files FROM its config — not by assuming standard locations
  • Check its systemd unit (systemctl cat) for ExecStart, User=, EnvironmentFile=, Requires=, After=
  • If it references another service (nginx → app server → database), investigate that too
  • When you figure out HOW to do something non-obvious — working db auth method, correct binary path,
    non-standard command syntax — call record_technique so future sessions use it directly
  • When you encounter a mistake or system quirk, call note_lesson so future sessions avoid it
Never stop at the surface. A shallow pass that discovers nginx but never reads the vhost configs
is not an acceptable result.

────────────────────────────────────────────────────────────
PHASE 1 — FOUNDATION
────────────────────────────────────────────────────────────
Run: hostname; uname -a; cat /etc/os-release; uptime; timedatectl 2>/dev/null || date
Run: lscpu | grep -E "Model name|Socket|Core|Thread|^CPU\(s\)"; free -h; df -h; lsblk -o NAME,SIZE,TYPE,MOUNTPOINT
Run: ip addr show; ip route show; cat /etc/resolv.conf; cat /etc/hosts | grep -v "^#\|^$"
Record: hostname, OS, kernel, architecture, uptime, timezone, CPU/RAM/disks, all interfaces+IPs, gateway, DNS, static host entries.

────────────────────────────────────────────────────────────
PHASE 2 — SERVICE AND PROCESS TOPOLOGY
────────────────────────────────────────────────────────────
Run: systemctl list-units --type=service --state=running --no-pager 2>/dev/null
Run: ps auxf 2>/dev/null | head -200
Run: ss -tlnp 2>/dev/null; ss -ulnp 2>/dev/null
Run: ss -tnp 2>/dev/null | grep ESTAB | head -60
Record: every running service, every listening TCP/UDP port with owning process, active established connections.

────────────────────────────────────────────────────────────
PHASE 3 — DEEP SERVICE INVESTIGATION (repeat for EACH service found)
────────────────────────────────────────────────────────────
For every non-trivial service discovered in phase 2, do ALL of the following — do not skip services:

a) Read the unit file:
   Run: systemctl cat <service> 2>/dev/null
   Extract: ExecStart (binary + args), User=, Group=, WorkingDirectory=, EnvironmentFile=, Requires=, After=, BindsTo=

b) Read the primary config file, then ALL includes:
   If the config has Include, include_dir, conf.d, sites-enabled, etc. — read those too.
   Extract: bind address/port, upstream URLs, database host/port/name, cache addresses,
            queue/broker endpoints, log file paths, error log paths, SSL cert/key paths.

c) Read any EnvironmentFile= referenced in the unit — show key names (mask values resembling passwords).

d) WEB SERVERS (nginx, apache, caddy, haproxy, traefik):
   Run: find /etc/nginx /etc/apache2 /etc/httpd /etc/caddy /etc/haproxy /etc/traefik -type f 2>/dev/null
   Read every vhost / site config. Extract: server_name/domain, document root, proxy_pass/upstream,
   SSL cert path, rate limits, auth directives.

e) APP SERVERS (gunicorn, uwsgi, node, python, java, go binaries, ruby, php-fpm):
   Identify the WorkingDirectory or app root from the ExecStart path.
   Run: ls -la <app_dir>; find <app_dir> -maxdepth 3 -name "*.cfg" -o -name "*.yaml" -o -name "*.yml" -o -name "*.json" -o -name ".env" -o -name "config.py" -o -name "settings.py" 2>/dev/null | head -40
   Read all config/settings files found. Extract DB connections, API keys structure, external service URLs.

f) DATABASES — full schema introspection for every engine found:

   MySQL / MariaDB:
     Run: mysql -N -e "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('information_schema','mysql','performance_schema','sys');" 2>/dev/null
     For EACH application database found, run ALL of:
       mysql -N -e "SELECT table_name, engine, table_rows, ROUND((data_length+index_length)/1024/1024,2) AS size_mb FROM information_schema.tables WHERE table_schema='<db>' ORDER BY size_mb DESC;" 2>/dev/null
       mysql -N <db> -e "SELECT table_name, column_name, column_type, column_key, is_nullable, column_default FROM information_schema.columns WHERE table_schema='<db>' ORDER BY table_name, ordinal_position;" 2>/dev/null
     Run: mysql -N -e "SELECT user, host, plugin, account_locked FROM mysql.user;" 2>/dev/null
     For each non-root user: mysql -N -e "SHOW GRANTS FOR '<user>'@'<host>';" 2>/dev/null

   PostgreSQL:
     Run: psql -c "\l+" 2>/dev/null
     For EACH application database (not postgres/template0/template1):
       psql -d <db> -c "SELECT schemaname, tablename, pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size, n_live_tup AS rows FROM pg_stat_user_tables ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;" 2>/dev/null
       psql -d <db> -c "SELECT table_schema, table_name, column_name, data_type, character_maximum_length, is_nullable, column_default FROM information_schema.columns WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY table_schema, table_name, ordinal_position LIMIT 500;" 2>/dev/null
       psql -d <db> -c "SELECT usename, usesuper, usecreatedb, usecreaterole FROM pg_catalog.pg_user;" 2>/dev/null
       psql -d <db> -c "SELECT grantee, table_schema, table_name, array_agg(privilege_type ORDER BY privilege_type) AS privs FROM information_schema.role_table_grants WHERE grantee NOT IN ('PUBLIC') GROUP BY grantee, table_schema, table_name ORDER BY grantee LIMIT 100;" 2>/dev/null

   SQLite:
     Run: find / -name "*.db" -o -name "*.sqlite" -o -name "*.sqlite3" 2>/dev/null | grep -v "/proc\|/sys\|/run\|/dev" | head -20
     For each file found:
       sqlite3 <file> ".tables" 2>/dev/null
       sqlite3 <file> "SELECT name, sql FROM sqlite_master WHERE type='table' ORDER BY name;" 2>/dev/null

   MongoDB:
     Run: mongosh --eval "db.adminCommand({listDatabases:1,nameOnly:false})" --quiet 2>/dev/null || mongo --eval "db.adminCommand({listDatabases:1})" 2>/dev/null
     For each application database:
       mongosh <db> --eval "db.getCollectionNames().forEach(function(n){var s=db[n].stats();print(n+': docs='+s.count+' size='+Math.round(s.size/1024)+'KB')})" --quiet 2>/dev/null
       mongosh <db> --eval "db.getCollectionNames().forEach(function(n){var d=db[n].findOne();if(d){print('=== '+n+' sample ===');print(JSON.stringify(Object.keys(d)))}})" --quiet 2>/dev/null

   Redis:
     Run: redis-cli INFO all 2>/dev/null
     Run: redis-cli CONFIG GET databases 2>/dev/null; redis-cli CONFIG GET maxmemory 2>/dev/null
     Run: for i in $(seq 0 15); do n=$(redis-cli -n $i DBSIZE 2>/dev/null); [ "${n:-0}" -gt 0 ] 2>/dev/null && echo "db$i: $n keys"; done
     Run: for k in $(redis-cli KEYS '*' 2>/dev/null | head -15); do echo "$k: $(redis-cli TYPE $k 2>/dev/null)"; done

   Elasticsearch:
     Run: curl -s "http://localhost:9200/_cluster/health?pretty" 2>/dev/null
     Run: curl -s "http://localhost:9200/_cat/indices?v&s=store.size:desc" 2>/dev/null
     Run: curl -s "http://localhost:9200/_cat/nodes?v" 2>/dev/null
     Run: curl -s "http://localhost:9200/*/_mapping?pretty" 2>/dev/null | head -200

g) CONTAINERS (docker, podman):
   Run: docker ps --format "table {{.Names}}\t{{.Image}}\t{{.Ports}}\t{{.Status}}" 2>/dev/null
   Run: docker network ls 2>/dev/null; docker volume ls 2>/dev/null
   For each running container: docker inspect <name> 2>/dev/null | python3 -c "import sys,json; d=json.load(sys.stdin)[0]; env=d['Config']['Env']; mounts=[(m['Source'],m['Destination']) for m in d['Mounts']]; ports=d['HostConfig']['PortBindings']; print('Env:',env); print('Mounts:',mounts); print('Ports:',ports)" 2>/dev/null
   Run: find / -name "docker-compose.yml" -o -name "docker-compose.yaml" 2>/dev/null | head -10
   Read each compose file — extract service definitions, environment variables (mask secret values), volume mounts, depends_on.

────────────────────────────────────────────────────────────
PHASE 3.5 — CONNECTION STRING AND DATABASE ACCESS DISCOVERY
────────────────────────────────────────────────────────────
Systematically find every location where database connection info is stored.
For all files found: show key names, redact values where the key contains pass/secret/key/token/auth/cred/pwd.

a) Grep all config locations for connection string patterns:
   Run: grep -rlE "DATABASE_URL|DB_URL|MONGO_URI|REDIS_URL|MONGODB_URI|db_host|database_host|DB_HOST|DB_SERVER|DB_PORT|DB_NAME|spring\.datasource|SQLALCHEMY_DATABASE|ConnectionString|jdbc:|Data Source=" /etc /opt /srv /var/www /home --include="*.env" --include="*.conf" --include="*.cfg" --include="*.ini" --include="*.yml" --include="*.yaml" --include="*.json" --include="*.properties" --include="*.xml" --include="*.toml" 2>/dev/null | grep -v ".git" | head -30
   Read each file found (redacting secret values).

b) EnvironmentFile contents (from all systemd units):
   For each EnvironmentFile= path found in Phase 3: cat the file (redact secret values).

c) Docker compose environment sections — already read in Phase 3g. Extract db-related env vars.

d) Framework-specific database config files:
   Django:  find / -name "settings.py" -o -name "settings_production.py" -o -name "local_settings.py" 2>/dev/null | head -10 → read each, extract DATABASES block
   Rails:   find / -name "database.yml" 2>/dev/null | grep "config/" | head -5 → read each
   Laravel: find / -name "database.php" 2>/dev/null | grep "config/" | head -5 → read each
   Spring:  find / -name "application.properties" -o -name "application.yml" 2>/dev/null | head -10 → read each, extract spring.datasource.* and spring.data.*
   Node:    find / -name "knexfile.js" -o -name "knexfile.ts" -o -name "ormconfig*" -o -name "typeorm.config*" -o -name "prisma/schema.prisma" -o -name "sequelize*config*" 2>/dev/null | head -10 → read each
   Go:      grep -rn "sql.Open\|gorm.Open\|mongo.Connect\|redis.NewClient\|pgx.Connect" --include="*.go" /opt /srv /var/www /home 2>/dev/null | head -20

e) .pgpass, .my.cnf, .rediscli_history, wallet files (credential stores):
   Run: find /root /home -name ".pgpass" -o -name ".my.cnf" -o -name ".mylogin.cnf" -o -name "pgpass.conf" 2>/dev/null | xargs cat 2>/dev/null
   (Mask actual passwords in output.)

────────────────────────────────────────────────────────────
PHASE 4 — APPLICATION DEPLOYMENTS AND SOURCE ANALYSIS
────────────────────────────────────────────────────────────
Find custom/non-packaged applications:
Run: find /opt /srv /var/www /home -maxdepth 4 \( -name "package.json" -o -name "go.mod" -o -name "requirements.txt" -o -name "Gemfile" -o -name "pom.xml" -o -name "Makefile" -o -name "Dockerfile" \) 2>/dev/null | head -40
Run: find /home /root /opt /srv -maxdepth 4 -name ".git" -type d 2>/dev/null | head -20
For each .git repo: git -C <dir> remote -v 2>/dev/null; git -C <dir> log --oneline -3 2>/dev/null; git -C <dir> describe --tags 2>/dev/null
Run: pm2 list 2>/dev/null; supervisorctl status 2>/dev/null
Run: find /etc/supervisor /etc/supervisord.d /etc/pm2 -name "*.conf" -o -name "*.json" 2>/dev/null | xargs cat 2>/dev/null
Record: every deployed application — location, tech stack, version/commit, repo origin, process manager.

For EACH application directory found, perform source code analysis:

a) Identify framework and read entry point (first 80 lines):
   Python/Django: find <dir> -name "manage.py" -o -name "wsgi.py" -o -name "asgi.py" | head -3 → read each
   Python/Flask/FastAPI: find <dir> -name "app.py" -o -name "main.py" -o -name "application.py" | head -3 → read first 80 lines
   Node.js: cat <dir>/package.json → read main/scripts; find <dir> -name "app.js" -o -name "server.js" -o -name "index.js" -maxdepth 3 | head -3 → read first 80 lines
   Go: cat <dir>/main.go 2>/dev/null || find <dir> -name "main.go" -maxdepth 4 | head -3 → read each
   Ruby/Rails: cat <dir>/config/application.rb 2>/dev/null; cat <dir>/Gemfile 2>/dev/null
   PHP/Laravel: cat <dir>/public/index.php 2>/dev/null; cat <dir>/composer.json 2>/dev/null
   Java/Spring: find <dir> -name "*Application.java" | head -3 → read each; cat src/main/resources/application.properties 2>/dev/null

b) Read routing / URL configuration fully:
   Django:    find <dir> -name "urls.py" | head -10 → read each (these define all endpoints)
   Rails:     cat <dir>/config/routes.rb 2>/dev/null
   Express:   find <dir>/routes -name "*.js" -o -name "*.ts" | head -20 → read each
   Laravel:   cat <dir>/routes/web.php <dir>/routes/api.php 2>/dev/null
   Go:        grep -rn "\.HandleFunc\|\.Handle\|router\.GET\|router\.POST\|mux\." --include="*.go" <dir> 2>/dev/null | head -60
   Spring:    find <dir> -name "*.java" | xargs grep -l "@RequestMapping\|@GetMapping\|@PostMapping\|@RestController" 2>/dev/null | head -10 → read each

c) Read ORM model definitions (understand data structure from code):
   Django:    find <dir> -name "models.py" | head -15 → read each (Django model = DB table)
   Rails:     find <dir>/app/models -name "*.rb" | head -15 → read each
   Laravel:   find <dir>/app/Models -name "*.php" 2>/dev/null | head -15 → read each
   SQLAlchemy: grep -rn "db.Model\|Base\)" --include="*.py" <dir> 2>/dev/null | head -20
   TypeORM:   find <dir> -name "*.entity.ts" | head -15 → read each
   Prisma:    find <dir> -name "schema.prisma" | head -3 → read each (full DB schema in one file)
   GORM:      grep -rn "type.*struct" --include="*.go" <dir> 2>/dev/null | head -30; find <dir> -name "*.go" -path "*/model*" | head -10 → read each

d) Find middleware, auth, and external API calls:
   Run: grep -rn "jwt\|oauth\|api_key\|APIKey\|Authorization\|Bearer\|http\.Get\|http\.Post\|axios\|fetch\|requests\.get" --include="*.go" --include="*.py" --include="*.js" --include="*.ts" --include="*.rb" <dir> 2>/dev/null | grep -v "_test\.\|vendor/\|node_modules/" | head -40

────────────────────────────────────────────────────────────
PHASE 5 — COMPREHENSIVE LOG DISCOVERY
────────────────────────────────────────────────────────────
Standard:
Run: find /var/log -maxdepth 4 -type f \( -name "*.log" -o -name "access.log" -o -name "error.log" \) 2>/dev/null | sort
Run: ls -lhR /var/log/ 2>/dev/null | head -300

Application-specific — for EACH log path extracted from configs in phase 3:
Run: test -f "<path>" && echo "EXISTS: <path>" || echo "MISSING: <path>"
Also check common alternative locations based on service:
Run: find /opt /srv /var/www /home -maxdepth 5 -name "*.log" 2>/dev/null | head -60

Journal:
Run: journalctl --disk-usage 2>/dev/null
Run: journalctl --list-boots --no-pager 2>/dev/null
Run: journalctl -p err --since "24 hours ago" --no-pager 2>/dev/null | tail -60

Container logs:
Run: for c in $(docker ps -q 2>/dev/null); do name=$(docker inspect --format '{{.Name}}' $c); driver=$(docker inspect --format '{{.HostConfig.LogConfig.Type}}' $c); logpath=$(docker inspect --format '{{.LogPath}}' $c); echo "$name driver=$driver path=$logpath"; done 2>/dev/null

Record: every confirmed log file path — include service name, absolute path, and a one-line description.
IMPORTANT: verify existence before recording. Do not guess.

────────────────────────────────────────────────────────────
PHASE 6 — INTER-SERVICE COMMUNICATION
────────────────────────────────────────────────────────────
Run: ss -xnp 2>/dev/null | head -60
Run: find /run /tmp /var/run -name "*.sock" -o -name "*.socket" 2>/dev/null
Run: lsof -i -n -P 2>/dev/null | grep -v "127.0.0.1.*127.0.0.1\|::1.*::1" | grep ESTABLISH | head -40
Service discovery / mesh:
Run: systemctl is-active consul etcd vault 2>/dev/null; cat /etc/consul.d/*.json /etc/consul/config.json 2>/dev/null | head -60
Record: Unix domain sockets (which processes share them), external IPs being contacted, service discovery if present.

────────────────────────────────────────────────────────────
PHASE 7 — SCHEDULED WORK
────────────────────────────────────────────────────────────
Run: cat /etc/crontab 2>/dev/null; find /etc/cron.d /etc/cron.daily /etc/cron.hourly /etc/cron.weekly /etc/cron.monthly -type f 2>/dev/null | xargs cat
Run: for u in $(cut -f1 -d: /etc/passwd); do t=$(crontab -l -u "$u" 2>/dev/null); [ -n "$t" ] && printf "=== %s ===\n%s\n" "$u" "$t"; done
Run: systemctl list-timers --all --no-pager 2>/dev/null
Run: atq 2>/dev/null
Record: every scheduled job — schedule, command, and owner.

────────────────────────────────────────────────────────────
PHASE 8 — SECURITY POSTURE
────────────────────────────────────────────────────────────
Run: cat /etc/sudoers 2>/dev/null; find /etc/sudoers.d -type f 2>/dev/null | xargs cat
Run: cat /etc/ssh/sshd_config 2>/dev/null | grep -v "^#\|^$"
Run: find /home /root -name "authorized_keys" 2>/dev/null -exec echo "=== {} ===" \; -exec cat {} \;
Run: lastlog 2>/dev/null | grep -v "Never logged" | head -20
Run: journalctl -u sshd --since "7 days ago" --no-pager 2>/dev/null | grep -i "fail\|invalid\|error" | tail -30
Run: grep -i "fail\|invalid" /var/log/auth.log /var/log/secure 2>/dev/null | tail -30
Run: getent passwd | awk -F: '$3>=1000{print $1, $3, $6, $7}'
Run: getent group | awk -F: '$3<1000 && $4!="" {print $1, $4}' | grep -v "^$"
Run: iptables -L -n -v 2>/dev/null || nft list ruleset 2>/dev/null || firewall-cmd --list-all 2>/dev/null
Record: sudo grants per user, SSH PermitRootLogin/PasswordAuth/PubkeyAuth, authorized keys, recent auth failures,
human user accounts, group memberships for service accounts, firewall rules.

────────────────────────────────────────────────────────────
PHASE 9 — SSL/TLS CERTIFICATES
────────────────────────────────────────────────────────────
Run: find /etc /opt /srv /var/www -name "*.pem" -o -name "*.crt" -o -name "*.cert" 2>/dev/null | grep -v "ca-certificates\|/usr/share\|/usr/lib" | head -30
For each cert file found: openssl x509 -in <file> -noout -subject -issuer -dates 2>/dev/null
Run: which certbot 2>/dev/null && certbot certificates 2>/dev/null
Record: every cert — subject/domain, issuer, valid from/to, expiry.

────────────────────────────────────────────────────────────
PHASE 10 — RECENT CHANGES AND ENVIRONMENT
────────────────────────────────────────────────────────────
Run: find /etc /opt /srv /var/www -name "*.conf" -newer /etc/os-release -type f 2>/dev/null | head -30
Run: rpm -qa --last 2>/dev/null | head -20 || dpkg-query -W --showformat='${Installed-Size}\t${Package}\t${Version}\n' 2>/dev/null | sort -rn | head -30
Run: cat /etc/environment 2>/dev/null; find /etc/profile.d -name "*.sh" 2>/dev/null | xargs cat
Run: find /etc /opt /srv /var/www /home -maxdepth 4 -name ".env" -o -name "*.env" 2>/dev/null | head -20
For each .env file found: cat it but replace values that look like passwords/secrets with <redacted>
  (a value is a secret if the key contains: pass, secret, key, token, auth, cred — case-insensitive)
Record: recently modified configs, recently installed packages, environment variables, .env key inventory.

────────────────────────────────────────────────────────────
OUTPUT FORMAT
────────────────────────────────────────────────────────────

# Linux Appliance Profile: <exact hostname>

## System Identity
<OS, kernel, architecture, uptime, timezone>

## Hardware
<CPU model+cores, total RAM, disks with sizes and mount points>

## Network
<each interface with IP, default gateway, DNS servers, notable /etc/hosts entries>

## Application Architecture
Narrative paragraph: which component handles incoming requests → how it routes to backend services
→ what data stores exist → how services authenticate to each other → what external APIs are called.
Describe the complete request flow from entry point to data layer. Base this on what you actually read.

## Application Code Structure
<per application found:>
### <app name> (<framework>)
- Location: <path>
- Entry point: <file>
- Routing: <summary of routes/endpoints discovered from route files>
- Models/Entities: <list of ORM models or DB tables found in source>
- External calls: <external HTTP/API calls found in source>
- Auth mechanism: <JWT/session/OAuth/API key — from code>

## Services and Ports
<every running service — name, listening port/socket, process owner>

## Service Configuration Details
<one subsection per significant service:>
### <service name>
- Unit file: <path>
- Binary: <ExecStart value>
- Config: <path(s) read>
- Key config values: <bind address, upstreams, db connections — from actual config>
- Log paths: <from config>
- Dependencies: <Requires/After from unit>

## Database Access and Connection Strings
<per application:>
### <app name>
- Database engine: <mysql/postgres/mongo/redis/sqlite>
- Connection: <host:port or socket path — from config, not guessed>
- Database name: <from config>
- User: <from config — mask password>
- Config source: <which file contains the connection string>
- Access method: <TCP/Unix socket, auth plugin if known>

## Database Schemas
<per database engine and database name:>
### <engine>: <database name>
#### Tables
| Table | Rows (approx) | Size | Key Columns |
|-------|--------------|------|-------------|
| <name> | <n> | <MB> | <col: type, col: type, ...> |

#### Users and Grants
| User | Host | Privileges |
|------|------|-----------|

## Application Deployments
<non-packaged apps — location, tech stack, version/commit, repo origin, process manager>

## Scheduled Jobs
<all cron/timer entries — schedule, command, owner>

## Security Posture
<sudo grants, SSH config (PermitRoot, PasswordAuth, keys), human users, auth failures, firewall>

## SSL/TLS Certificates
<each cert — domain, issuer, expiry date>

## Inter-Service Communication
<Unix sockets between services, external connections, service discovery>

## Recent Changes
<recently modified configs, recent package installs>

## Recent Errors
<journalctl error output or "None found">

## Summary and Notable Findings
<key observations, potential issues, things that stand out — based solely on what commands returned>

## Log Files
` + "```json" + `
[
  {"service": "<name>", "path": "<absolute-path confirmed to exist>", "desc": "<one line>"},
  ...
]
` + "```" + `

IMPORTANT: The Log Files JSON block MUST list only paths you confirmed exist on this system.
Every entry must have "service", "path", and "desc". This list is stored for future log queries.

────────────────────────────────────────────────────────────
LARGE OUTPUT STRATEGY
────────────────────────────────────────────────────────────
Command output is capped at 10,000 characters. When truncated, the message gives total line count
and a ready-made sed command for the next page.
• Filter first: pipe through | grep KEYWORD or | awk to narrow before reading.
• For files: use count_lines to check size, then read_range to page in chunks of ≤300 lines.
• For user/group lists: wc -l first, then awk -F: '$3>=1000' to get human accounts only.`
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func (T *Servitor) Init() error {
	T.Private() // HARD GUARD: never escalate to lead LLM
	T.Flags.StringVar(&T.input.host, "host", "", "SSH host to connect to (required).")
	T.Flags.IntVar(&T.input.port, "port", 22, "SSH port number.")
	T.Flags.StringVar(&T.input.user, "user", "root", "SSH username.")
	T.Flags.StringVar(&T.input.key, "key", "", "Path to SSH private key file (PEM). Tries ~/.ssh/id_rsa if not set.")
	T.Flags.StringVar(&T.input.password, "password", "", "SSH password (key auth preferred).")
	T.Flags.BoolVar(&T.input.confirm, "confirm", "Prompt for confirmation before every command (not just destructive ones).")
	T.Flags.StringVar(&T.input.output, "output", "", "File path to save the Markdown report.")
	T.Flags.IntVar(&T.input.max_rounds, "max_rounds", 100, "Max agent loop rounds (default 100).")
	T.Flags.Order("host", "port", "user", "key", "password", "confirm", "output", "max_rounds")
	return T.Flags.Parse()
}

// confirm_cmd prompts the user to allow or deny a command before it runs.
// destructive=true shows a red warning box; false shows a yellow notice box.
// Returns true if the user allows the command.
func (T *Servitor) confirm_cmd(cmd, reason string, destructive bool) bool {
	PleaseWait.Hide()
	defer PleaseWait.Show()
	if destructive {
		Stderr("\n\033[1;31m  ╭─ Destructive Command ────────────────────────────────\033[0m")
		Stderr("\033[1;31m  │\033[0m  Reason:  \033[1m%s\033[0m", reason)
		Stderr("\033[1;31m  │\033[0m  Command: %s", cmd)
		Stderr("\033[1;31m  ╰──────────────────────────────────────────────────────\033[0m")
	} else {
		Stderr("\n\033[1;33m  ╭─ Command ────────────────────────────────────────────\033[0m")
		Stderr("\033[1;33m  │\033[0m  $ %s", cmd)
		Stderr("\033[1;33m  ╰──────────────────────────────────────────────────────\033[0m")
	}
	answer := strings.ToLower(strings.TrimSpace(GetInput("  Allow? [y/N]: ")))
	return answer == "y" || answer == "yes"
}

func (T *Servitor) connect() error {
	var auth_methods []ssh.AuthMethod

	key_path := T.input.key
	if key_path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			candidate := home + "/.ssh/id_rsa"
			if _, err := os.Stat(candidate); err == nil {
				key_path = candidate
			}
		}
	}

	if key_path != "" {
		key_bytes, err := os.ReadFile(key_path)
		if err != nil {
			return fmt.Errorf("reading key %s: %w", key_path, err)
		}
		signer, err := ssh.ParsePrivateKey(key_bytes)
		if err != nil {
			return fmt.Errorf("parsing private key: %w", err)
		}
		auth_methods = append(auth_methods, ssh.PublicKeys(signer))
	}

	if T.input.password != "" {
		auth_methods = append(auth_methods, ssh.Password(T.input.password))
	}

	if len(auth_methods) == 0 {
		return fmt.Errorf("no authentication method: provide --key or --password")
	}

	addr := net.JoinHostPort(T.input.host, fmt.Sprintf("%d", T.input.port))
	cfg := &ssh.ClientConfig{
		User:            T.input.user,
		Auth:            auth_methods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return fmt.Errorf("SSH connect to %s: %w", addr, err)
	}
	T.conn = client
	return nil
}

// exec_command runs cmd on the remote host via a fresh SSH session and returns
// combined stdout+stderr. Non-zero exit codes are not treated as hard errors
// since probe commands often return non-zero when a feature is absent.
// exec_command is a convenience wrapper for callers without a context.
// New code should call exec_command_ctx so the command honors cancellation
// and the per-command timeout.
func (T *Servitor) exec_command(cmd string) (string, error) {
	return T.exec_command_ctx(context.Background(), cmd)
}

// exec_command_ctx runs cmd via SSH, capping wall-clock time at command_timeout
// and aborting if ctx is cancelled. Without this guard a hung command (e.g.
// `tail -f`, `journalctl -f`, anything reading stdin) would block the worker
// goroutine indefinitely — the LLM never sees a result, the heartbeat keeps
// firing, and the session looks stuck.
func (T *Servitor) exec_command_ctx(ctx context.Context, cmd string) (string, error) {
	session, err := T.conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("new SSH session: %w", err)
	}
	defer session.Close()

	type runResult struct {
		out []byte
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		out, err := session.CombinedOutput(cmd)
		done <- runResult{out: out, err: err}
	}()

	timer := time.NewTimer(command_timeout)
	defer timer.Stop()

	var (
		out       []byte
		runErr    error
		timedOut  bool
		cancelled bool
	)
	select {
	case r := <-done:
		out, runErr = r.out, r.err
	case <-timer.C:
		timedOut = true
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		r := <-done
		out, runErr = r.out, r.err
	case <-ctx.Done():
		cancelled = true
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		r := <-done
		out, runErr = r.out, r.err
	}

	result := strings.TrimSpace(string(out))
	if len(result) > max_output {
		totalLines := strings.Count(result, "\n") + 1
		truncated := result[:max_output]
		shownLines := strings.Count(truncated, "\n") + 1
		result = truncated + fmt.Sprintf(
			"\n... [TRUNCATED: showing lines 1–%d of %d total (%d chars). "+
				"Strategies: (1) re-run with `| grep KEYWORD` to filter; "+
				"(2) re-run with `| sed -n '%d,%dp'` for the next 100 lines; "+
				"(3) if this is a file, use count_lines then read_range for clean pagination.]",
			shownLines, totalLines, len(result), shownLines+1, shownLines+100,
		)
	}
	if timedOut {
		notice := fmt.Sprintf("\n[TIMED OUT after %s — command killed. If this command does not terminate on its own (e.g. `tail -f`, `journalctl -f`, `top`, `watch`), use a bounded variant: `tail -n N`, `journalctl --since=...`, `top -bn1`, etc.]", command_timeout)
		if result == "" {
			return strings.TrimPrefix(notice, "\n"), nil
		}
		return result + notice, nil
	}
	if cancelled {
		notice := "\n[CANCELLED — session aborted before the command completed.]"
		if result == "" {
			return strings.TrimPrefix(notice, "\n"), ctx.Err()
		}
		return result + notice, ctx.Err()
	}
	if runErr != nil {
		exitCode := -1
		var exitErr *ssh.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitStatus()
		}
		if result == "" {
			return fmt.Sprintf("[exit code %d — no output]", exitCode), nil
		}
		return result + fmt.Sprintf("\n[exit code %d]", exitCode), nil
	}
	return result, nil
}

// exec_local is a context-less convenience wrapper. New code should call
// exec_local_ctx so the command honors cancellation and the per-command
// timeout.
func (T *Servitor) exec_local(cmd, workDir string, envVars []string) (string, error) {
	return T.exec_local_ctx(context.Background(), cmd, workDir, envVars)
}

// exec_local_ctx runs cmd on the local machine, capping wall-clock time at
// command_timeout and aborting if ctx is cancelled. Same rationale as
// exec_command_ctx — long-running commands would otherwise block the worker.
func (T *Servitor) exec_local_ctx(ctx context.Context, cmd, workDir string, envVars []string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, command_timeout)
	defer cancel()

	c := exec.CommandContext(runCtx, "sh", "-c", cmd)
	if workDir != "" {
		c.Dir = workDir
	}
	c.Env = append(os.Environ(), envVars...)
	out, err := c.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if len(result) > max_output {
		totalLines := strings.Count(result, "\n") + 1
		truncated := result[:max_output]
		shownLines := strings.Count(truncated, "\n") + 1
		result = truncated + fmt.Sprintf(
			"\n... [TRUNCATED: showing lines 1–%d of %d total (%d chars). "+
				"Strategies: (1) re-run with `| grep KEYWORD` to filter; "+
				"(2) re-run with `| sed -n '%d,%dp'` for the next 100 lines.]",
			shownLines, totalLines, len(result), shownLines+1, shownLines+100,
		)
	}
	// Distinguish timeout from caller cancellation from a normal nonzero exit.
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.Canceled) {
		notice := fmt.Sprintf("\n[TIMED OUT after %s — command killed. If this command does not terminate on its own, use a bounded variant.]", command_timeout)
		if result == "" {
			return strings.TrimPrefix(notice, "\n"), nil
		}
		return result + notice, nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		notice := "\n[CANCELLED — session aborted before the command completed.]"
		if result == "" {
			return strings.TrimPrefix(notice, "\n"), ctx.Err()
		}
		return result + notice, ctx.Err()
	}
	if err != nil {
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if result == "" {
			return fmt.Sprintf("[exit code %d — no output]", exitCode), nil
		}
		return result + fmt.Sprintf("\n[exit code %d]", exitCode), nil
	}
	return result, nil
}

func (T *Servitor) Main() error {
	if err := T.RequireLLM(); err != nil {
		return err
	}
	if T.input.host == "" {
		return fmt.Errorf("--host is required")
	}

	Log("Connecting to %s@%s:%d ...", T.input.user, T.input.host, T.input.port)
	if err := T.connect(); err != nil {
		return err
	}
	defer T.conn.Close()
	Log("Connected. Starting appliance reconnaissance (max %d rounds).", T.input.max_rounds)

	mainCtx := AppContext()
	run_tool := AgentToolDef{
		Tool: Tool{
			Name:        "run_command",
			Description: "Execute a shell command on the remote Linux system via SSH and return combined stdout+stderr. Output is capped at 10,000 characters.",
			Parameters: map[string]ToolParam{
				"command": {Type: "string", Description: "The shell command to run on the remote host."},
			},
			Required: []string{"command"},
		},
		Handler: func(args map[string]any) (string, error) {
			cmd, _ := args["command"].(string)
			if cmd == "" {
				return "", fmt.Errorf("command is required")
			}
			destructive, reason := is_destructive(cmd)
			if destructive {
				if !T.confirm_cmd(cmd, reason, true) {
					return "", fmt.Errorf("command denied by user")
				}
			} else if T.input.confirm {
				if !T.confirm_cmd(cmd, "", false) {
					return "", fmt.Errorf("command denied by user")
				}
			}
			return T.exec_command_ctx(mainCtx, cmd)
		},
		NeedsConfirm: false, // confirmation is handled inside the handler
	}

	messages := []Message{
		{Role: "user", Content: fmt.Sprintf(
			"Perform a complete reconnaissance and profile of the Linux appliance at %s (connected as %s). Be systematic and thorough.",
			T.input.host, T.input.user,
		)},
	}

	resp, _, err := T.RunAgentLoop(mainCtx, messages, AgentLoopConfig{
		SystemPrompt: T.SystemPrompt(),
		Tools:        []AgentToolDef{run_tool},
		MaxRounds:    T.input.max_rounds,
		RouteKey:     "app.servitor",
		ChatOptions:  []ChatOption{WithThink(false)},
		OnStep: func(step StepInfo) {
			for _, tc := range step.ToolCalls {
				cmd, _ := tc.Args["command"].(string)
				Log("[ssh] $ %s", cmd)
			}
		},
	})
	if err != nil {
		return err
	}

	report := ""
	if resp != nil {
		report = resp.Content
	}
	if report == "" {
		Warn("Agent produced no final report.")
		return nil
	}

	Stdout("\n%s\n", report)

	if T.input.output != "" {
		if err := os.WriteFile(T.input.output, []byte(report), 0644); err != nil {
			Err("Failed to write report to %s: %s", T.input.output, err)
		} else {
			Log("Report saved to %s", T.input.output)
		}
	}

	return nil
}
