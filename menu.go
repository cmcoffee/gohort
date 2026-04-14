package main

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/snugforge/eflag"
	"github.com/cmcoffee/snugforge/nfo"
)

var command menu

// menu represents a menu structure for managing and executing agents.
type menu struct {
	mutex        sync.RWMutex
	text         *tabwriter.Writer
	entries      map[string]*menu_elem
	agents       []string
	apps         []string
	admin_agents []string
}

// cmd_text writes a command and its description to the menu text.
func (m *menu) cmd_text(cmd string, desc string) {
	m.text.Write([]byte(fmt.Sprintf("  %s \t \"%s\"\n", cmd, desc)))
}

const (
	_normal_agent = 1 << iota
	_app
	_admin_agent
)

// menu_elem represents a single menu entry.
type menu_elem struct {
	name   string
	desc   string
	parsed bool
	t_flag uint
	agent  Agent
	flags  *FlagSet
}

// RegisterAdmin registers an admin agent with the menu.
func (m *menu) RegisterAdmin(agent Agent) {
	m.register(agent.Name(), _admin_agent, agent)
}

// Register registers a new agent with the menu.
func (m *menu) Register(agent Agent) {
	m.register(agent.Name(), _normal_agent, agent)
}

// RegisterApp registers an app with the menu.
func (m *menu) RegisterApp(agent Agent) {
	m.register(agent.Name(), _app, agent)
}

// RegisterName registers an agent with a custom name.
func (m *menu) RegisterName(name string, agent Agent) {
	m.register(name, _normal_agent, agent)
}

// set_agent_flags sets the flags for a given agent.
func set_agent_flags(agent Agent, Flags FlagSet) {
	T := agent.Get()
	T.Flags = Flags
}

// set_agent_db sets the database and cache for a given agent.
func set_agent_db(agent Agent, DB Database) {
	T := agent.Get()
	T.DB = DB
	T.Cache = global.cache
	for _, k := range T.Cache.Tables() {
		T.Cache.Drop(k)
	}
}

func init() {
	// Wire key providers for image generation.
	ImageProviderFunc = ImageProvider
	GeminiKeyFunc = GeminiAPIKey
	OpenAIKeyFunc = OpenAIAPIKey
}

// set_agent_llm initializes the LLM client for a given agent from stored config.
func set_agent_llm(agent Agent) {
	cfg := load_llm_config()
	if cfg.Provider == "" {
		return
	}
	llm, err := NewLLMFromConfig(cfg)
	if err != nil {
		Debug("LLM init: %s", err)
		return
	}
	T := agent.Get()
	T.LLM = llm
	// Auto-enable prompt-based tools when native tool calling is disabled.
	if !cfg.NativeTools {
		T.PromptTools = true
	}

	// Initialize lead LLM if configured.
	lead_cfg := load_lead_llm_config()
	if lead_cfg.Provider != "" {
		lead_llm, err := NewLLMFromConfig(lead_cfg)
		if err != nil {
			Debug("Lead LLM init: %s", err)
		} else {
			T.LeadLLM = lead_llm
			Debug("Lead LLM initialized: %s/%s", lead_cfg.Provider, lead_cfg.Model)
		}
	}
}

// set_agent_prompt stores the agent's SystemPrompt() on the FuzzAgent.
func set_agent_prompt(agent Agent) {
	T := agent.Get()
	T.SetSystemPrompt(agent.SystemPrompt())
}

// set_agent_report sets the report for a given agent.
func set_agent_report(agent Agent, input *TaskReport) {
	T := agent.Get()
	T.Report = input
}

// agent_report_summary summarizes the agent report with error count.
func agent_report_summary(agent Agent, errors uint32) {
	T := agent.Get()
	if T.Report == nil {
		return
	}
	T.Report.Summary(errors)
}

// register registers an agent with the menu.
func (m *menu) register(name string, t_flag uint, agent Agent) {
	desc := agent.Desc()

	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.entries == nil {
		m.entries = make(map[string]*menu_elem)
	}
	cmd_name := strings.Split(name, ":")[0]
	flags := &FlagSet{EFlagSet: NewFlagSet(cmd_name, ReturnErrorOnly)}
	flags.SyntaxName(fmt.Sprintf("%s %s", os.Args[0], cmd_name))
	flags.ShowSyntax = true
	flags.AdaptArgs = true

	m.entries[name] = &menu_elem{
		name:   name,
		desc:   desc,
		t_flag: t_flag,
		agent:  agent,
		flags:  flags,
		parsed: false,
	}
	my_entry := m.entries[name]

	my_entry.flags.BoolVar(&global.single_thread, "serial", NONE)
	my_entry.flags.BoolVar(&global.debug, "debug", NONE)
	my_entry.flags.BoolVar(&global.snoop, "snoop", NONE)
	my_entry.flags.BoolVar(&global.sysmode, "quiet", NONE)
	my_entry.flags.DurationVar(&global.freq, "repeat", global.freq, NONE)

	switch t_flag {
	case _admin_agent:
		m.admin_agents = append(m.admin_agents, name)
	case _app:
		m.apps = append(m.apps, name)
	default:
		m.agents = append(m.agents, name)
	}
}

// showAgentSection displays a section of agents.
func (m *menu) showAgentSection(header string, agents []string) {
	if len(agents) == 0 {
		return
	}

	type sub_cmd struct {
		name string
		desc string
	}

	sub_menu := make(map[string][]sub_cmd)
	var sub_cats []string

	for _, k := range agents {
		if IsBlank(m.entries[k].desc) {
			continue
		}
		if val := strings.Split(m.entries[k].desc, ":"); len(val) > 1 {
			sub := strings.TrimSpace(val[0])
			desc := strings.TrimSpace(val[1])
			if _, ok := sub_menu[sub]; !ok {
				sub_cats = append(sub_cats, sub)
			}
			sub_menu[sub] = append(sub_menu[sub], sub_cmd{name: k, desc: desc})
			continue
		}
		m.cmd_text(k, m.entries[k].desc)
	}

	os.Stderr.Write([]byte(header))
	m.text.Write([]byte("\n"))
	m.text.Flush()

	for _, sub := range sub_cats {
		for _, v := range sub_menu[sub] {
			m.cmd_text(fmt.Sprintf(" %s", v.name), v.desc)
		}
		os.Stderr.Write([]byte(fmt.Sprintf(" %s:\n", sub)))
		m.text.Write([]byte("\n"))
		m.text.Flush()
	}
}

// Show displays the menu options to stderr.
func (m *menu) Show() {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if m.text == nil {
		m.text = tabwriter.NewWriter(os.Stderr, 25, 1, 3, '.', 0)
	}

	m.showAgentSection("Agents:\n", m.agents)
	m.showAgentSection("Apps:\n", m.apps)
	m.showAgentSection("Admin:\n", m.admin_agents)

	os.Stderr.Write([]byte(fmt.Sprintf("\n  chat .................. \"Interactive AI chat with agent access.\"\n\n")))
	os.Stderr.Write([]byte(fmt.Sprintf("For extended help on any command, type %s <command> --help.\n", os.Args[0])))
}

// get_agentstore returns the database bucket for the agent.
func get_agentstore(name string) Database {
	return global.db.Bucket(name)
}

// Select processes the provided input to execute agents.
func (m *menu) Select(input [][]string) (err error) {
	for input == nil || len(input) == 0 {
		return eflag.ErrHelp
	}

	// Initialize selected agent.
	init := func(args []string) (err error) {
		source := args[len(args)-1]
		if x, ok := m.entries[args[0]]; ok {
			x.flags.FlagArgs = args[1 : len(args)-1]
			var show_help bool
			if len(x.flags.FlagArgs) == 0 {
				show_help = true
			}

			set_agent_db(x.agent, get_agentstore(strings.Split(x.name, ":")[0]))
			set_agent_llm(x.agent)
			set_agent_flags(x.agent, *x.flags)

			if err := x.agent.Init(); err != nil {
				if show_help || err == eflag.ErrHelp {
					if err != eflag.ErrHelp {
						Stderr("%s: %s\n\n", x.name, err.Error())
					}
					x.flags.Usage()
					Exit(0)
				}
				if err != eflag.ErrHelp {
					if source != "cli" {
						Stderr("err [%s]: %s\n\n", source, err.Error())
					} else {
						Stderr("err: %s\n\n", err.Error())
					}
				}
				x.flags.Usage()
				Exit(1)
			} else {
				set_agent_prompt(x.agent)
				x.parsed = true
			}
		}
		return nil
	}

	// Initialize database
	init_database()
	wireToolDB()

	// Initialize all agents from cli arguments.
	for n, args := range input {
		m.mutex.RLock()
		if x, ok := m.entries[args[0]]; ok {
			if !x.parsed {
				init(args)
			} else {
				// Agent already is initialized, so clone the agent and parse new variables.
				i := 0
				for k := range m.entries {
					if strings.Contains(k, args[0]) {
						i++
					}
				}
				new_name := fmt.Sprintf("%s:%d", x.name, i)
				new_agent := reflect.New(reflect.TypeOf(x.agent).Elem()).Interface().(Agent)
				m.mutex.RUnlock()
				command.RegisterName(new_name, new_agent)
				m.mutex.RLock()
				input[n][0] = new_name
				init(args)
			}
		} else {
			m.mutex.RUnlock()
			return fmt.Errorf("No such command: '%s' found.\n\n", args[0])
		}
		m.mutex.RUnlock()
	}

	if global.debug || global.snoop {
		enable_debug()
	}

	if global.snoop {
		enable_trace()
	}

	init_logging()

	Info("### %s v%s ###", APPNAME, VERSION)
	Info(NONE)

	// Main agent loop.
	for {
		tasks_loop_start := time.Now().Round(time.Millisecond)
		task_count := len(input) - 1
		for i, args := range input {
			m.mutex.RLock()
			if x, ok := m.entries[args[0]]; ok {
				if x.parsed {
					DefaultPleaseWait()
					PleaseWait.Show()
					name := strings.Split(x.name, ":")[0]
					source := args[len(args)-1]
					pre_errors := ErrCount()
					if source == "cli" {
						Info("<-- agent '%s' started -->", name)
					} else {
						Info("<-- agent '%s' (%s) started -->", name, source)
					}
					Info("\n")

					set_agent_report(x.agent, NewTaskReport(name, source, x.flags))
					report := Defer(func() error {
						agent_report_summary(x.agent, ErrCount()-pre_errors)
						return nil
					})
					if err := x.agent.Main(); err != nil {
						Err(err)
					}
					DefaultPleaseWait()
					report()
					if source == "cli" {
						Info("<-- agent '%s' stopped -->", name)
					} else {
						Info("<-- agent '%s' (%s) stopped -->", name, source)
					}
					if i < task_count {
						Info(NONE)
					}
				}
			}
			m.mutex.RUnlock()
		}

		PleaseWait.Hide()

		// Stop here if this is non-continuous.
		if global.freq == 0 {
			return nil
		}

		runtime.GC()

		// Agent Loop
		if ctime := time.Now().Add(time.Duration(tasks_loop_start.Round(time.Second).Sub(time.Now().Round(time.Second)) + global.freq)).Round(time.Second); ctime.Unix() > time.Now().Round(time.Second).Unix() && ctime.Sub(time.Now().Round(time.Second)) >= time.Second {
			Info(NONE)
			Info("Next agent cycle will begin at %s.", ctime)
			for time.Now().Sub(tasks_loop_start) < global.freq {
				ctime := time.Duration(global.freq - time.Now().Round(time.Second).Sub(tasks_loop_start)).Round(time.Second)
				Flash("* Agent cycle will restart in %s.", ctime.String())
				if ctime > time.Second {
					time.Sleep(time.Duration(time.Second))
				} else {
					time.Sleep(ctime)
					break
				}
			}
		}
		Info("Restarting agent cycle ... (%s has elapsed since last run.)", time.Now().Round(time.Second).Sub(tasks_loop_start).Round(time.Second))
		Info(NONE)
	}
}

// execAgent sets up and runs an agent from a menu entry, capturing its output.
// This is the shared implementation used by both chat.go and agent delegation.
func execAgent(entry *menu_elem, args []string) (string, error) {
	agent := entry.agent

	cmdName := strings.Split(entry.name, ":")[0]
	flags := &FlagSet{EFlagSet: NewFlagSet(cmdName, ReturnErrorOnly)}
	flags.FlagArgs = args
	set_agent_flags(agent, *flags)
	set_agent_db(agent, get_agentstore(cmdName))
	set_agent_llm(agent)
	set_agent_report(agent, NewTaskReport(cmdName, "chat", flags))

	if err := agent.Init(); err != nil {
		return "", err
	}
	set_agent_prompt(agent)

	// Capture output.
	var buf bytes.Buffer
	nfo.SetOutput(nfo.STD, &buf)
	nfo.SetOutput(nfo.AUX, &buf)
	defer func() {
		nfo.SetOutput(nfo.STD, os.Stderr)
		nfo.SetOutput(nfo.AUX, os.Stderr)
	}()

	if err := agent.Main(); err != nil {
		captured := buf.String()
		if captured != "" {
			return captured, err
		}
		return "", err
	}

	return buf.String(), nil
}
