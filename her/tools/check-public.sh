#!/bin/sh
# What must not reach a public repository: home folders of this machine, things shaped like keys,
# and the names listed in the file HER_PRIVATE_WORDS points at, kept outside the repository.
set -u
cd "$(dirname "$0")/.."
failed=0

search() {
  # Tracked files only, without the lock files and generated catalogs.
  git ls-files -z -- ':!*.lock' ':!pnpm-lock.yaml' ':!crates/her-llm/catalog/*' ':!tools/check-public.sh' \
    | xargs -0 grep -nIE "$1" 2>/dev/null
}

report() {
  found="$(search "$2" | grep -vE "$3" || true)"
  if [ -n "$found" ]; then
    echo "$1:"
    echo "$found" | cut -c1-160 | head -n 40
    failed=1
  fi
}

report "paths of a home folder" '/(Users|home)/[a-z][a-z0-9_-]+/' '/(Users|home)/(me|you|user|runner|alice|someone|name|example|test|tester|work|dev|ci|demo)/'
report "things shaped like a key" '(sk-[A-Za-z0-9]{32,}|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{36}|-----BEGIN [A-Z ]*PRIVATE KEY-----)' 'BEGIN (TEST|EXAMPLE)'

if [ -n "${HER_PRIVATE_WORDS:-}" ] && [ -f "$HER_PRIVATE_WORDS" ]; then
  while IFS= read -r word; do
    [ -n "$word" ] || continue
    report "the private word $word" "$word" '^$'
  done <"$HER_PRIVATE_WORDS"
fi

if [ "$failed" -ne 0 ]; then
  exit 1
fi
echo "nothing private found"
