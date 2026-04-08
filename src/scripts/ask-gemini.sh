#!/usr/bin/env sh
set -eu

if [ "$#" -eq 0 ]; then
  echo "Usage: scripts/ask-gemini.sh <question or task>" >&2
  exit 1
fi

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
echo "[nana] wrapper deprecation: prefer 'nana ask gemini \"...\"'." >&2
if [ -x "$SCRIPT_DIR/../bin/nana.js" ]; then
  exec node "$SCRIPT_DIR/../bin/nana.js" ask gemini "$@"
fi
exec node "$SCRIPT_DIR/run-provider-advisor.js" gemini "$@"
