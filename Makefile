APPNAME   := gohort
VERSION   := $(shell cat version.txt)

# Default output directory
OUTDIR    := build

# Go build flags
GOFLAGS   := -trimpath
LDFLAGS   := -s -w

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

# What actually goes out. GOWORK=off is the point of it: a workspace build
# links whatever is in the local checkouts of snugforge, fpdf and goexif, so
# the binary contains code that exists on one machine and the notices say so
# in as many words ("LOCAL WORKING COPY"). A release resolves every module to
# its published version instead — reproducible, and attributable to something
# a reader can go and look at.
release:
	@mkdir -p $(OUTDIR)
	GOWORK=off go run ./scripts/licensenotice -out $(OUTDIR)/THIRD_PARTY_NOTICES .
	@cp LICENSE NOTICE $(OUTDIR)/
	GOWORK=off go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTDIR)/$(APPNAME) .
	@echo "Released $(OUTDIR)/$(APPNAME) ($(VERSION)) + LICENSE, NOTICE, THIRD_PARTY_NOTICES"

clean:
	rm -rf $(OUTDIR)
