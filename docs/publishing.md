# Publishing: where a finished document goes

Status: **built**, revised 2026-10-05 (v0.7.386 to v0.7.391). A producer app
(Scribe) publishes a document through `core/docs`; destinations are
registered by `apps/publish`. This records the shape, and the corrections
made after a person's Atlassian MCP publishing target "did not work".

## Two kinds of destination

- **The deployment's** (Admin > Publishing, admin only): Confluence through a
  SecureAPI credential, a webhook, and agent destinations an admin sets up for
  everyone. One Confluence and one webhook per deployment.
- **A person's own publishing targets** (Extensions > Publishing targets):
  each a name, the integration it publishes through (one of their API
  credentials or MCP servers, or one of their agents), an instruction, and
  optional questions (a category, a space). A publish holds only that
  integration; an MCP server's tools that delete, remove or archive are left
  out.

**Users are sent to their own targets, never to the admin page.** Every
reason a destination cannot be used, the Publisher's "nowhere to publish",
and Scribe's empty dialog say "publish through a target of your own: make one
in Extensions, Publishing targets" (`setUpYourOwn`). The person reading it is
usually not an admin.

A Confluence-through-MCP option on the admin page was built and reverted
(v0.7.386, v0.7.387): the person had already set Atlassian's MCP up as their
own target, which is where their integrations belong.

## The Publisher picks the target you named

Clicking a target in Scribe opens the Publisher chat ("publish this guide to
my publishing target X"). Its `list_publish_destinations` used to show all of
a person's targets as one anonymous "Your publishing targets" entry, beside
the built-in Confluence reading "NOT available: no credential is configured".
A target for Confluence lost to the built-in, and the publish came back "not
configured" while agents used the same MCP server without trouble.

Now each target is listed by name with its own destination id
(`target:<id>`), first; available built-ins after; unusable ones last under
"Not available, do not publish to these". Scribe's button passes the id, and
the Publisher's prompt says to use the target the person named even when a
built-in has a similar name.

## A document's links to itself

Navigation is presentation, and presentation belongs to where a document is
published. A guide's table of contents and its links to its own sections use
oddjob's heading anchors, which point at nothing on a Confluence page (or
wherever an MCP or agent route converts the markdown).

`docs.AnalyzeNav` reads the navigation (headings with oddjob's anchors, each
in-page link and the heading it means, by anchor or else by its words, and
the table of contents' heading). `PublishDocument`, which every publish path
goes through, puts it on the request (`PublishRequest.Nav`), so no producer
changes. Each destination rebuilds it its own way:

- **Confluence through its API**, in code (`confluence_nav.go`): the table of
  contents becomes the Table of Contents macro, a linked heading gets an Anchor
  macro, an in-page link becomes an `ac:link` to it, and a link to no heading
  keeps its words without the dead link.
- **Anything a model publishes to** (a target through an API or MCP
  integration or an agent, an agent destination): the instruction carries
  `docs.NavInstruction`, listing each link and the heading it means, to
  rebuild them the destination's way and change no other wording. Not
  specific to Confluence, and it depends on the model following it.
- **Webhook:** the JSON carries `nav` for its receiver.

The Publisher's prompt: how a document looks at its destination is its job
(fixed by republishing with `update_existing`); what it says is the author's.
It used to send the person to the author for broken links.

## Watching a publish

A publish through an integration is a model run with tools; from outside it
was a spinner and then an outcome. `docs.PublishStep` reports a step on the
publish's context (`WithPublishSteps` puts a reporter there), so a
destination reports with one call and no signature changes:

- the integration run reports each tool call (arguments, a long value by its
  size) and the first line of what came back, failures included, and what it
  reported at the end;
- agent routes report the hand-off and the agent's answer;
- Confluence's API route reports the navigation it rebuilt and whether it
  created or updated the page.

Scribe's publish job keeps the steps (bounded at 200) and the dialog lists
them under the spinner, kept after it ends. Publish-to-target and Update
share `runPublishJob`; Update used to run on the request, a dead button that
closing the dialog could cut off.

## Open

- A person's report that Update did not fix the contents links on their
  Atlassian MCP target is undiagnosed: it ran on another machine. The steps of
  the next run should show what the MCP run actually called.
- Whether the admin Confluence and webhook destinations are retired in favour
  of targets (with a Confluence template on the target form) is undecided.
