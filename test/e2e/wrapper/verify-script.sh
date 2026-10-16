#!/bin/sh
# 01-AC-6 / 52-AC-5 (S4 review): test/e2e/verify/run must not delete anything in the checkout it is started from;
# it works in a fresh `git clone --no-local` under a temp directory and removes only that directory.
. "$(dirname "$0")/lib.sh"
echo "verify-script (01-AC-6, 52-AC-5)"

S=$SC_ROOT/test/e2e/verify/run
BODY=$(cat "$S")
expect_match "verify/run clones with git clone --no-local" 'git clone --no-local' "$BODY"
expect_match "verify/run works under a mktemp -d directory" 'mktemp -d' "$BODY"
expect_match "verify/run makes the read-only Go module cache writable before cleanup" 'chmod -R u\+w' "$BODY"
expect_match "verify/run copies the summaries back to out/verify-e2e" 'out/verify-e2e' "$BODY"
del=$(printf '%s\n' "$BODY" | grep -E '(^|[^A-Za-z_])rm +(-[a-z]*r[a-z]*|-[a-z]+ +-[a-z]*r[a-z]*)' || true)
expect_nomatch "verify/run has no rm -r touching .cache or out" '\.cache|(^|[ "/])out([ "/]|$)' "${del:-none}"
expect_match "verify/run removes a temp directory variable" 'rm -rf "?\$' "$BODY"

finish
