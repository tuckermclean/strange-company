#!/bin/sh
# 02-AC-15 (static side): `make toolbox-check` rejects a Containerfile FROM without a digest.
# Needs GNU make and (through the Makefile re-exec) a working engine, or SC_IN_CONTAINER=1 with the ci tools on PATH.
# Not part of run.sh, because toolbox-check itself runs run.sh.
. "$(dirname "$0")/lib.sh"
echo "check-pins (02-AC-15)"

fx=$TMP/tree
mkdir -p "$fx"
cd "$SC_ROOT" || exit 1
git ls-files -z | tar --null -T - -cf - | tar -xf - -C "$fx"

RC=0
(cd "$fx" && make toolbox-check) >"$TMP/clean.out" 2>&1 || RC=$?
CLEAN_RC=$RC
expect_eq "02-AC-15 make toolbox-check passes on the untouched tree (precondition)" 0 "$RC"

mkdir -p "$fx/toolbox"
cp "$HERE/testdata/Containerfile.nodigest" "$fx/toolbox/Containerfile"
RC=0
(cd "$fx" && make toolbox-check) >"$TMP/bad.out" 2>&1 || RC=$?
[ "$RC" -ne 0 ] && [ "$CLEAN_RC" -eq 0 ] && ok "02-AC-15 make toolbox-check fails for FROM golang:1.27 without a digest" || bad "02-AC-15 make toolbox-check fails for FROM golang:1.27 without a digest" "exit 0"
[ "$CLEAN_RC" -ne 0 ] || expect_match "02-AC-15 the failure mentions the unpinned FROM" "golang:1\.27|digest" "$(cat "$TMP/bad.out")"

finish
