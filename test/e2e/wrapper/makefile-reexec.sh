#!/bin/sh
# 01-AC-5: Makefile re-exec contract (plan 01 section 8.1). Needs GNU make, no engine.
. "$(dirname "$0")/lib.sh"
echo "makefile-reexec (01-AC-5)"

cd "$SC_ROOT" || exit 1
RC=0
OUT=$(env -u SC_IN_CONTAINER make -n verify 2>"$TMP/err") || RC=$?
expect_eq "01-AC-5 make -n verify (SC_IN_CONTAINER unset) exits 0" 0 "$RC"
n=$(printf '%s\n' "$OUT" | grep -c . || true)
expect_eq "01-AC-5 prints exactly one command" 1 "$n"
expect_match "01-AC-5 the command starts with ./bootstrap exec --image ci -- make verify" '^\./bootstrap exec --image ci -- make verify' "$OUT"

RC=0
OUT=$(SC_IN_CONTAINER=1 make -n verify 2>"$TMP/err") || RC=$?
expect_eq "01-AC-5 make -n verify (SC_IN_CONTAINER=1) exits 0" 0 "$RC"
expect_nomatch "01-AC-5 no re-exec inside the container" '\./bootstrap exec' "$OUT"
for t in toolbox-check layout-check locks-check lint; do
	expect_match "01-AC-5 tier-V sequence includes $t" "(^|[^A-Za-z0-9_-])$t([^A-Za-z0-9_-]|\$)" "$OUT"
done

finish
