# Getting started

The README's Quick start gets gohort installed and running. This picks up from
there: from a fresh instance to an agent you actually use. You have run
`gohort --setup`, started `gohort serve`, and signed in as the administrator.

Only the first two steps are needed. The rest add what you want, in any order.

## 1. Connect a model

Nothing runs without one. On a fresh instance the dashboard says so, and
opening **Administrator** takes you straight to **Connect a language model**:

1. **Provider.** A local server (Ollama, llama.cpp or vLLM) needs the address
   it listens on. Anthropic, OpenAI, Gemini and AWS Bedrock need their key.
   Another hosted provider that speaks the OpenAI API (OpenRouter, Groq and
   others) is OpenAI with that provider's address.
2. **Model.** Pick one, or leave it blank for the provider's default.
3. **Check.** gohort tries the model before saving it.

Use a model that can call tools: agents work by calling them. This is the
**worker**, the model most work runs on. Everything about it can be changed
later under **Administrator › Models**, which is also where a second, stronger
**lead** model is added and where each tier's default thinking is set.

## 2. Make your assistant

Open **Agents**. With no agent of your own yet, it opens **Create your personal
assistant**: Welcome, Personality, Purpose, About you, Name, Create. Nothing is
made until the last step, and all of it can be edited afterwards. This is the
agent you talk to; it can hand work to specialist agents you add later.

Ask it something it needs a tool for, such as *What time is it in Tokyo, and
how many days until 25 December?* The reply comes with a **tools** button:
open it to see each call the agent made and what came back.

While you are here, set your **timezone** on the **Account** page, so dates,
"today" and schedules are yours rather than the server's.

## 3. Give it something to know

Agents can answer from your own documents.

1. Knowledge searches by meaning, with an embedding model. Out of the box
   that is the built-in embedder, which runs inside gohort and downloads its
   model (about 130 MB) once, the first time it is used; its progress is under
   **Administrator › Services › Embeddings**. To use an embedding server
   instead, choose it there, adding a connector from its template if it is
   not listed.
2. Open **Knowledge**, press **+ New collection**, and add documents (PDF,
   Word, Markdown, text, web pages and more).
3. On your agent's page, open **Configure › Knowledge** and attach the
   collection.

The agent now finds passages from it when a question calls for them, and sees
when each was added, so an old document reads as old.

## 4. Talk to it

To speak to an agent and hear it answer, choose a transcription and a speech
server under **Administrator › Services**, in **Audio Transcription (STT)** and
**Speech (TTS)**. Each list ends with **+ Add another...**: the whisper.cpp /
OpenAI-compatible templates fit most local servers. Once chosen, the chat
composer shows a microphone, and each reply gets a read-aloud button.

## 5. Use it from another program

Programs reach your agents with an API key. On the **Account** page, under API
keys, press **+ New API key**. A key does one thing, chosen when you make it:

- **MCP**: an MCP client such as Claude Desktop, reaching your agents and
  their tools. The endpoint and setup are on the **Bridges** page, under MCP
  Server.
- **Model API**: any program that speaks the OpenAI API, reaching your agents
  and channels at `/v1`.
- **Raw models**: the worker and lead models with no agent in front, over
  `/v1` or the Ollama-compatible model proxy.
- **People outside gohort**: a chat widget or similar, calling for people who
  are not gohort users. Each person gets their own memory.

The secret is shown once, when the key is made. Copy it then.

On `/v1`, agents think as they are set to. A caller with someone waiting on a
voice line sends `"reasoning_effort": "none"` to answer without thinking.

## 6. Add people

Under **Administrator › People**:

- **Add account** creates a user. To let people sign themselves up instead,
  turn on **Allow public signup** under **Sign-in**.
- **Teams** groups people, so access can be given to a team at once.
- **Feature Access** decides who may use what. Most features are open to
  everyone until you save a list for them; once saved, a list means exactly
  who is on it. Two are closed until granted, because they reach the models
  directly: **Model API** and **Model access**. Administrators always have
  both. Grant them to the people or teams who need them, and set how much raw
  model use each person gets under **Model access limits**.

Sharing an agent, collection or tool with others is done by its owner, from
that thing's own sharing list.

## Where to go next

- **Scheduler**, in the Agents sidebar: have an agent run on a schedule, or
  watch something and wake when it changes. Each schedule shows how its last
  run went.
- **Bridges**: answer messages from other services. iMessage comes through the
  `gohort-desktop` app on a Mac; Teams bots and linked services are added under
  **Extensions › Integrations**.
- **Build**, on any agent's page: describe a tool, app or agent and Builder
  makes it.
- **Servitor**: give agents your SSH systems and git repositories to
  investigate.
- **Administrator › Overview › System Status**: whether the sandbox, the
  background work and the rest are healthy.

[docs/REFERENCE.md](REFERENCE.md) covers the whole feature surface, and
[SECURITY.md](../SECURITY.md) the trust model.
