APPNAME  := phantom-bridge
OUTDIR   := build
GOFLAGS  := -trimpath
LDFLAGS  := -s -w

.PHONY: all build install clean test deps

all: build

deps:
	go mod tidy

build: deps
	@mkdir -p $(OUTDIR)
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OUTDIR)/$(APPNAME) .
	@echo "Built $(OUTDIR)/$(APPNAME)"

# Install to ~/bin and restart the launchd service if it is loaded
install: build
	@mkdir -p ~/bin
	cp $(OUTDIR)/$(APPNAME) ~/bin/$(APPNAME)
	@echo "Installed ~/bin/$(APPNAME)"
	@PLIST=$$HOME/Library/LaunchAgents/com.gohort.phantom-bridge.plist; \
	UID_DOMAIN=gui/$$(id -u); \
	SVC=$$UID_DOMAIN/com.gohort.phantom-bridge; \
	if launchctl list com.gohort.phantom-bridge > /dev/null 2>&1; then \
		launchctl bootout $$UID_DOMAIN $$PLIST 2>/dev/null || true; \
		launchctl enable $$SVC 2>/dev/null || true; \
		launchctl bootstrap $$UID_DOMAIN $$PLIST; \
		echo "Restarted com.gohort.phantom-bridge"; \
	else \
		echo ""; \
		echo "Service not yet registered. First-time setup:"; \
		echo "  phantom-bridge --setup     # configure server URL and API key"; \
		echo "  phantom-bridge --install   # register as a login item (launchd)"; \
		echo ""; \
	fi

# Run the DB sanity check (no server calls)
test: build
	$(OUTDIR)/$(APPNAME) --test

# Run setup wizard
setup: build
	$(OUTDIR)/$(APPNAME) --setup

clean:
	rm -rf $(OUTDIR)
