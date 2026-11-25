// Package apppage is what an app's page can call: window.app, the helper
// apps/customapps puts at the top of every frame a page renders in. The
// reference is here, apart from the helper, so a package the app host's
// tests import (orchestrate, for Builder's help) can name it too.
package apppage

// API is what window.app offers a page, one call a line.
const API = `app.data(name, params)        GET a data source, its JSON
app.action(name, body, opts)  POST an action, {message, saved, records, result}.
                              A slow one keeps running on the server; the
                              promise resolves when it ends, and
                              opts.onProgress(line) hears its latest output
                              meanwhile (show it: it is how a person knows
                              a five-minute download is alive)
app.cancelAction(name)        stop this person's running action of that name
app.state.get(key)            what the app saved for this person under key
                              (null when nothing), on every device. Every
                              open of an app starts fresh, reloads included:
                              localStorage lasts one visit. To let someone
                              carry on where they left off, save it here
                              and offer it on load ("Continue where you
                              left off?"); the app's data goes in records
app.state.set(key, value)     keep value (any JSON) for this person
app.state.remove(key)         forget one
app.state.clear()             forget everything the app saved for them
app.work()                    this person's work in the app, newest first:
                              each {job, kind: "action"|"ask", name,
                              running, progress, done, outcome, started}.
                              Work keeps going when the page is closed; call
                              this as the page loads and follow what runs,
                              or show how it ended (kept 30 minutes)
app.follow(job, opts)         a job from work(), resolving as action or ask
                              would when it ends; opts.onProgress as for
                              action. The same action called again with the
                              same body while it runs joins that run
app.records.list()            this person's records, oldest first
app.records.save(record)      create, or update by the record key
app.records.remove(id)        delete the record whose key (record_key) is id
app.shared(name)              a shared collection's records
app.ask(prompt, {json})       a question to the app's agent, its text. A
                              long answer keeps coming when the page is
                              closed: work() finds it
app.asset(name)               the URL of one of the app's assets
app.files.list(id)            the files a record keeps (a song's takes),
                              each {name, title, type, size, added, url}
app.files.url(id, name)       the URL that plays or opens one
app.files.add(id, file, title) keep a file the person picked (an
                              <input type=file>'s file) on the record:
                              a photo, a recording, a PDF; 64 MiB each.
                              Resolves to {name, title, type, size, url}
app.files.remove(id, name)    take one off the record and the disk
                              (an action script keeps one by naming it
                              in its reply: files:[{record, file, title}])
app.coauthor(textarea, opts)  a document the person writes with the app's
                              agent (coauthor below)
app.propose(doc, prompt, o)   the agent asked for a change to doc,
                              {reply, edit?: {find, replace, summary}};
                              o.selection {text, start}, o.history
app.onChange(fn)              fn() whenever records or shared data change
                              (anyone's), until the returned stop() is called
app.confirm(msg)              a question, true or false
app.alert(msg)                a notice, resolved when read
app.prompt(msg, default)      a line of text, or null`

// Work is how a page treats work that outlives it, said once for everyone
// who writes a page: Builder, the folder guide, Anvil.
const Work = "WORK THAT OUTLIVES THE PAGE: an action or an ask keeps running on the server when the person closes or leaves the page, and they are told how it went if nobody was watching. " +
	"So call app.work() as the page loads, and app.follow(job) for what is still running, or show how it ended. " +
	"Do work of several steps (fetch, then summarize, then save) in ONE action script, which can ask the agent (gohort.ask) and report progress as it goes, not as calls chained in the page: those stop the moment the page closes, and the steps after it never run."
