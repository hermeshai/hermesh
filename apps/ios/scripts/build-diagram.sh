#!/usr/bin/env bash
# Bundle the diagram renderer (scripts/diagram and the desktop app's Mermaid)
#   apps/ios/scripts/build-diagram.sh   →   apps/ios/build/Diagram
# and, run by the Xcode build, copy it into the app as Diagram/. It
# bundles again only when its sources or the lockfile are newer.
set -euo pipefail
cd "$(dirname "$0")/../../.."
OUT=apps/ios/build/Diagram
export PATH="$PATH:/opt/homebrew/bin:/usr/local/bin"
if [[ ! -f "$OUT/mermaid.min.js" || -n "${FORCE:-}" ]] || [[ -n "$(find apps/ios/scripts/diagram pnpm-lock.yaml -newer "$OUT/mermaid.min.js" -type f | head -1)" ]]; then
  [[ -d apps/desktop/node_modules/mermaid ]] || pnpm install --frozen-lockfile
  rm -rf "$OUT"
  mkdir -p "$OUT"
  cp apps/ios/scripts/diagram/index.html "$OUT/"
  cp apps/desktop/node_modules/mermaid/dist/mermaid.min.js "$OUT/"
else
  echo "› $OUT is up to date"
fi
if [[ -n "${TARGET_BUILD_DIR:-}" ]]; then
  DEST="$TARGET_BUILD_DIR/$UNLOCALIZED_RESOURCES_FOLDER_PATH/Diagram"
  mkdir -p "$DEST"
  rsync -a --delete "$OUT/" "$DEST/"
fi
