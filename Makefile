APPNAME   := gohort
VERSION   := $(shell cat version.txt)

# Default output directory
OUTDIR    := build

# Release artifacts: one archive per platform, plus SHA256SUMS
DISTDIR   := $(OUTDIR)/dist

# Where `release` exports its source to before building it. Not the working
# tree: see the comment on the release target.
SRCDIR    := $(OUTDIR)/.src

# What `release` builds. A commit, a tag, or any tree-ish — `make release
# REF=v0.6.344` cuts the artifacts for a tag without checking it out.
REF       ?= HEAD

# Go build flags
GOFLAGS   := -trimpath
LDFLAGS   := -s -w

# Nothing in this tree needs cgo, and leaving it enabled links the host's libc
# into the artifact: a binary built on one distro then carries that glibc's
# floor onto every machine that downloads it, and "one static binary" stops
# being true of the file people actually get. Off everywhere, so a dev build
# and a release build are the same kind of object.
export CGO_ENABLED := 0

# What a release covers. Every one of these compiles from this tree today; a
# platform that stops compiling belongs off this list rather than in a release
# that half-works.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build clean linux notices release

# Default: build for the current platform
all: build

build: notices
	@mkdir -p $(OUTDIR)
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTDIR)/$(APPNAME) .
	@echo "Built $(OUTDIR)/$(APPNAME) ($(VERSION))"

linux: notices
	@mkdir -p $(OUTDIR)
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTDIR)/$(APPNAME)_linux_amd64 .
	@echo "Built $(OUTDIR)/$(APPNAME)_linux_amd64"

# What a downloaded binary has to carry. A compiled gohort contains the object
# code of ~40 modules, each asking for its notice to travel with it, and the
# source tree cannot answer that question — the dependencies are not in it.
# Generated from what is ACTUALLY LINKED, and it FAILS the build if a module's
# license cannot be found rather than shipping a list that looks complete.
notices:
	@mkdir -p $(OUTDIR)
	@go run ./scripts/licensenotice -out $(OUTDIR)/THIRD_PARTY_NOTICES .
	@cp LICENSE NOTICE $(OUTDIR)/

# What actually goes out: one archive per platform, each carrying the binary
# and the three legal files, and a SHA256SUMS over the archives.
#
# It builds an EXPORT OF A COMMIT, not the working tree, and there are two reasons
# rather than one. A release is a commit — an artifact built from a directory
# is attributable to nothing anybody else can check out. And this checkout has
# a private half: `private.go` and the `private/` symlink are gitignored, so
# they are invisible to `git archive` and visible to `go build`. Building the
# working tree would quietly compile the private apps into a public download.
#
# GOWORK=off is the other half of the same idea. A workspace build links
# whatever is in the local checkouts of snugforge, fpdf and goexif, so the
# binary contains code that exists on one machine and the notices say so in as
# many words ("LOCAL WORKING COPY"). A release resolves every module to its
# published version instead — reproducible, and attributable to something a
# reader can go and look at.
#
# The notices are regenerated per platform for the same reason they are
# generated at all: `go list -deps` answers for one GOOS/GOARCH, and the module
# set is not identical across them (Windows links two modules the others do
# not). One notices file copied five ways would be a guess about four of them.
# The generator is compiled once for the HOST and then run with the target's
# GOOS/GOARCH in its environment — `go run` under a cross GOOS builds a tool
# this machine cannot execute.
release:
	@if [ -z "$(ALLOW_DIRTY)" ] && [ -n "$$(git status --porcelain)" ]; then \
	  echo "release: the working tree has uncommitted changes."; \
	  echo "         A release is built from $(REF), so those changes would not be in it."; \
	  echo "         Commit them, or re-run with ALLOW_DIRTY=1 to release HEAD as it stands."; \
	  exit 1; \
	fi
	@rm -rf $(DISTDIR) $(SRCDIR)
	@mkdir -p $(DISTDIR) $(SRCDIR)
	@git archive $(REF) | tar -x -C $(SRCDIR)
	@if [ -e $(SRCDIR)/private.go ] || [ -e $(SRCDIR)/private ]; then \
	  echo "release: the private tree is present in the export — refusing to ship it."; \
	  exit 1; \
	fi
	@(cd $(SRCDIR) && GOWORK=off go build -o $(CURDIR)/$(DISTDIR)/.licensenotice ./scripts/licensenotice)
	@for p in $(PLATFORMS); do \
	  os=$${p%%/*}; arch=$${p##*/}; \
	  name=$(APPNAME)_$(VERSION)_$${os}_$${arch}; \
	  stage=$(CURDIR)/$(DISTDIR)/$$name; \
	  bin=$(APPNAME); \
	  if [ "$$os" = "windows" ]; then bin=$(APPNAME).exe; fi; \
	  echo "  $$os/$$arch"; \
	  mkdir -p $$stage; \
	  (cd $(SRCDIR) && GOWORK=off GOOS=$$os GOARCH=$$arch $(CURDIR)/$(DISTDIR)/.licensenotice -out $$stage/THIRD_PARTY_NOTICES .) || exit 1; \
	  (cd $(SRCDIR) && GOWORK=off GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $$stage/$$bin .) || exit 1; \
	  cp $(SRCDIR)/LICENSE $(SRCDIR)/NOTICE $$stage/ || exit 1; \
	  if [ "$$os" = "windows" ] && command -v zip >/dev/null 2>&1; then \
	    (cd $(DISTDIR) && zip -qr $$name.zip $$name) || exit 1; \
	  else \
	    tar -czf $(DISTDIR)/$$name.tar.gz -C $(DISTDIR) $$name || exit 1; \
	  fi; \
	  rm -rf $$stage; \
	done
	@rm -rf $(SRCDIR) $(DISTDIR)/.licensenotice
	@cd $(DISTDIR) && sha256sum $(APPNAME)_$(VERSION)_* > SHA256SUMS
	@echo
	@echo "Release $(VERSION) from $$(git rev-parse --short $(REF)^{}), in $(DISTDIR):"
	@cd $(DISTDIR) && ls -1 $(APPNAME)_$(VERSION)_* SHA256SUMS | sed 's/^/  /'

# Removes what the build put in $(OUTDIR), and only that. `rm -rf build` would
# be shorter and would also take out the data/, logs/ and gohort.ini of anyone
# who runs the binary where it was built — which is the obvious thing to do
# with a self-contained binary, and a database is not a build artifact.
clean:
	rm -rf $(DISTDIR) $(SRCDIR)
	rm -f $(OUTDIR)/$(APPNAME) $(OUTDIR)/$(APPNAME)_* $(OUTDIR)/$(APPNAME).exe
	rm -f $(OUTDIR)/THIRD_PARTY_NOTICES $(OUTDIR)/LICENSE $(OUTDIR)/NOTICE
	@rmdir $(OUTDIR) 2>/dev/null || true
