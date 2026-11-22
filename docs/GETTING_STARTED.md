# Getting started

The README's Quick start gets gohort running. This guide takes a fresh
instance to an agent you use, starting after you have run `gohort --setup`,
started `gohort serve`, and signed in as the administrator.

Steps 1 and 2 are required. The rest are optional, in any order.

## 1. Connect a model

Nothing runs without one. On a fresh instance the dashboard says so, and
**Administrator** opens **Connect a language model**:

1. **Provider.** A local server (Ollama, llama.cpp or vLLM) needs its
   address; Anthropic, OpenAI, Gemini and AWS Bedrock need a key. For
   another hosted provider that speaks the OpenAI API (OpenRouter, Groq and
   others), choose OpenAI with that provider's address.
2. **Model.** Pick one, or leave it blank for the provider's default.
3. **Check.** gohort tries the model before saving it.

Pick a model that can call tools: agents work by calling them. This is the
**worker**, which runs most work. Change it later under **Administrator ›
Models**, where you also add a stronger **lead** model and set each tier's
default thinking.

## 2. Make your assistant

Open **Agents**. With no agent of your own yet, it opens **Create your
personal assistant**: Welcome, Personality, Purpose, About you, Name,
Create. Nothing is made until the last step, and everything stays
editable. This is the agent you talk to; it can hand work to specialist
agents you add later.

Ask it something that needs a tool, such as *What time is it in Tokyo, and
how many days until 25 December?* The reply's **tools** button shows each
call and what came back.

Set your **timezone** on the **Account** page so dates, "today" and
schedules use yours, not the server's.

## 3. Give it something to know

Agents can answer from your own documents.

1. Knowledge searches by meaning, using an embedding model. By default
   that is the built-in embedder, which runs inside gohort and downloads
   its model (about 130 MB) on first use; progress shows under
   **Administrator › Services › Embeddings**. To use an embedding server
   instead, choose it there, adding a connector from its template if it is
   not listed.
2. Open **Knowledge**, press **+ New collection**, and add documents (PDF,
   Word, Markdown, text, web pages and more).
3. On your agent's page, open **Configure › Knowledge** and attach the
   collection.

The agent then finds passages when a question calls for them, and sees
when each was added, so an old document reads as old.

## 4. Talk to it

For voice, choose servers under **Administrator › Services**, in **Audio
Transcription (STT)** and **Speech (TTS)**. Each list ends with **+ Add
another...**; the whisper.cpp / OpenAI-compatible templates fit most local
servers. The chat composer then shows a microphone, and each reply a
read-aloud button.

## 5. Use it from another program

Programs use an API key: on the **Account** page, under API keys, press
**+ New API key**. Each key does one thing, chosen when you make it:

- **MCP**: an MCP client such as Claude Desktop, reaching your agents and
  their tools. Endpoint and setup: **Bridges** page, under MCP Server.
- **Model API**: any program that speaks the OpenAI API, reaching your
  agents and channels at `/v1`.
- **Raw models**: the worker and lead models with no agent in front, over
  `/v1` or the Ollama-compatible model proxy.
- **External contacts**: a chat widget or similar, for people with no
  gohort account. Each person gets their own memory.

The secret is shown once, when the key is made; copy it then.

On `/v1`, agents think as they are set to. A caller with someone waiting on
a voice line sends `"reasoning_effort": "none"` to skip thinking.

## 6. Add people

Under **Administrator › People**:

- **Add account** creates a user. **Allow public signup**, under
  **Sign-in**, lets people sign up themselves.
- **Teams** groups people, so a whole team can be given access at once.
- **Feature Access** sets who may use what. Most features are open to
  everyone until you save a list, which then means exactly who is on it.
  **Model API** and **Model access** reach the models directly, so they are
  closed until granted; administrators always have both. Grant them to the
  people or teams who need them, and set each person's raw model use under
  **Model access limits**.

Owners share an agent, collection or tool from its own sharing list.

## 7. Work on code with Anvil

**Anvil**, on the dashboard, is a coding assistant that works on a project.
Under **＋ New project**:

- **Start a new repository**: an empty one, for anyone.
- **One of your apps**: the app as its files (`app.json`, its pages and
  scripts).
- **Upload a bundle**: a repository brought as a git bundle, where an
  administrator allows it.
- **A repository or a directory** (administrators): a repository checked out
  for you, or a folder on this server, edited in place.

Ask for a change in the project's chat. Anvil reads before it edits, shows
each edit as a card you can revert, and runs the project's check (set in
**Settings**) before it finishes, working on while the check fails. The
commands it runs are sandboxed, with no network, in that checkout only, and
it asks before deleting a file.

When the work is done:

- **Commit** records it as yours, with a message (✨ Draft message writes
  one). Anvil never pushes: **Publish** pushes the commits to a repository
  elsewhere, where allowed, and **Changes as a patch** or **The repository
  as a bundle** downloads them.
- In an app project, **Preview** opens your working copy (only you see it),
  **Install** puts it live through the app's checks, and **History** lists
  every version the app went live as, what each changed, and a way back to
  it. **Install as a new app** makes a project holding `app.json` one of
  your apps.

## Where to go next

- **Scheduler**, in the Agents sidebar: run an agent on a schedule, or
  wake it when something it watches changes. Each schedule shows how its
  last run went.
- **Bridges**: answer messages from other services. iMessage comes through
  the `gohort-desktop` app on a Mac; add Teams bots and linked services
  under **Extensions › Integrations**.
- **Build**, on any agent's page: describe a tool, app or agent and Builder
  makes it.
- **Servitor**: give agents your SSH systems and git repositories to
  investigate.
- **Administrator › Overview › System Status**: health of the sandbox,
  background work and the rest.

[docs/REFERENCE.md](REFERENCE.md) covers the whole feature surface, and
[SECURITY.md](../SECURITY.md) the trust model.
