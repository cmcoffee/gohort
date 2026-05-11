#!/usr/bin/env bash
# Install the gohort git hooks by symlinking them into .git/hooks/.
# Run from anywhere inside the repo:
#   scripts/hooks/install.sh

set -euo pipefail
repo_root="$(git rev-parse --show-toplevel)"
hooks_src="$repo_root/scripts/hooks"
hooks_dst="$repo_root/.git/hooks"

for hook in pre-commit; do
  src="$hooks_src/$hook"
  dst="$hooks_dst/$hook"
  if [ ! -f "$src" ]; then continue; fi
  chmod +x "$src"
  ln -sfn "../../scripts/hooks/$hook" "$dst"
  echo "installed: $hook"
done
