#!/bin/sh
# F03 S4 (stub engine): the image.lock reference may name a registry with a port (02-AC-4 pulls from a local registry
# at localhost:<port>), env-file and image.lock reading is locale-independent (S3 review 2 N3), and an explicit
# --env-file that does not exist is reported for --image ci too (N5).
. "$(dirname "$0")/lib-s3.sh"
echo "stub-s4-wrapper (02-AC-4 wrapper side, 02-AC-11, plan 02 9.3, 14.2 step 3, 14.3)"

fx=$TMP/repo
mkrepo "$fx"
REPO=$(cd -P "$fx" && pwd -P)
HOME_=$TMP/home
id=$(ref_inputs_id "$fx")
mkdir -p "$HOME_/.ssh" "$HOME_/keys"
fresh() { : >"$STUB_LOG"; : >"$STUB_STATE"; : >"$STUB_PULLED"; : >"$STUB_PULL_COUNT"; : >"$SLEEP_LOG"; }
mkkey() { printf 'KEYBODY\n' >"$1"; chmod 600 "$1"; }
a64=$(printf '%064d' 0 | tr 0 a)

# --- 02-AC-4 / plan 02 9.3: image.lock may name a registry with a port, as a local OCI registry does
for img in localhost:5000/strange-company-toolbox 127.0.0.1:5001/sc/strange-company-toolbox; do
	cat >"$fx/toolbox/image.lock" <<EOT
apiVersion: strange.company/v1alpha1
kind: ToolboxImageLock
inputsId: $id
toolboxImage: $img
toolboxDigest: sha256:$a64
ciImage: ""
ciDigest: ""
EOT
	fresh
	wrapx "$fx" "SC_TOOLBOX_REQUIRE_PULL=1" exec -- true
	expect_eq "02-AC-4 image.lock names $img: exit 0 (not WRAPPER_LOCK_INVALID)" 0 "$RC"
	expect_nomatch "02-AC-4 $img: the lock is not rejected as malformed" "WRAPPER_LOCK_INVALID" "$ERR x"
	expect_match "02-AC-4 $img: the pull names <image>@<digest>" "^(pull|image pull) .*$img@sha256:$a64( |\$)" "$(grep -E '^(pull|image pull) ' "$STUB_LOG" | head -n 1)"
	expect_match "02-AC-4 $img: the container runs the pulled reference" " $img@sha256:$a64( |\$)" "$(run_line)"
	expect_match "02-AC-4 $img: SC_TOOLBOX_SOURCE=pulled" "SC_TOOLBOX_SOURCE=pulled( |\$)" "$(run_line)"
	expect_eq "02-AC-4 $img: no build" 0 "$(build_calls)"
done
# A malformed digest is still refused with a port in the name.
cat >"$fx/toolbox/image.lock" <<EOT
apiVersion: strange.company/v1alpha1
kind: ToolboxImageLock
inputsId: $id
toolboxImage: localhost:5000/strange-company-toolbox
toolboxDigest: sha256:nothex
ciImage: ""
ciDigest: ""
EOT
fresh
wrapx "$fx" "" exec -- true
expect_eq "02 9.3 a malformed digest with a registry port is still refused: exit 2" 2 "$RC"
expect_match "02 9.3 ... as WRAPPER_LOCK_INVALID" "WRAPPER_LOCK_INVALID" "$ERR"
expect_eq "02 9.3 ... without a pull" 0 "$(pulls)"
printf 'kind: ToolboxImageLock\n' >"$fx/toolbox/image.lock"

# --- N3: reading is locale-independent. The Go side trims only " \t\r\n\v\f", and so must sed in any locale.
EMSP=$(printf '\342\200\203') # U+2003 EM SPACE: a [[:space:]] member in a UTF-8 locale, not trimmable by Go's rule
mkkey "$HOME_/keys/em${EMSP}"
printf 'SSH_PRIVATE_KEY_PATH=%s/keys/em%s\n' "$HOME_" "$EMSP" >"$fx/bootstrap.env"
for loc in C C.UTF-8; do
	reset_log
	wrapx "$fx" "LC_ALL=$loc" exec -- true
	expect_match "N3 LC_ALL=$loc: a trailing U+2003 is part of the value (only ASCII whitespace is trimmed)" "$(mount_re "$HOME_/keys/em${EMSP}" /run/sc-input/ssh/id_key ro)" "$(run_line)"
done
rm -f "$HOME_/keys/em${EMSP}"
# The recorded engine log holds a raw 0xff byte below: inspect it bytewise (grep -a, LC_ALL=C).
run_line_a() { grep -a -E '^(run|container run) ' "$STUB_LOG" | tail -n 1 || true; }
expect_mount_a() { # name SRC DST MODE
	if run_line_a | LC_ALL=C grep -a -Eq -- "$(mount_re "$2" "$3" "$4")"; then ok "$1"; else bad "$1" "mount $2 -> $3 not in the run line"; fi
}
BAD=$(printf 'k\377')
mkkey "$HOME_/keys/$BAD"
printf 'SSH_PRIVATE_KEY_PATH="%s/keys/%s"\n' "$HOME_" "$BAD" >"$fx/bootstrap.env"
for loc in C C.UTF-8; do
	reset_log
	wrapx "$fx" "LC_ALL=$loc" exec -- true
	expect_mount_a "N3 LC_ALL=$loc: a quoted value with an invalid UTF-8 byte has its quotes removed" "$HOME_/keys/$BAD" /run/sc-input/ssh/id_key ro
done
rm -f "$HOME_/keys/$BAD" "$fx/bootstrap.env"

# --- N5: an explicit --env-file that does not exist is never silently ignored, for --image ci either
reset_log
wrapx "$fx" "" --env-file "$TMP/missing.env" exec --image ci -- true
case $RC in
0) expect_match "N5 --image ci with a missing explicit --env-file warns, naming the file" "$TMP/missing.env" "$ERR" ;;
*) expect_match "N5 --image ci with a missing explicit --env-file fails, naming the file" "$TMP/missing.env" "$ERR" ;;
esac
reset_log
wrapx "$fx" "" --env-file "$TMP/missing.env" exec --image toolbox -- true
expect_eq "N5 baseline: --image toolbox with a missing explicit --env-file exits 2" 2 "$RC"
expect_match "N5 baseline: ... naming the file" "$TMP/missing.env" "$ERR"

finish
