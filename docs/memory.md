# Memory

What an agent remembers from one conversation to the next. Memory is set
**per agent** and kept **per person**: what an agent learns while you use it
is in your copy, and the owner decides which of their own layers travel with
the agent when they share it.

Memory is not the same as knowledge. **Knowledge** is documents you give an
agent (uploads and attached collections), and it is always available. Memory
is what the agent saves itself while it works, and what it is told to keep.

## The layers

| Layer | What it holds | When the agent sees it |
|---|---|---|
| **Pinned notes** | Short notes that shape every turn: a preference, a name, how you like answers | On every turn, in its prompt |
| **Searchable memory** | Longer findings it saves as it works: an API's quirks, a recipe that worked, a detail from a document | When it looks them up with `recall`, or a recall hint points at one |
| **Graph memory** | Named things (a service, a team, a person) and how they relate | When it asks about one by name |
| **Session recall** | A marker for each past conversation: what was asked, what it made | When it searches earlier conversations, with a link back to each |
| **Cortex** | The record of what reached the agent outside a conversation: messages, scheduled runs, requests from other agents | Only when **Reads its Cortex** is on |

On top of these, the current conversation is carried forward: the most recent
messages word for word, and older ones folded into a running summary.

## Setting it up

Open the agent, then **Agent › Edit**. The **Memory** section has:

- **Memory mode**, which decides what the agent pins:
  - **Agent: generalized lessons only**, for a specialist. It pins lessons
    about doing its job, and saves specifics to searchable memory instead.
  - **Chatbot: lessons + user personalization**, for an assistant. It also
    pins what it learns about you.
- **Reads its Cortex**. Off, the Cortex is a record for you (the 📋 row at the
  top of the conversation list) and the agent never sees it. On, it is the
  agent's standing home thread (🧠): its recent lines go into every
  conversation, so the agent arrives knowing what has been happening.
- **Session recall**: lets the agent find earlier conversations and link you
  to them. On by default in Chatbot mode, off in Agent mode.
- **Pinned notes** and **Searchable memory**: turn either layer off. An agent
  that should answer only from its sources keeps searchable memory off.
- **Recall hints**: each turn, the agent sees a short list of titles from its
  knowledge and memory that look relevant, and pulls the one it needs. Best
  for an agent with a real body of documents.

The **Context** section below it sets how many recent messages are kept word
for word (12 by default) and whether older ones are summarized or dropped.

The new-agent wizard asks the same question more simply: **Personalized**
(Chatbot) or **Lessons only** (Agent).

A sub-agent has one switch instead, **Keeps its own memory**. Its memory is
its own; the parent never reads it and gets only what the sub-agent reports.

## How an agent saves and deletes

The agent has three memory tools:

- **`remember`** saves a pinned note or a finding. A pinned note records
  whether it was told or worked out, and whether the person saying it settles
  it (their own preference) or not (how a server behaves). A note that holds
  only for a while can carry an expiry.
- **`recall`** searches every layer, or reads one item by its id.
- **`forget`** deletes one item by its id. Asked to forget without an id, it
  lists the closest matches and deletes nothing until it has the right one.
  There is no tool to clear everything.

Knowledge and conversation history cannot be deleted through `forget`:
knowledge is managed where it was added, and history is the record of what
was said.

You can also just tell it: "remember that I use metric units", "forget what
you saved about the old server".

## Seeing and pruning what it remembers

**Configure › Memory** opens the agent's memory window:

- **Search** every layer. **Stored text** finds any entry by its words;
  **Recall preview** shows what the agent actually gets back for a question,
  which is the quickest way to find a memory steering it wrong.
- **Saved facts**: the pinned notes, with **+ Add** and delete.
- **Reference Memory**: the saved findings. Delete one that has drifted, or
  **Wipe all**.
- **Graph Memory**: delete an entity with its links, or one relationship.
- **Needs attention**: entries that name something no longer there, or that
  record work done rather than how things are. **Remove** or **Ignore** each.
- **Moved out of saved notes**: past events and closed items (see below),
  each with **Undo**.

Everything here acts on your own copy.

## Memory keeps itself tidy

- **Duplicates and changes.** A note that repeats one already saved is not
  saved twice, and a changed fact replaces the stale one.
- **Pruning.** When an agent's pinned notes reach 40, a background pass
  removes junk and merges overlaps. Past 60, the least recently updated go.
- **Events move on.** A note about something that happened on a date (a
  trip, an appointment) moves to searchable memory after 14 days, so the
  agent stops raising it as news. Search still finds it.
- **Open items close.** A note about work still waiting on someone is asked
  about once after 14 idle days, and closes if nobody picks it back up. One
  untouched for 30 days closes without asking. Closed items can be restored.

An administrator sets these numbers, the caps on findings and graph size, and
automatic graph extraction (off by default) under **Administrator › Tuning ›
Memory**.

## Conversations that leave nothing behind

- **New incognito session** (beside New session) starts a clean room: no
  memory or Cortex carried in, and nothing saved back. It is set when the
  conversation is made.
- **Clean**, one of the per-turn modes, keeps searchable memory out of a
  single turn, so the agent answers from its sources alone.

Neither leaves a session recall marker, and deleting a conversation deletes
its marker.

## When an agent is shared

Each person who uses a shared agent has their own copy of its memory. What
the agent learns in their conversations is saved there, and they can see and
prune it in their own memory window. Nobody else's conversations reach it.

The owner chooses which of **their** layers a recipient also reads, under
**Agent › Share**, in **What a recipient sees**:

| Setting | What it lends | Default |
|---|---|---|
| **Its standing activity** | The owner's Cortex lines | Shared |
| **What it worked out** | The owner's searchable findings | Shared |
| **Its saved notes** | The owner's pinned notes | Private |
| **Adding documents of their own** | Whether recipients may upload | Allowed |

A lent layer is read-only to the recipient, and these are enforced, not
suggestions: a layer that is not lent is never in the recipient's prompt.
The default for every agent is set by an administrator; an agent's own
choice overrides it.

**People outside gohort** (through a channel or the outside API) each get a
store of their own. The agent's **Security › Share** page lists them under
**People it has served**, with **View**, **Clear memory** (keeps the
conversations) and **Delete** (removes them from every agent they reached),
which is what to do when someone asks to be forgotten.

## Choosing settings

- **A specialist** (a troubleshooter, a researcher): Agent mode, searchable
  memory on, session recall off. Its pinned notes stay lessons, not details
  of one case.
- **An agent that works one case after another**: also turn **Searchable
  memory** off, so one case's findings do not surface in the next by
  similarity.
- **A personal assistant**: Chatbot mode, session recall on, and **Reads its
  Cortex** on if it is woken by schedules or messages.
- **An agent that must answer only from approved material**: pinned notes and
  searchable memory off, recall hints on.
