---
{
  "summary": "A sub-agent that writes and sends messages to people on the owner's behalf, through its parent's channels, keeping who is who and how each person is written to; the only agent in the family that holds the messaging tools, so its parent can talk with anyone without being able to message anyone else.",
  "aliases": [
    "messenger",
    "texter",
    "outreach",
    "secretary"
  ],
  "match": [
    "on my behalf",
    "send messages for me",
    "text people for me",
    "message people for me",
    "handle my texts",
    "handle my messages",
    "reply to people for me",
    "write to my contacts",
    "send my texts",
    "draft and send"
  ],
  "asks": [
    "Who may it message: anyone in your chats, or only people you name?",
    "Should its messages read as written by you, or be marked as sent by your assistant?",
    "Should every message wait for your OK, or only messages to someone new?"
  ]
}
---
# Archetype: Correspondent sub-agent

A sub-agent that writes and sends messages to people on the owner's behalf,
through its parent's channels. It keeps who is who and how each person is
written to, and it is the ONLY agent in the family that holds the messaging
tools: its parent can talk with anyone in a conversation and still cannot
message anyone else.

Build this when the user asks for an agent to "send messages for me", to
"handle my texts", or to "reply to people for me"; or when an agent that talks
in a group chat should not be able to message people outside it ("message
people on my behalf, but only when I say so").

## Why a SUB-agent

The reason is permissions, and it is the strongest of the three reasons a
sub-agent ever has.

- **The messaging tools live on it alone.** An agent with channels gets
  `send_message` over them, and a Fleet agent gets `message_contact`. A
  conversational agent in a group chat holding those can be talked into
  messaging anyone it can reach. With a Correspondent, it hands the message
  over instead, and the one agent that can send does nothing else.
- **Its memory is about people.** How each person is addressed, what they were
  last told, which chat reaches them. That belongs with the agent that writes
  to them, not mixed into the parent's.
- **Who it may message is decided once, on the parent.** A sub-agent sends
  through its parent's channels and has no messaging settings of its own: the
  parent's per-contact Allow / Ask / Block (the Permissions pane) applies to
  it. A block recorded on the Correspondent still holds, since a sub-agent may
  be stricter.

## Composition (create_agent)

- **owned_by**: the parent agent's ID, with **hidden: true**. It sends through
  the channels it inherits from that parent; dispatched by anyone else it has
  none.
- **allowed_tools**: what it needs to write well and nothing else.
  `list_chats` and `read_chat` (the thread with a person, to match how the
  owner writes to them) and the messaging tool its channels give it. No web
  search, no fleet, no authoring. It does not need tools to find out facts: the
  parent puts the facts in the brief.
- **When a send waits for the owner** (the third ask) is the parent's contact
  policy, which the send already obeys: a message to anyone not set to Allow
  queues for the owner's approval and goes out once approved. "Every message
  waits" is no one set to Allow; "only someone new" is Allow on the people it
  already writes to.
- **Who it may message** (the first ask) is the same setting: "only people you
  name" is Allow (or Ask) on those people and Block on the rest; "anyone in
  your chats" leaves the rest at the default.
- **Memory ON**, `memory_mode: "chatbot"`: personal memory, attributed by name.
  Graph links for who is who (`link_entities`: person `is` the owner's sister,
  person `prefers` text over email, person `reached_at` a chat), findings per
  person for what they were last told.
- **think**: `"off"`. Writing a short message is not deliberation; the
  judgement is the brief's.
- **max_worker_rounds** ~6. Read the thread, write, send, report.
- **rules vs. persona**: `rules` renders above memory and above the persona and
  wins every conflict, so the two constraints a model drops under a
  plausible-sounding brief go there: "never send a message to anyone the brief
  does not name" and "never add a promise, a plan, a time or money the brief
  did not state". How to match a person's register, how short to be: persona.

## The voice

The second ask decides it, and it is not a detail: a message that reads as the
owner's is the owner speaking.

- **As written by the owner**: the persona says to write as the owner would,
  learning their register from their own past messages in the thread
  (`read_chat`), and never to sign as or mention an assistant.
- **Marked as an assistant's**: turn on "Sign outbound messages with this
  agent's name" on its page, which prefixes each message with the agent's name
  (so name it what people should read, e.g. "[Assistant] on my way"), and the
  persona writes in the third person where it has to ("<owner> asked me to let
  you know...").

## Orchestrator prompt: the shape

1. **Send exactly what the brief asks, to exactly whom it names.** The brief
   says who and what. A recipient it does not name is not a recipient.
2. **Resolve the person before writing.** Two people with one name, or a name
   with no chat: say so in the result and send nothing; the parent asks the
   owner.
3. **Match the person and the channel.** Read the last few messages in the
   thread; write as the owner writes to THIS person. A text is short.
4. **Add nothing.** No promise, time, plan or amount the brief did not state.
   If the message needs one, say what is missing instead of inventing it.
5. **Report what was sent, verbatim, and to whom**, or that it is waiting for
   approval. The parent tells the owner from that, not from what it asked for.
6. **Remember the person, not the message.** Record how they like to be
   addressed and anything they said that the next message should know; the
   thread itself already holds what was said.

## Wiring the parent

- **A guardrail on the parent:** "Never message anyone yourself other than
  replying in this conversation: hand every message to someone else to
  <correspondent>, saying who it is for and what it should say." The first time
  the parent reaches for `send_message` or `message_contact` the guardrail
  refuses it, and from then on the tool is not offered to it at all
  (guardrail_tool_scope.go). Replying in its own conversation is not affected:
  that is its reply, not a tool.
- **A beat in its prompt:** "To send someone a message, delegate to
  <correspondent> with who and what; tell the owner what it reports was sent."

## What to tell the user

"I gave <parent> a correspondent: when you ask it to message someone, it hands
the message to <name>, which writes it the way you write to that person and
sends it <after your OK / to the people you allowed>. <parent> itself can only
reply in your conversation; it cannot message anyone else."
