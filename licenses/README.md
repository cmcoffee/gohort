# Vendored license texts

A module that ships no license file cannot have its terms reproduced in a
binary's `THIRD_PARTY_NOTICES`, and the generator refuses to guess: it fails the
release build with the module's name (`scripts/licensenotice`). This directory
is where that gets answered once, in the open, instead of a person deciding it
again at every release.

Layout is the module's import path, then the license file as the generator would
have found it in the module itself, plus a `SOURCE` file naming where the text
came from:

```
licenses/<module path>/LICENSE
licenses/<module path>/SOURCE
```

Entries here are marked in the generated notices as supplied by this project,
with the `SOURCE` line printed above the terms, so nobody reads them believing
the module shipped them.

**Only vendor a license you can point at.** Upstream's repository, the module's
own README where it states its terms, an SPDX identifier the author published.
Reconstructing a license from a name and a guessed copyright line is worse than
the build failure it silences.

## Current entries

| Module | Why | Source |
|---|---|---|
| `github.com/mattn/go-localereader` | Reached only on Windows, through `charmbracelet/bubbletea`. The `v0.0.1` module zip carries no license file; its README states MIT, and upstream's repository has since added the full text. | [upstream `LICENSE`](https://github.com/mattn/go-localereader/blob/master/LICENSE) |
