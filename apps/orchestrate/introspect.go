package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// introspectToolDef builds the per-turn `introspect` tool: read-only,
// code-derived self-awareness. It reports what the agent ACTUALLY is —
// configuration, capabilities, channels + cortex, and memory state — read
// straight from the agent record + registries + bindings, so the agent answers
// "what can you do / how are you set up / what are you connected to" from FACT
// instead of imagining it (the same gap behind self-description confabulation).
// Never writes.
func (t *chatTurn) introspectToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "introspect",
			Description: "Get accurate, CURRENT details about YOURSELF and your gohort setup — configuration, capabilities (tools/skills/knowledge), channels + cortex, and memory state — read straight from the framework. Use it to answer \"what can you do?\" / \"how are you set up?\" / \"what are you connected to?\" from FACT instead of guessing, and to check your own config before acting. Read-only.",
			Parameters: map[string]ToolParam{
				"section": {Type: "string", Description: "Optional. Which slice to report: \"identity\", \"capabilities\", \"channels\", \"memory\", or \"all\" (default). Omit for the full picture."},
			},
			Caps: []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			section := strings.ToLower(strings.TrimSpace(stringArg(args, "section")))
			if section == "" {
				section = "all"
			}
			want := func(s string) bool { return section == "all" || section == s }
			a := t.agent
			var b strings.Builder

			if want("identity") {
				b.WriteString("## Identity\n")
				b.WriteString("- Name: " + chFirst(a.Name, a.ID) + "\n")
				if d := strings.TrimSpace(a.Description); d != "" {
					b.WriteString("- Description: " + d + "\n")
				}
				typeBits := []string{}
				if a.Cortex {
					typeBits = append(typeBits, "Cortex (persistent home thread) ON")
				} else {
					typeBits = append(typeBits, "Cortex OFF")
				}
				if a.Fleet {
					typeBits = append(typeBits, "Fleet (delegation + scheduling) ON")
				}
				b.WriteString("- Type: " + strings.Join(typeBits, "; ") + "\n")
				mode := a.MemoryMode
				if mode == "" {
					mode = "agent"
				}
				b.WriteString("- Memory mode: " + mode + "\n")
				if r := strings.TrimSpace(a.Rules); r != "" {
					b.WriteString("- Rules (always applied):\n")
					for _, line := range strings.Split(r, "\n") {
						if line = strings.TrimSpace(line); line != "" {
							b.WriteString("    - " + line + "\n")
						}
					}
				}
				b.WriteString("\n")
			}

			if want("capabilities") {
				b.WriteString("## Capabilities\n")
				if len(a.AllowedTools) == 0 {
					b.WriteString("- Tools: default pool (every read + network tool)\n")
				} else {
					b.WriteString("- Tools (allowlist): " + strings.Join(a.AllowedTools, ", ") + "\n")
				}
				if len(a.AllowedSkills) == 0 {
					b.WriteString("- Skills: none attached\n")
				} else {
					name := map[string]string{}
					for _, sk := range LoadSkills(t.udb, t.user) {
						name[sk.ID] = sk.Name
					}
					var ss []string
					for _, id := range a.AllowedSkills {
						ss = append(ss, chFirst(name[id], id))
					}
					b.WriteString("- Skills: " + strings.Join(ss, ", ") + "\n")
				}
				if len(a.AttachedCollections) == 0 {
					b.WriteString("- Knowledge collections: none explicitly attached\n")
				} else {
					cname := map[string]string{}
					for _, c := range listCollections(t.udb, t.user) {
						cname[c.ID] = c.Name
					}
					var cs []string
					for _, id := range a.AttachedCollections {
						cs = append(cs, chFirst(cname[id], id))
					}
					b.WriteString("- Knowledge collections: " + strings.Join(cs, ", ") + "\n")
				}
				b.WriteString("\n")
			}

			if want("channels") {
				b.WriteString("## Channels & cortex\n")
				chans := ListChannelsForAgent(RootDB, t.user, a.ID)
				switch {
				case a.Cortex && len(chans) == 1:
					b.WriteString("- Cortex ON with one channel → that channel RELAYS into your cortex; the conversation (in + out) lives in your cortex thread, not a separate channel thread.\n")
				case a.Cortex:
					b.WriteString("- Cortex ON with multiple channels → each channel keeps its own per-room thread and feeds your cortex digests.\n")
				default:
					b.WriteString("- Cortex OFF → channels (if any) use per-room threads.\n")
				}
				if len(chans) == 0 {
					b.WriteString("- No channels bound.\n")
				} else {
					for _, ch := range chans {
						parts := []string{chFirst(ch.Name, ch.Address, ch.ID)}
						if ch.Service != "" {
							parts = append(parts, ServiceDisplayName(ch.Service))
						} else {
							parts = append(parts, "inert (no source hooked)")
						}
						dir := strings.TrimSpace(ch.Direction)
						if dir == "" {
							dir = "bidirectional"
						}
						parts = append(parts, "direction="+dir)
						if ch.AutoReply {
							parts = append(parts, "auto-reply")
						}
						if strings.TrimSpace(ch.Gatekeeper) != "" {
							parts = append(parts, "gatekeeper set")
						}
						b.WriteString("- " + strings.Join(parts, " · ") + "\n")
					}
					// Reply-routing grounding — matches the per-turn [CHANNEL CONTEXT]
					// note on each inbound: a reply goes BACK to the conversation it
					// came from automatically; reaching anyone else is a separate send.
					b.WriteString("- Reply routing: when a message reaches you on a channel, your reply is delivered straight back to that same conversation automatically — no tool needed, and you're already \"on\" it. Reaching a DIFFERENT person or channel is a separate, proactive outbound message (which may be gated).\n")
				}
				b.WriteString("\n")
			}

			if want("memory") {
				b.WriteString("## Memory & state\n")
				facts := ListMemoryFacts(t.udb, factsNamespace(a.ID))
				b.WriteString(fmt.Sprintf("- Explicit memory (always-in-prompt saved facts): %d\n", len(facts)))
				if ents, edges := GraphCounts(t.udb, factsNamespace(a.ID)); ents > 0 || edges > 0 {
					b.WriteString(fmt.Sprintf("- Graph memory (entities + relationships, via link_entities / recall_about): %d entities, %d edges\n", ents, edges))
				}
				if a.Cortex {
					if sess, ok := loadChatSession(t.udb, a.ID, cortexSessionID(a.ID)); ok && len(sess.Messages) > 0 {
						obs := 0
						for _, m := range sess.Messages {
							if strings.TrimSpace(m.ReportFrom) != "" {
								obs++
							}
						}
						b.WriteString(fmt.Sprintf("- Cortex thread: %d message(s), %d observation card(s)\n", len(sess.Messages), obs))
					} else {
						b.WriteString("- Cortex thread: empty\n")
					}
				}
				b.WriteString("\n")
			}

			out := strings.TrimSpace(b.String())
			if out == "" {
				return "Unknown section — use one of: identity, capabilities, channels, memory, all.", nil
			}
			return out, nil
		},
	}
}
