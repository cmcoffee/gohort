package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/snugforge/eflag"
	"github.com/cmcoffee/snugforge/nfo"
)

// APPNAME is the application name.
const APPNAME = "gohort"

// VERSION holds the application's version string.
//
//go:embed version.txt
var VERSION string

// global holds application-wide configuration and state.
var global struct {
	cfg           ConfigStore
	db            Database
	cache         Database
	freq          time.Duration
	root          string
	debug         bool
	snoop         bool
	sysmode       bool
	single_thread bool
}

// get_runtime_info returns the name of the executable.
func get_runtime_info() string {
	exec, err := os.Executable()
	Critical(err)

	localExec := filepath.Base(exec)

	global.root, err = filepath.Abs(filepath.Dir(exec))
	Critical(err)

	global.root = GetPath(global.root)

	return localExec
}

func main() {
	AppVersion = VERSION
	nfo.HideTS()
	defer Exit(0)

	// Install the process-lifetime app context. SIGINT handlers below
	// call ShutdownApp() to cancel it so in-flight LLM streams,
	// persistent pipelines, and the interactive REPL all wind down
	// cleanly instead of being killed mid-call on daemon close.
	InitAppContext(context.Background())

	get_runtime_info()

	// Initial modifier flags.
	flags := eflag.NewFlagSet(NONE, eflag.ReturnErrorOnly)
	version := flags.Bool("version", "")
	setup := flags.Bool("setup", "Fuzz configuration (LLM, mail, etc).")
	web := flags.String("web", "", "Start web dashboard on address (e.g. ':8080').")
	max_concurrent := flags.Int("max_concurrent", 1, "Max simultaneous apps. Others are queued.")
	tls_cert := flags.String("tls_cert", "", "Path to TLS certificate file (PEM).")
	tls_key := flags.String("tls_key", "", "Path to TLS private key file (PEM).")
	tls_self := flags.Bool("tls", "Enable TLS with auto-generated self-signed certificate.")
	flags.DurationVar(&global.freq, "repeat", 0, "How often to repeat agent, 0s = single run.")
	flags.BoolVar(&global.sysmode, "quiet", "Minimal output for non-interactive processes.")

	flags.Order("repeat", "quiet", "web", "max_concurrent", "tls", "tls_cert", "tls_key")
	flags.Footer = " "

	flags.BoolVar(&global.single_thread, "serial", NONE)
	flags.BoolVar(&global.debug, "debug", NONE)
	flags.BoolVar(&global.snoop, "snoop", NONE)

	flags.Header = fmt.Sprintf("Usage: %s [options]... <command> [parameters]...\n", os.Args[0])

	f_err := flags.Parse(os.Args[1:])

	loadAgents()
	loadTools()

	// Wire up agent-to-agent delegation.
	RunAgentFunc = func(name string, args []string) (string, error) {
		command.mutex.RLock()
		entry, ok := command.entries[name]
		command.mutex.RUnlock()
		if !ok {
			return "", fmt.Errorf("unknown agent: %s", name)
		}
		return execAgent(entry, args)
	}

	// Wire up mail config loader.
	LoadMailConfigFunc = func() MailConfig {
		return load_mail_config()
	}

	// Wire up web search config loader.
	LoadWebSearchConfigFunc = func() WebSearchConfig {
		return load_search_config()
	}

	// Wire up LLM routing lookup. Reads per-stage setting from db.
	LookupRouteFunc = func(key string) string {
		var val string
		global.db.Get(RoutingTable, key, &val)
		return val
	}

	// Wire up web agent setup for the central dashboard.
	// Initialize LLMs once and share across all web apps.
	var shared_llm, shared_lead_llm LLM
	SetupWebAgentFunc = func(agent Agent) {
		set_agent_db(agent, get_agentstore(agent.Name()))
		if shared_llm == nil {
			set_agent_llm(agent)
			shared_llm = agent.Get().LLM
			shared_lead_llm = agent.Get().LeadLLM
			// Expose to stateless tools that need an inline LLM call
			// (e.g., the mock-shell tool). Only runs once because the
			// first-agent branch is gated on shared_llm being nil.
			SetSharedLLMs(shared_llm, shared_lead_llm)
		} else {
			T := agent.Get()
			T.LLM = shared_llm
			T.LeadLLM = shared_lead_llm
		}
	}

	if global.debug {
		enable_debug()
	}

	if global.snoop {
		enable_trace()
	}

	if *version {
		Stdout(`
      ____       _                _
     / ___| ___ | |__   ___  _ __| |_
    | |  _ / _ \| '_ \ / _ \| '__| __|
    | |_| | (_) | | | | (_) | |  | |_
     \____|\___/|_| |_|\___/|_|   \__|

      v%s

      Gohort Agent Framework
`, VERSION)
		Exit(0)
	}

	if *setup {
		setup_fuzz()
		Exit(0)
	}

	if *web != "" {
		Log("### %s v%s ###", APPNAME, VERSION)
		init_database()
		wireToolDB()
		init_logging()

		// Load saved web config as defaults; CLI flags override.
		var saved_max int
		var saved_cert, saved_key string
		var saved_self_signed bool
		global.db.Get(WebTable, "max_concurrent", &saved_max)
		global.db.Get(WebTable, "tls_cert", &saved_cert)
		global.db.Get(WebTable, "tls_key", &saved_key)
		global.db.Get(WebTable, "tls_self_signed", &saved_self_signed)

		if *max_concurrent != 1 {
			MaxConcurrentTasks = *max_concurrent
		} else if saved_max > 0 {
			MaxConcurrentTasks = saved_max
		}
		if *tls_cert != "" {
			TLSCert = *tls_cert
		} else {
			TLSCert = saved_cert
		}
		if *tls_key != "" {
			TLSKey = *tls_key
		} else {
			TLSKey = saved_key
		}
		if *tls_self {
			TLSSelfSigned = true
		} else {
			TLSSelfSigned = saved_self_signed
		}

		// Wire auth database.
		AuthDB = func() Database { return global.db }
		AuthEnabled = func() bool { return AuthHasUsers(global.db) }
		AuthSignupAllowed = func() bool {
			var allowed bool
			global.db.Get(WebTable, "allow_signup", &allowed)
			return allowed
		}
		AuthSessionDays = func() int {
			var days int
			global.db.Get(WebTable, "session_days", &days)
			if days == 0 {
				days = 7
			}
			return days
		}
		AuthAPIKey = func() string {
			var key string
			global.db.Get(WebTable, "api_key", &key)
			return key
		}

		WebBaseURL = func() string {
			var url string
			global.db.Get(WebTable, "external_url", &url)
			return url
		}
		ServiceNameFunc = func() string {
			var name string
			global.db.Get(WebTable, "service_name", &name)
			return name
		}
		AuthMaxAttempts = func() int {
			var n int
			global.db.Get(WebTable, "max_login_attempts", &n)
			if n == 0 {
				n = 5
			}
			return n
		}
		AuthLockoutMinutes = func() int {
			var n int
			global.db.Get(WebTable, "lockout_minutes", &n)
			if n == 0 {
				n = 15
			}
			return n
		}
		NotifyFromFunc = func() string {
			var from string
			global.db.Get(WebTable, "notify_from", &from)
			return from
		}

		// Wire persistent queue.
		SetQueueDB(func() Database { return global.db })

		// Wire CMS config loader (setup saves to global.db, not per-agent bucket).
		LoadGhostConfigFunc = func() GhostConfig {
			var cfg GhostConfig
			global.db.Get("ghost_config", "url", &cfg.URL)
			global.db.Get("ghost_config", "api_key", &cfg.APIKey)
			return cfg
		}

		// Wire admin IP allowlist.
		LoadAdminAllowedIPsFunc = func() string {
			var val string
			global.db.Get(WebTable, "admin_allowed_ips", &val)
			return val
		}

		if err := ServeDashboard(*web); err != nil {
			Fatal(err)
		}
		Exit(0)
	}

	// Check for chat mode before processing other flags.
	if args := flags.Args(); len(args) > 0 && args[0] == "chat" {
		if err := startChat(); err != nil {
			Stderr(err)
			Exit(1)
		}
		Exit(0)
	}

	if global.sysmode {
		nfo.Animations = false
		nfo.SignalCallback(syscall.SIGINT, func() bool {
			ShutdownApp()
			return true
		})
	} else {
		nfo.SignalCallback(syscall.SIGINT, func() bool {
			Log("Application interrupt received. (shutting down)")
			ShutdownApp()
			return true
		})
	}

	if f_err != nil {
		if f_err != eflag.ErrHelp {
			Stderr(f_err)
			Stderr(NONE)
		}
		flags.Usage()
		command.Show()
		return
	}

	// Read and process CLI arguments.
	args := flags.Args()

	// No command given — launch interactive chat.
	if len(args) == 0 {
		if err := startChat(); err != nil {
			Stderr(err)
			Exit(1)
		}
		return
	}

	var task_args [][]string
	args = append(args, "cli")
	task_args = append(task_args, args)

	if err := command.Select(task_args); err != nil {
		if err != eflag.ErrHelp {
			Stderr(err)
		}
		flags.Usage()
		command.Show()
		if err == eflag.ErrHelp {
			return
		} else {
			Exit(1)
		}
	}
}
