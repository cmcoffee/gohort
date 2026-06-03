APPNAME   := Gohort
VERSION   := 0.1.0

# Wails outputs to build/bin/<name>.app on macOS. Override OUTDIR to
# place artifacts elsewhere.
OUTDIR    := build/bin
APPBUNDLE := $(OUTDIR)/$(APPNAME).app
INSTALL_DIR := /Applications

# The bridge is a SEPARATE app/bundle (Gohort-Bridge.app), built from
# cmd/gohort-bridge — a menu-bar agent (LSUIElement, no dock icon), its
# own bundle id, distinct process from the viewer. BRIDGEBIN is its
# binary inside the assembled bundle.
BRIDGENAME   := Gohort-Bridge
BRIDGEBUNDLE := $(OUTDIR)/$(BRIDGENAME).app
BRIDGEBIN    := $(BRIDGEBUNDLE)/Contents/MacOS/gohort-bridge
BRIDGE_ID    := com.gohort.bridge

.PHONY: all build dev doctor universal clean install uninstall help bridge bridge-install bridge-setup install-all stop

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

# Build Gohort-Bridge.app — the menu-bar agent. Plain `go build` (cgo on
# macOS for systray); no Wails. Assembles a minimal .app bundle with an
# LSUIElement Info.plist so it has no dock icon and lives in the menu bar.
bridge:
	@mkdir -p "$(BRIDGEBUNDLE)/Contents/MacOS" "$(BRIDGEBUNDLE)/Contents/Resources"
	CGO_ENABLED=1 go build -trimpath -o "$(BRIDGEBIN)" ./cmd/gohort-bridge
	@cp build/appicon.png "$(BRIDGEBUNDLE)/Contents/Resources/appicon.png"
	@printf '%s\n' \
		'<?xml version="1.0" encoding="UTF-8"?>' \
		'<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
		'<plist version="1.0"><dict>' \
		'  <key>CFBundleName</key><string>Gohort Bridge</string>' \
		'  <key>CFBundleDisplayName</key><string>Gohort Bridge</string>' \
		'  <key>CFBundleIdentifier</key><string>$(BRIDGE_ID)</string>' \
		'  <key>CFBundleExecutable</key><string>gohort-bridge</string>' \
		'  <key>CFBundleIconFile</key><string>appicon</string>' \
		'  <key>CFBundlePackageType</key><string>APPL</string>' \
		'  <key>CFBundleShortVersionString</key><string>$(VERSION)</string>' \
		'  <key>LSUIElement</key><true/>' \
		'  <key>LSMinimumSystemVersion</key><string>10.15</string>' \
		'</dict></plist>' > "$(BRIDGEBUNDLE)/Contents/Info.plist"
	@echo ""
	@echo "Built $(BRIDGEBUNDLE) (menu-bar agent, no dock icon)"
	@echo "Configure it from Gohort (Connect screen) or: make bridge-setup"

# Optional CLI config for the bridge (normally configured in the viewer).
bridge-setup: bridge
	"$(BRIDGEBIN)" --setup

# Back-compat alias — `install` now stops the running pair and installs
# BOTH apps (viewer + bridge) on its own.
install-all: install

# Register the (already-installed) bridge to run at login.
bridge-install: bridge
	"$(BRIDGEBIN)" --install

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
# Stop the running viewer + bridge so a reinstall replaces clean bundles
# and the fresh build takes over (new code, new tool catalog). The bridge
# runs under launchd with KeepAlive — a plain kill is relaunched — so the
# agent is booted out; `install` re-bootstraps it. All best-effort: the
# leading "-" ignores errors, so it works whether or not they're running.
stop:
	@echo "Stopping running $(APPNAME) viewer + bridge…"
	-@osascript -e 'quit app "$(APPNAME)"' >/dev/null 2>&1
	-@killall "$(APPNAME)" >/dev/null 2>&1
	-@launchctl bootout gui/$$(id -u)/$(BRIDGE_ID) >/dev/null 2>&1
	-@killall gohort-bridge >/dev/null 2>&1
	@sleep 1

# One-command rebuild + reinstall of BOTH apps — the viewer (Gohort.app)
# and the bridge (Gohort-Bridge.app). Stops the running pair first, then
# replaces both bundles, re-registers the bridge so launchd launches the
# fresh build, and reopens the viewer. After `make install` everything is
# back up on the new code — nothing else to type.
install: stop build bridge
	@echo "Installing $(APPNAME).app + $(BRIDGENAME).app to $(INSTALL_DIR)/"
	@if [ -d "$(INSTALL_DIR)/$(APPNAME).app" ]; then rm -rf "$(INSTALL_DIR)/$(APPNAME).app"; fi
	cp -R "$(APPBUNDLE)" "$(INSTALL_DIR)/"
	@if [ -d "$(INSTALL_DIR)/$(BRIDGENAME).app" ]; then rm -rf "$(INSTALL_DIR)/$(BRIDGENAME).app"; fi
	cp -R "$(BRIDGEBUNDLE)" "$(INSTALL_DIR)/"
	"$(INSTALL_DIR)/$(BRIDGENAME).app/Contents/MacOS/gohort-bridge" --install
	@open "$(INSTALL_DIR)/$(APPNAME).app"
	@echo "Installed + relaunched the viewer; the bridge is running under launchd."
	@echo "If you haven't yet, grant Full Disk Access to $(BRIDGENAME).app for iMessage."

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
	@echo "  make                 Build viewer .app for the current architecture (default)"
	@echo "  make universal       Build universal .app (Intel + Apple Silicon)"
	@echo "  make bridge          Build Gohort-Bridge.app (menu-bar agent, no dock icon)"
	@echo "  make install-all     Install BOTH apps + register the bridge at login"
	@echo "  make bridge-install  Register the bridge to run at login"
	@echo "  make bridge-setup    Configure the bridge from the CLI (server URL, API key)"
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
