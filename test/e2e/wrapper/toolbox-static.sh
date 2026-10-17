#!/bin/sh
# F03 S2 static checks, no engine, no network (plan 02 sections 9.2, 9.4, 14.1, 22):
# 02-AC-1 (make toolbox shape), 02-AC-2 (lock inventory), 02-AC-3 (build-side inputs), 02-AC-9 (Env), 02-R-1.
. "$(dirname "$0")/lib.sh"
echo "toolbox-static (02-AC-1, 02-AC-2, 02-AC-3, 02-AC-9, 02-R-1)"

PY=/opt/sc/stage0-venv/bin/python
[ -x "$PY" ] || PY=python3
RC=0
"$PY" -I "$HERE/toolbox-static.py" "$SC_ROOT" >"$TMP/static.out" 2>"$TMP/static.err" || RC=$?
if [ "$RC" -ne 0 ]; then bad "toolbox-static.py runs" "exit $RC: $(tail -n 3 "$TMP/static.err")"; fi
while IFS="$(printf '\t')" read -r st name detail; do
	case $st in
	ok) ok "$name" ;;
	FAIL) bad "$name" "$detail" ;;
	esac
done <"$TMP/static.out"

# --- 02-AC-1: make toolbox builds both targets from scratch for linux/amd64, on the host (an engine is not
# reachable from inside the ci container, plan 02 section 13), so no ./bootstrap re-exec.
cd "$SC_ROOT" || exit 1
RC=0
OUT=$(env -u SC_IN_CONTAINER -u MAKEFLAGS -u MAKELEVEL -u MAKEOVERRIDES make -n toolbox 2>"$TMP/err") || RC=$?
expect_eq "02-AC-1 make -n toolbox exits 0" 0 "$RC"
expect_nomatch "02-AC-1 make toolbox does not re-exec into the ci image" '\./bootstrap exec' "$OUT"
for tg in toolbox ci; do
	l=$(printf '%s\n' "$OUT" | grep -E "build" | grep -E -- "--target[ =]$tg( |\$)" | head -n 1)
	expect_match "02-AC-1 make toolbox builds --target $tg" "build" "$l"
	expect_match "02-AC-1 the $tg build uses --no-cache" "(^| )--no-cache( |\$)" "$l"
	expect_match "02-AC-1 the $tg build is for linux/amd64" "linux/amd64" "$l"
	expect_match "02-AC-1 the $tg build uses toolbox/Containerfile" "toolbox/Containerfile" "$l"
done

# --- wrapper, stub engine: --image toolbox builds and tags localhost/strange-company-toolbox:in-<inputs12> (02-AC-1/2).
fx=$TMP/tbx
mkrepo "$fx"
id=$(ref_inputs_id "$fx")
tag=localhost/strange-company-toolbox:in-$(id12 "$id")
reset_log
wrap "$fx" exec --image toolbox -- true
expect_eq "02-AC-2 exec --image toolbox exits 0" 0 "$RC"
b=$(grep -E '^(build|image build|buildx build) ' "$STUB_LOG")
expect_eq "02-AC-2 exec --image toolbox records one build" 1 "$(build_calls)"
expect_match "02-AC-2 toolbox build is tagged $tag" "(^|[ =])$tag( |\$)" "$b"
expect_match "02-AC-2 toolbox build targets the toolbox stage" "--target[ =]toolbox( |\$)" "$b"
expect_match "02-AC-2 toolbox build carries the full inputs id" "$id" "$b"
expect_match "02-AC-2 the container runs the toolbox tag" "$tag" "$(run_line)"
reset_log
wrap "$fx" exec --image toolbox -- true
expect_eq "02-AC-2 second toolbox exec reuses the image (no build)" "0:0" "$RC:$(build_calls)"
expect_match "02-AC-2 second toolbox exec runs the same tag" "$tag" "$(run_line)"
# The toolbox and ci images do not share a tag.
reset_log
wrap "$fx" exec --image ci -- true
expect_match "02-AC-1 --image ci still builds its own -ci tag after a toolbox build" "localhost/strange-company-ci:in-$(id12 "$id")" "$(grep -E '^(build|image build|buildx build) ' "$STUB_LOG")"

# --- 02-AC-3 (build side): a build failure leaves no tag in the stub engine's state
: >"$STUB_STATE"
reset_log
STUB_BUILD_FAIL=1 wrap "$fx" exec --image toolbox -- true
expect_eq "02-AC-3 a failing toolbox build exits 3" 3 "$RC"
expect_match "02-AC-3 the failure surfaces TOOLBOX_CHECKSUM_MISMATCH:<tool>" "TOOLBOX_CHECKSUM_MISMATCH:[a-z0-9_-]+" "$ERR$OUT"
expect_eq "02-AC-3 the failed attempt did run one build" 1 "$(build_calls)"
expect_eq "02-AC-3 no toolbox image is tagged after the failed build" 0 "$(grep -c "strange-company-toolbox" "$STUB_STATE" || true)"

finish
