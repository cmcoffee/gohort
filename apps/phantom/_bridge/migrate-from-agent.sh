#!/bin/bash
# migrate-from-agent.sh — migrates an existing phantom-agent install to phantom-bridge.
#
# Run on the Mac that hosts the bridge. Idempotent: safe to re-run if a step fails.
#
#   bash migrate-from-agent.sh
#
# What it does:
#   1. Stops the old launchd service (com.gohort.phantom-agent).
#   2. Removes the old plist (~/Library/LaunchAgents/com.gohort.phantom-agent.plist).
#   3. Renames the config file (~/.phantom-agent.cfg → ~/.phantom-bridge.cfg) so
#      your server URL + API key carry over without a re-setup.
#   4. Renames the log file (~/phantom-agent.log → ~/phantom-bridge.log) for
#      continuity (optional; safe to delete instead).
#   5. Removes the old binary (~/bin/phantom-agent).
#   6. Builds + installs the new binary via `make install`, which registers
#      the new launchd service (com.gohort.phantom-bridge).
#
# Aborts at the first error. Re-run after fixing.

set -euo pipefail

cd "$(dirname "$0")"

OLD_LABEL="com.gohort.phantom-agent"
NEW_LABEL="com.gohort.phantom-bridge"
OLD_BIN="$HOME/bin/phantom-agent"
NEW_BIN="$HOME/bin/phantom-bridge"
OLD_PLIST="$HOME/Library/LaunchAgents/${OLD_LABEL}.plist"
NEW_PLIST="$HOME/Library/LaunchAgents/${NEW_LABEL}.plist"
OLD_CFG="$HOME/.phantom-agent.cfg"
NEW_CFG="$HOME/.phantom-bridge.cfg"
OLD_LOG="$HOME/phantom-agent.log"
NEW_LOG="$HOME/phantom-bridge.log"
UID_DOMAIN="gui/$(id -u)"

say() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }

# 1. Stop the old service if it's running.
if launchctl list "$OLD_LABEL" >/dev/null 2>&1; then
	say "stopping old service: $OLD_LABEL"
	launchctl bootout "$UID_DOMAIN" "$OLD_PLIST" 2>/dev/null || true
else
	say "old service not running (skip stop)"
fi

# 2. Remove the old plist.
if [[ -f "$OLD_PLIST" ]]; then
	say "removing old plist: $OLD_PLIST"
	rm -f "$OLD_PLIST"
else
	say "old plist already gone (skip)"
fi

# 3. Rename config — preserves server URL + API key.
if [[ -f "$OLD_CFG" && ! -f "$NEW_CFG" ]]; then
	say "moving config: $OLD_CFG → $NEW_CFG"
	mv "$OLD_CFG" "$NEW_CFG"
elif [[ -f "$OLD_CFG" && -f "$NEW_CFG" ]]; then
	warn "both old and new config exist; leaving $OLD_CFG in place. Review and merge by hand if needed."
else
	say "no old config to migrate (skip)"
fi

# 4. Rename log (optional — keeps history continuous).
if [[ -f "$OLD_LOG" && ! -f "$NEW_LOG" ]]; then
	say "moving log: $OLD_LOG → $NEW_LOG"
	mv "$OLD_LOG" "$NEW_LOG"
fi

# 5. Remove old binary.
if [[ -f "$OLD_BIN" ]]; then
	say "removing old binary: $OLD_BIN"
	rm -f "$OLD_BIN"
fi

# 6. Build and install the new binary, which also bootstraps the new launchd service.
say "building and installing phantom-bridge"
make install

# 7. Verify the new service is up.
if launchctl list "$NEW_LABEL" >/dev/null 2>&1; then
	say "phantom-bridge is loaded — migration complete"
	echo
	echo "Verify:"
	echo "  tail -f $NEW_LOG"
	echo "  launchctl list | grep phantom"
else
	warn "phantom-bridge not running — check 'make install' output above"
	exit 1
fi
