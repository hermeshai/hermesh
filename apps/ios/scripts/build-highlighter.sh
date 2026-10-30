#!/usr/bin/env bash
# Build the code highlighter (scripts/highlighter, the desktop app's Shiki)
#   apps/ios/scripts/build-highlighter.sh   →   apps/ios/build/Highlighter
# and, run by the Xcode build, copy it into the app as Highlighter/. It
# builds again only when its sources or the lockfile are newer.
set -euo pipefail
cd "$(dirname "$0")/../../.."
OUT=apps/ios/build/Highlighter
export PATH="$PATH:/opt/homebrew/bin:/usr/local/bin"
if [[ ! -f "$OUT/highlighter.js" || -n "${FORCE:-}" ]] || [[ -n "$(find apps/ios/scripts/highlighter pnpm-lock.yaml -newer "$OUT/highlighter.js" -type f | head -1)" ]]; then
  [[ -d apps/desktop/node_modules/shiki ]] || pnpm install --frozen-lockfile
  node apps/ios/scripts/highlighter/build.mjs "$OUT"
else
  echo "› $OUT is up to date"
fi
if [[ -n "${TARGET_BUILD_DIR:-}" ]]; then
  DEST="$TARGET_BUILD_DIR/$UNLOCALIZED_RESOURCES_FOLDER_PATH/Highlighter"
  mkdir -p "$DEST"
  rsync -a --delete "$OUT/" "$DEST/"
fi
