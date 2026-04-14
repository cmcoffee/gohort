APPNAME   := gohort
VERSION   := $(shell cat version.txt)

# Default output directory
OUTDIR    := build

# Go build flags
GOFLAGS   := -trimpath
LDFLAGS   := -s -w

.PHONY: all build clean linux

# Default: build for the current platform
all: build

build:
	@mkdir -p $(OUTDIR)
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTDIR)/$(APPNAME) .
	@echo "Built $(OUTDIR)/$(APPNAME) ($(VERSION))"

linux:
	@mkdir -p $(OUTDIR)
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTDIR)/$(APPNAME)_linux_amd64 .
	@echo "Built $(OUTDIR)/$(APPNAME)_linux_amd64"

clean:
	rm -rf $(OUTDIR)
