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
app.records.list()            this person's records, oldest first
app.records.save(record)      create, or update by the record key
app.records.remove(id)        delete one
app.shared(name)              a shared collection's records
app.ask(prompt, {json})       a question to the app's agent, its text
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
