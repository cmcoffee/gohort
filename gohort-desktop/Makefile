APPNAME   := Gohort
VERSION   := 0.1.0

# Wails outputs to build/bin/<name>.app on macOS. Override OUTDIR to
# place artifacts elsewhere.
OUTDIR    := build/bin
APPBUNDLE := $(OUTDIR)/$(APPNAME).app
INSTALL_DIR := /Applications

.PHONY: all build dev doctor universal clean install uninstall help

# Default target: native architecture build. For a distribution build
# that runs on both Intel and Apple Silicon Macs in one binary, use
# `make universal`.
all: build

# Build the .app for the current architecture. Wails handles the
# bundle layout (Info.plist, Resources/, MacOS/<binary>) and embeds
# the frontend assets from frontend/ into the binary.
build:
	@command -v wails >/dev/null 2>&1 || { \
		echo "Error: wails CLI not found."; \
		echo "Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"; \
		exit 1; \
	}
	wails build -clean
	@echo ""
	@echo "Built $(APPBUNDLE) ($(VERSION))"
	@echo "Drag to /Applications/ or run: make install"

# Debug build: same .app but built with -debug so WKWebView's
# inspector (right-click → Inspect Element) is enabled. Use this
# when troubleshooting in-page JS. The production `make build`
# disables the inspector for end-user installs.
debug:
	@command -v wails >/dev/null 2>&1 || { \
		echo "Error: wails CLI not found."; \
		echo "Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"; \
		exit 1; \
	}
	wails build -debug -clean
	@echo ""
	@echo "Built $(APPBUNDLE) (debug, $(VERSION))"
	@echo "Right-click in the window → Inspect Element to open devtools."

# Convenience: debug + install in one step.
debug-install: debug
	@echo "Installing $(APPNAME).app (debug) to $(INSTALL_DIR)/"
	@if [ -d "$(INSTALL_DIR)/$(APPNAME).app" ]; then \
		rm -rf "$(INSTALL_DIR)/$(APPNAME).app"; \
	fi
	cp -R "$(APPBUNDLE)" "$(INSTALL_DIR)/"
	@echo "Installed (debug). Right-click in window → Inspect Element."

# Universal build: single .app that runs on both arm64 (Apple Silicon)
# and amd64 (Intel) Macs. Slightly larger binary; right for sharing
# the .app to anyone who isn't sure of their Mac's architecture.
universal:
	@command -v wails >/dev/null 2>&1 || { \
		echo "Error: wails CLI not found."; \
		echo "Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"; \
		exit 1; \
	}
	wails build -clean -platform darwin/universal
	@echo ""
	@echo "Built universal $(APPBUNDLE) ($(VERSION))"

# Dev mode: opens the window with live-reload on Go file edits. The
# operator runs `gohort serve 127.0.0.1:8088` in another terminal;
# this just opens the window pointed at that. Edit app.go / proxy.go
# and Wails recompiles + reloads automatically.
dev:
	@command -v wails >/dev/null 2>&1 || { \
		echo "Error: wails CLI not found."; \
		echo "Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"; \
		exit 1; \
	}
	@echo "Reminder: run 'gohort serve 127.0.0.1:8088' in another terminal first."
	wails dev

# Check that the toolchain is ready (Wails CLI, Xcode CLI tools,
# system webview). Run this first on a fresh machine.
doctor:
	wails doctor

# Build + copy the .app into /Applications in one shot. Always
# rebuilds first so a stale binary can't ship — earlier this was
# `install: $(APPBUNDLE)` which only rebuilt when the bundle was
# missing, so post-edit installs silently shipped the previous build.
install: build
	@echo "Installing $(APPNAME).app to $(INSTALL_DIR)/"
	@if [ -d "$(INSTALL_DIR)/$(APPNAME).app" ]; then \
		echo "Removing existing $(INSTALL_DIR)/$(APPNAME).app"; \
		rm -rf "$(INSTALL_DIR)/$(APPNAME).app"; \
	fi
	cp -R "$(APPBUNDLE)" "$(INSTALL_DIR)/"
	@echo "Installed. Launch from Applications, Spotlight (\"$(APPNAME)\"), or:"
	@echo "  open $(INSTALL_DIR)/$(APPNAME).app"

# Remove the installed .app from /Applications.
uninstall:
	@if [ -d "$(INSTALL_DIR)/$(APPNAME).app" ]; then \
		rm -rf "$(INSTALL_DIR)/$(APPNAME).app"; \
		echo "Removed $(INSTALL_DIR)/$(APPNAME).app"; \
	else \
		echo "Not installed at $(INSTALL_DIR)/$(APPNAME).app"; \
	fi

# Remove all build artifacts.
clean:
	rm -rf build/

help:
	@echo "gohort-desktop build targets:"
	@echo ""
	@echo "  make                 Build .app for the current architecture (default)"
	@echo "  make universal       Build universal .app (Intel + Apple Silicon)"
	@echo "  make debug           Build with WKWebView inspector enabled"
	@echo "  make debug-install   Build (debug) and install to /Applications/"
	@echo "  make dev             Run dev mode with live reload"
	@echo "  make doctor          Check Wails toolchain"
	@echo "  make install         Copy built .app to /Applications/"
	@echo "  make uninstall       Remove installed .app from /Applications/"
	@echo "  make clean           Remove build artifacts"
	@echo ""
	@echo "First-time setup:"
	@echo "  go install github.com/wailsapp/wails/v2/cmd/wails@latest"
	@echo "  xcode-select --install"
	@echo "  make doctor"
