// App-specific UI bits loaded into the servitor chat page's <head>.
// Two responsibilities:
//
//  1. CSS for servitor-specific block shapes (intent narration, plan
//     checklist, draft preview, etc.) so the shared core/ui CSS stays
//     domain-agnostic.
//  2. JS registering block renderers for the four servitor event
//     kinds that the chat_bridge.go translator routes through
//     kind: "block": servitor_intent, servitor_plan,
//     servitor_notes_consumed. The framework calls
//     window.UIBlockRenderers[<type>] with the event data and
//     expects a {wrap, body, onDone?} object back.
//  3. JS registering client actions for the chat toolbar
//     (servitor_open_rules, servitor_run_map).
//     Each opens a modal that hits the existing legacy endpoints.
//
// The markup itself lives in assets/web_assets.html (external xterm refs, a
// <style> block, then a <script> block) so it can be edited as real
// HTML/CSS/JS — highlighting, linting, formatting, and legal backticks —
// instead of a Go raw string. It is injected verbatim into the page <head>
// (see chat_page.go).

package servitor

import _ "embed"

//go:embed assets/web_assets.html
var servitorWebAssets string
