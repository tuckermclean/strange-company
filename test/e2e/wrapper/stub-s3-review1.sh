#!/bin/sh
# F03 S3 review round 1 (stub engine): env-file parser parity with the Go side (plan 02 section 14.2 step 3, plan 50
# section 9.1), CD-67 (a) operator inputs only for the toolbox image, CD-67 (b) REQUIRE_PULL accepts only a pulled
# image, single-quoted image.lock values (plan 02 section 9.3).
. "$(dirname "$0")/lib-s3.sh"
echo "stub-s3-review1 (02-R-3, 02-AC-6, 02-AC-11, CD-67, 02 9.3)"

fx=$TMP/repo
mkrepo "$fx"
REPO=$(cd -P "$fx" && pwd -P)
HOME_=$TMP/home
id=$(ref_inputs_id "$fx")
ttag=localhost/strange-company-toolbox:in-$(id12 "$id")
mkdir -p "$HOME_/.ssh" "$HOME_/keys"
fresh() { : >"$STUB_LOG"; : >"$STUB_STATE"; : >"$STUB_PULLED"; : >"$STUB_PULL_COUNT"; : >"$SLEEP_LOG"; }
mkkey() { printf 'KEYBODY\n' >"$1"; chmod 600 "$1"; }

# --- 2. parser parity: one level of matching quotes
mkkey "$fx/'k'"
cat >"$fx/bootstrap.env" <<'EOT'
SSH_PRIVATE_KEY_PATH="'k'"
EOT
reset_log
wrapx "$fx" "" exec -- true
expect_eq "02 14.2.3 quoted-quote value: exit 0" 0 "$RC"
expect_match "02 14.2.3 \"'k'\" yields 'k' (one quote level removed, the inner quotes stay)" "$(mount_re "'k'" /run/sc-input/ssh/id_key ro)" "$(run_line)"
rm -f "$fx/'k'"

# --- 2. parser parity: a later empty assignment unsets (plan 50 section 9.1), the default key is used
mkkey "$TMP/first.key"
mkkey "$HOME_/.ssh/id_ed25519"
printf 'SSH_PRIVATE_KEY_PATH=%s\nSSH_PRIVATE_KEY_PATH=\n' "$TMP/first.key" >"$fx/bootstrap.env"
reset_log
wrapx "$fx" "" exec -- true
expect_nomatch "02 9.1 KEY=/first then KEY= is unset: the first value is not mounted" "$TMP/first.key" "$(run_line)"
expect_match "02 9.1 KEY=/first then KEY= is unset: the default key is mounted" "$(mount_re "$HOME_/.ssh/id_ed25519" /run/sc-input/ssh/id_key ro)" "$(run_line)"
rm -f "$HOME_/.ssh/id_ed25519"

# --- 2. parser parity: an embedded CR stays, only the CRLF line ending's CR goes
CR=$(printf '\r')
mkkey "$HOME_/keys/a${CR}b"
printf 'SSH_PRIVATE_KEY_PATH=%s/keys/a%sb\r\n' "$HOME_" "$CR" >"$fx/bootstrap.env"
reset_log
wrapx "$fx" "" exec -- true
expect_match "02 14.2.3 an embedded CR is kept in the value and the trailing CR of CRLF is stripped" "$(mount_re "$HOME_/keys/a${CR}b" /run/sc-input/ssh/id_key ro)" "$(run_line)"
rm -f "$HOME_/keys/a${CR}b"

# --- 4. CD-67 (a): --image ci mounts no operator input and creates nothing under HOME
mkkey "$HOME_/keys/mykey"
printf 'ssh-ed25519 AAAA test\n' >"$HOME_/keys/mykey.pub"
sock=$TMP/agent.sock
: >"$sock"
cat >"$fx/bootstrap.env" <<EOT
CLUSTER_NAME=demo
SSH_PRIVATE_KEY_PATH=$HOME_/keys/mykey
SSH_USE_AGENT=true
EOT
WH=$TMP/whome-ci
rm -rf "$WH"
mkdir -p "$WH"
reset_log
WRAP_HOME=$WH wrapx "$fx" "SSH_AUTH_SOCK=$sock" exec --image ci -- true
expect_eq "CD-67a exec --image ci -- true exits 0" 0 "$RC"
L=$(run_line)
expect_match "CD-67a the ci run line exists" "strange-company-ci" "$L"
expect_nomatch "CD-67a ci: no env file mount" "sc-input" "$L"
expect_nomatch "CD-67a ci: no /blackstart mount" "/blackstart" "$L"
expect_nomatch "CD-67a ci: no key, public key or agent socket mount" "$HOME_/keys|$sock|id_key|ssh-agent" "$L"
expect_match "CD-67a ci keeps the repository mount" "$(mount_re "$REPO" "$REPO" "")" "$L"
expect_match "CD-67a ci keeps the /run/sc tmpfs" "/run/sc:" "$L"
if [ -e "$WH/.strange-company" ]; then bad "CD-67a ci creates no black-start directory under HOME"; else ok "CD-67a ci creates no black-start directory under HOME"; fi

# HOME unset: the ci run still works
reset_log
RC=0
(
	cd "$fx" || exit 99
	unset HOME SC_IN_CONTAINER SC_TOOLBOX_BUILD SC_TOOLBOX_REQUIRE_PULL SC_ALLOW_ROOT SC_BLACKSTART_DIR SSH_AUTH_SOCK
	env -u HOME PATH="$STUBBIN:$PATH" SC_CONTAINER_ENGINE=docker "$fx/bootstrap" exec --image ci -- true
) >"$TMP/out" 2>"$TMP/err" </dev/null || RC=$?
expect_eq "CD-67a exec --image ci with HOME unset exits 0" 0 "$RC"
expect_nomatch "CD-67a ci with HOME unset: no black-start directory error" "WRAPPER_BLACKSTART_UNSAFE" "$(cat "$TMP/err") x"

# the toolbox image keeps all of them
WH=$TMP/whome-tb
rm -rf "$WH"
mkdir -p "$WH"
reset_log
WRAP_HOME=$WH wrapx "$fx" "SSH_AUTH_SOCK=$sock" exec --image toolbox -- true
L=$(run_line)
expect_match "CD-67a toolbox keeps the env file mount" "$(mount_re "$REPO/bootstrap.env" /run/sc-input/bootstrap.env ro)" "$L"
expect_match "CD-67a toolbox keeps the SSH key mount" "$(mount_re "$HOME_/keys/mykey" /run/sc-input/ssh/id_key ro)" "$L"
expect_match "CD-67a toolbox keeps the agent socket mount" "$(mount_re "$sock" /run/sc-input/ssh-agent.sock "")" "$L"
expect_match "CD-67a toolbox keeps /blackstart" "$(mount_re "$WH/.strange-company" /blackstart "")" "$L"

# --- 5. CD-67 (b): REQUIRE_PULL accepts only a pulled image, a local build (reused or new) is refused
rm -f "$fx/bootstrap.env"
fresh
wrapx "$fx" "" exec -- true
expect_eq "CD-67b setup: a local build of the toolbox image exists" 1 "$(build_calls)"
reset_log
wrapx "$fx" "SC_TOOLBOX_REQUIRE_PULL=1" exec -- true
expect_eq "CD-67b REQUIRE_PULL=1 with a reusable local build exits 3" 3 "$RC"
expect_match "CD-67b ... naming WRAPPER_TOOLBOX_PULL_FAILED" "WRAPPER_TOOLBOX_PULL_FAILED" "$ERR"
expect_eq "CD-67b ... and runs no container" 0 "$(runs)"
reset_log
wrapx "$fx" "SC_TOOLBOX_REQUIRE_PULL=1" --toolbox-build exec -- true
expect_eq "CD-67b REQUIRE_PULL=1 with --toolbox-build exits 3" 3 "$RC"
expect_match "CD-67b ... naming WRAPPER_TOOLBOX_PULL_FAILED (--toolbox-build)" "WRAPPER_TOOLBOX_PULL_FAILED" "$ERR"
expect_eq "CD-67b ... runs no container (--toolbox-build)" 0 "$(runs)"
expect_eq "CD-67b ... and builds nothing (--toolbox-build)" 0 "$(build_calls)"

# --- 6. image.lock values in single quotes are read like double-quoted ones
a64=$(printf '%064d' 0 | tr 0 a)
b64=$(printf '%064d' 0 | tr 0 b)
TREF=ghcr.io/example-org/strange-company-toolbox@sha256:$a64
cat >"$fx/toolbox/image.lock" <<EOT
apiVersion: strange.company/v1alpha1
kind: ToolboxImageLock
inputsId: '$id'
toolboxImage: 'ghcr.io/example-org/strange-company-toolbox'
toolboxDigest: 'sha256:$a64'
ciImage: 'ghcr.io/example-org/strange-company-ci'
ciDigest: 'sha256:$b64'
EOT
fresh
wrapx "$fx" "" exec --image toolbox -- true
expect_eq "02 9.3 single-quoted image.lock values: exit 0" 0 "$RC"
expect_match "02 9.3 single-quoted values: the pull names <image>@<digest>" "^(pull|image pull) .*$TREF( |\$)" "$(grep -E '^(pull|image pull) ' "$STUB_LOG" | head -n 1)"
expect_match "02 9.3 single-quoted values: source is pulled" "SC_TOOLBOX_SOURCE=pulled( |\$)" "$(run_line)"
cat >"$fx/toolbox/image.lock" <<EOT
apiVersion: strange.company/v1alpha1
kind: ToolboxImageLock
inputsId: "$id"
toolboxImage: 'ghcr.io/example-org/strange-company-toolbox'
toolboxDigest: ''
ciImage: 'ghcr.io/example-org/strange-company-ci'
ciDigest: ''
EOT
fresh
wrapx "$fx" "" exec --image toolbox -- true
expect_eq "02 9.3 an empty single-quoted toolboxDigest means always build: exit 0" 0 "$RC"
expect_eq "02 9.3 an empty single-quoted toolboxDigest: no pull" 0 "$(pulls)"
expect_eq "02 9.3 an empty single-quoted toolboxDigest: build" 1 "$(build_calls)"

finish
