// App-specific UI bits loaded into the orchestrate chat page's <head>.
// Goals:
//
//  1. CSS + renderers for the in-chat plan, intent, and draft blocks —
//     visually 1:1 with servitor's equivalents (servitor_plan,
//     servitor_intent, servitor_draft). Both apps converge on the
//     same look so a user familiar with one immediately recognizes
//     the other.
//  2. CSS for interjection bubble affordances (edit/delete).
//  3. Client actions for the chat-page toolbar (edit/clone/etc).
//
// The markup itself lives in assets/web_assets.html (a <style> block followed
// by a <script> block) so it can be edited as real HTML/CSS/JS — highlighting,
// linting, formatting, and legal backticks/template literals — instead of a Go
// raw string. It is injected verbatim into the page <head> (see page_chat.go).

package orchestrate

import _ "embed"

//go:embed assets/web_assets.html
var orchestrateWebAssets string
