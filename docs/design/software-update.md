# Software update from the admin page

Status: built (core/selfupdate, apps/admin/page_update.go, scripts/publish,
`make publish` and `make release-key`). No key made and nothing published yet.

An administrator updates gohort from the admin page: a Software Update
section under System checks for a newer version when asked, and one button
downloads it, checks it, puts it in place and restarts onto it. Nothing
checks on its own and nothing tells anyone to update. The model is kitebroker's updater (a `version.txt` and a binary per
platform on a web folder), with two things kitebroker does not have: every
release is signed, and a publish never shows a version before its binaries
are all there.

## Where releases live

The dist site is a web folder (for the owner: the NAS share mounted at
`/mnt/distro`, served by nginx as `https://dist.snuglab.com`). gohort gets a
folder of its own beside kitebroker's:

```
gohort/
  version.txt                 newest version, for people and scripts
  manifest.json               newest release: version, and each binary's hash
  manifest.json.sig           the signature over manifest.json
  0.9.380/
    linux-amd64/gohort
    linux-arm64/gohort
    darwin-amd64/gohort
    darwin-arm64/gohort
    windows-amd64/gohort.exe  each platform folder also holds its THIRD_PARTY_NOTICES
    LICENSE, NOTICE
  0.9.377/ ...                the previous releases, the last three kept
```

Each release has its own folder, unlike kitebroker's fixed
`<os>-<arch>/kitebroker`, so publishing never writes over a file a server
may be downloading: the new folder is written whole, and only then does the
manifest point at it.

`manifest.json`:

```json
{
  "product": "gohort",
  "version": "0.9.380",
  "commit": "c4f38ce2",
  "published": "2026-11-13T18:40:00Z",
  "files": {
    "linux-amd64": {"path": "0.9.380/linux-amd64/gohort", "size": 61234567, "sha256": "..."}
  }
}
```

`product` is signed with the rest, so a manifest for another product signed
with the same key is not taken for gohort's.

Only the public tree is published, the export `make release` already makes
(it refuses when `private/` is in it). The private apps are never on the
dist site.

## Signing

Ed25519, from Go's standard library: no new dependency.

- **The key pair.** `make release-key` makes one. The private key stays on
  the build machine, owner-only (`~/.config/gohort/release.key`; `publish` refuses
  a key file others can read), and never on the dist share: the share is writable
  by any machine that can mount it, so a key kept there signs for anyone. The
  public key (32 bytes) is added to `core/selfupdate/release_keys.txt`,
  which is built into gohort; commit it. `publish` refuses to sign with a
  key that file does not have, since no server could check it.
- **Two trusted keys.** gohort trusts a main key and a spare kept offline,
  so a lost main key is replaced with a release signed by the spare, not by
  updating every server by hand. The `.sig` file names the key it was made
  with: `<key id> <base64 signature>`.
- **What it checks.** The signature is over the manifest's exact bytes.
  gohort checks it before reading anything else in the manifest, then checks
  each download against the manifest's size and SHA-256.
- **A different key.** `[update] key =` in gohort.ini replaces the built-in
  keys, for someone who builds and publishes their own. That is no weaker:
  the threat is the channel the update comes over, and anyone who can edit
  the ini already has the machine.

What it stops: a binary changed on the dist share or swapped on the way, and
an old release served again as new (the version is signed and gohort never
goes backwards). What it does not stop: a stolen private key, or bad code the
owner signs.

## Publishing: `make publish`

1. The same export, vet and build as `make release`, for the same five
   platforms, building bare binaries instead of archives.
2. Copy each binary into `<dist>/gohort/<version>/<os>-<arch>/` under a
   temporary name, then rename it into place (a rename inside one filesystem
   is atomic; a `mv` from local disk to NFS is a copy).
3. Write `manifest.json`, sign it, and write both by temporary name and
   rename, the signature first.
4. Write `version.txt` last.
5. Remove release folders past the newest three.

`DIST=/mnt/distro/gohort` by default, overridable. It refuses a version that
is already published, and a version not newer than the published one.

## On the server

### Settings

```ini
[update]
url =                ; empty: the dist site built in (https://dist.snuglab.com/gohort)
key =                ; empty: the keys built in
```

Release builds have the dist site built in, so updating needs nothing set
up. `url` points at another dist site. A check sends nothing but the request
itself, with `gohort/<version>` as its User-Agent, as kitebroker does.

### The Software Update section

A section on the Overview tab (the System part of the admin page), after
System Status and the Activity Log. It
says what is running and where updates come from, and has one button:

- **Running:** `0.9.377` (from dist.snuglab.com/gohort) `[Check for update]`

Pressing it fetches `manifest.json` and its signature (a few hundred bytes)
and checks the signature. The section then shows one of:

- `0.9.380 is available` `[Update to 0.9.380]`
- `Up to date`
- why it cannot tell (`the signature does not match: not offering it`,
  `dist.snuglab.com did not answer`).

The answer is kept in memory only, so the section shows the last check
(`checked 4 minutes ago`) until the server restarts. Nothing checks on a
timer, and no other page mentions updates.

### Updating

Admin only, like the rest of the admin page, with a plain confirm ("Update
to 0.9.380? gohort restarts, down for a few seconds"). No second password:
other admin actions that reach as far, adding an administrator or editing a
credential, do not ask for one, and an update can only install a release
signed with the owner's key, so a session left open can move a server to a
newer official release and nothing else.

It then runs as a maintenance pass with its progress shown
(`ReportMaintenanceProgress`), so the page shows it alive and a page opened
mid-run rejoins it:

1. Fetch the manifest and its signature again and check the signature.
2. Refuse unless the version is newer than the one running.
3. Download this platform's binary to `<binary dir>/.gohort-<version>.download`
   (`12.4 of 61.2 MB - 9s`), and check its size and SHA-256.
4. Run it once with `--version`, with a timeout: it must start and say
   the version the manifest says. This catches a binary for the wrong
   platform or one that does not start, before anything is replaced.
5. Rename the running binary to `gohort.prev`, and the new one to the
   binary's name.
6. Log it in the Activity Log (kind `update`): who, from what version to
   what.
7. Restart onto it (below).

Any failure before step 5 leaves everything as it was and says which step
failed. The download is removed.

### Restarting

On Linux and macOS: shut down the way a SIGINT does (cancel the app context,
let in-flight work wind down, close the databases), then `syscall.Exec` the
new binary with the same arguments and environment. The process ID, the
terminal and the parent stay the same, so this works in tmux, under systemd
and under nothing. The listener closes, so the site is down for the few
seconds startup takes. The section's status refreshes every few seconds and
shows the new version once the server answers again.

The exec is registered first at startup, so it runs last in shutdown, after
every database has closed.

On Windows, which has no exec, the new binary is installed and gohort says to
restart it: a new process started before the old one exits cannot take its
port.

### Rolling back

`gohort.prev` is the version before. The section shows
`[Roll back to 0.9.377]` while it is there; it is the same swap and restart,
without the download, with the same confirm. By hand it is
`mv gohort.prev gohort`.

A rollback goes back to the old program, not the old data: whatever the new
version wrote stays.

### When a server cannot update itself

The section shows the reason in place of the buttons:

- the binary's folder is not writable by the user gohort runs as (a package
  install, a container image): "update this install the way it was
  installed";
- **the running binary has the private apps in it**: updating would replace
  it with the public build and drop them. Such a build is known by what it
  registers (an app or tool from a `/gohort/private/` package; `private.go`
  is not in the public repository, so it cannot mark the build itself), and
  it is never offered the public update.

The owner's own server is the second case: it keeps updating from source
(`update_gohort.sh`).

## Code

A subpackage, `core/selfupdate`, that speaks no core type: manifest,
signature check, download with progress, the swap and the exec. It is the
part kitebroker could use too, and it can move to snugforge once both use
it. The admin page's route, row and Activity Log entry are in apps/admin. No
new top-level export in core.

## Kitebroker

The same signing fits kitebroker, which is exposed more (clients in the
field update from the public dist site on every run). Its own key, not
gohort's, so one product's key cannot sign the other's. Moving it over:

1. A kitebroker release whose updater checks a signature, published the old
   way, unsigned: clients already out there take it as they take any update.
2. From then on its releases are signed and unsigned ones are refused.

Clients that skip that release keep taking unsigned updates until they run
it. Its `build_kitebroker.sh` also writes `version.txt` before building,
which has the same window as above; the fix is the same order.

## Not in this

- Checking on a timer, or telling anyone a newer version is out: an
  administrator checks from the section when they want to.
- Updating automatically, with no one pressing the button.
- Starting the previous version again by itself when the new one fails after
  the restart. The `--version` run catches a binary that does not start at
  all; the rest is the rollback button, or `mv` when the page will not come
  up.
- A private channel for builds with the private apps.

## Open questions

1. **How many past releases to keep** on the share (three, above).
