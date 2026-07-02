#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DPM_BIN="${DPM_BIN:-$HOME/.dpm/bin/dpm}"
SDK_VERSION="${SDK_VERSION:-$("$DPM_BIN" version 2>/dev/null | awk '/\*/{print $2}')}"
COMPONENT_VERSION="${COMPONENT_VERSION:-0.1.0}"
DPM_HOME="${DPM_HOME:-$HOME/.dpm}"

if [[ ! -x "$DPM_BIN" ]]; then
  echo "error: DPM not found at $DPM_BIN" >&2
  exit 1
fi

GLOBAL_MANIFEST="$HOME/.dpm/cache/sdk/open-source/$SDK_VERSION.yaml"
if [[ ! -f "$GLOBAL_MANIFEST" ]]; then
  echo "error: SDK manifest not found: $GLOBAL_MANIFEST" >&2
  echo "run: $DPM_BIN install $SDK_VERSION" >&2
  exit 1
fi

mkdir -p "$DPM_HOME/cache/components" "$DPM_HOME/cache/sdk/open-source"

if [[ "$DPM_HOME" != "$HOME/.dpm" ]]; then
  for component in "$HOME/.dpm/cache/components/"*; do
    [[ -e "$component" ]] || continue
    ln -sfn "$component" "$DPM_HOME/cache/components/$(basename "$component")"
  done
fi

COMPONENT_DIR="$DPM_HOME/cache/components/dpm-trace/$COMPONENT_VERSION"
mkdir -p "$COMPONENT_DIR/bin"
ln -sfn "$ROOT/component.yaml" "$COMPONENT_DIR/component.yaml"
ln -sfn "$ROOT/bin/dpm-trace" "$COMPONENT_DIR/bin/dpm-trace"
ln -sfn "$ROOT/bin/dpm-debug" "$COMPONENT_DIR/bin/dpm-debug"

LOCAL_MANIFEST="$DPM_HOME/cache/sdk/open-source/$SDK_VERSION.yaml"
if grep -q '^    dpm-trace:' "$GLOBAL_MANIFEST"; then
  [[ "$LOCAL_MANIFEST" != "$GLOBAL_MANIFEST" ]] && cp "$GLOBAL_MANIFEST" "$LOCAL_MANIFEST"
else
  TMP="$(mktemp)"
  awk -v version="$COMPONENT_VERSION" '
    /^  assistant:/ && !added {
      print "    dpm-trace:"
      print "      version: " version
      added = 1
    }
    { print }
  ' "$GLOBAL_MANIFEST" > "$TMP"
  mv "$TMP" "$LOCAL_MANIFEST"
fi

echo "Installed dpm-trace component (SDK $SDK_VERSION)."
echo
echo "Run with:"
if [[ "$DPM_HOME" != "$HOME/.dpm" ]]; then
  echo "  DPM_HOME=$DPM_HOME $DPM_BIN trace --help"
else
  echo "  $DPM_BIN trace --help"
fi
