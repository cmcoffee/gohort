// A map of this package, by file name.
//
// orchestrate is one package of several hundred files, and the files are named
// for the part they belong to: everything about collections starts collection_,
// everything about guardrails starts guardrail_. To find a part, list its
// prefix; to see what a part reaches into, run
//
//	go run ./scripts/cutmap apps/orchestrate <prefix>
//
// The larger groups, and what each one holds:
//
//	agent_       one agent: its access, credentials, dispatch, family,
//	             maintainers, media, memory and feedback
//	agents_      the agent store: seeding, export, migrations, the agents tool
//	             and its HTTP routes
//	app_         apps built in gohort: the app_def authoring tool, drafts,
//	             bundles, copies, checks and the agents an app owns
//	builder_     Builder, the agent that builds agents and apps: its tools,
//	             delegation and owner jobs
//	collection_  knowledge collections: curation, held pages, second sources,
//	             vetting, suggestions and the steward
//	console_     the console's sections: fleet, activity, approvals,
//	             scheduler, monitors, permissions, tool health
//	dispatch_    sending work from one agent to another: targets, fan-out,
//	             lineage, progress, escalation
//	guardrail_   owner guardrails and the warden that judges them: checks
//	             before and after output, appeals, declines, history
//	machine_     machines (an agent's phased procedure): editor, runs, drafts,
//	             previews, sharing
//	memory_      an agent's memory: modes, scope, pool, search, lifecycle, audit
//	operator_    the Operator agent's fleet tools: standing agents, monitors,
//	             schedules, wakes
//	page_        the agent pages: chat, the wizard, access, fleet security
//	pipeline_    pipelines: stages, the per-stage editor, runs, sharing
//	runner_      one chat turn from request to reply: routing, the tool
//	             catalog, the lead and worker split, the stream, the trace
//	session_     a conversation: starting one, its context, cards, the
//	             diagnostics trail
//	tool_        tool policy: grants, groups, elevation, confirmations, the
//	             outcome and verify ledgers
//
// Smaller groups follow the same rule (channel_, schedule_, run_, turn_,
// reply_, lead_, graph_, bulletins_, publish_, objective_, authoring_,
// admin_..._scope), and a part that is one file is named for what it does.
//
// ceiling_test.go holds the package's file count; a new file is a decision
// someone has to type a number for.

package orchestrate
