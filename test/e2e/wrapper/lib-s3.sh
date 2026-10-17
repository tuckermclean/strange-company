# shellcheck shell=sh
# Shared helpers for the F03 S3 wrapper tests (stub engine with pull/save, fake sleep, env-file fixtures).
# Sourced after nothing else: it sources lib.sh itself. Never executed directly.
. "$(dirname "$0")/lib.sh"
export HERE

# Replace the plain stub engine by the pull/save-capable one, and add a fake sleep that records its argument.
cp "$HERE/engine-stub-pull.sh" "$STUBBIN/docker"
cp "$HERE/engine-stub-pull.sh" "$STUBBIN/podman"
cat >"$STUBBIN/sleep" <<'EOT'
#!/bin/sh
echo "$1" >>"$SLEEP_LOG"
EOT
chmod +x "$STUBBIN/docker" "$STUBBIN/podman" "$STUBBIN/sleep"
SLEEP_LOG=$TMP/sleep.log
STUB_PULLED=$TMP/pulled
STUB_PULL_COUNT=$TMP/pullcount
: >"$SLEEP_LOG"
: >"$STUB_PULLED"
: >"$STUB_PULL_COUNT"
export SLEEP_LOG STUB_PULLED STUB_PULL_COUNT

# Fake root: an id that reports uid 0 (the wrapper must refuse to run as host root).
ROOTBIN=$TMP/rootbin
mkdir -p "$ROOTBIN"
cat >"$ROOTBIN/id" <<'EOT'
#!/bin/sh
case $1 in -u) echo 0 ;; *) exec /usr/bin/id "$@" ;; esac
EOT
chmod +x "$ROOTBIN/id"

# wrapx DIR "NAME=value NAME=value" ARGS...: like wrap, with extra environment (no spaces in values). Sets RC, OUT, ERR.
# Knobs as for wrap: ENGINE (docker|podman), STUB_ROOTLESS, STUB_RUN_EXIT, STUB_BUILD_FAIL, STUB_PULL_FAIL, WRAP_HOME.
wrapx() {
	_d=$1
	_e=$2
	shift 2
	mkdir -p "${WRAP_HOME:-$TMP/home}"
	RC=0
	(
		cd "${WRAP_CWD:-$_d}" || exit 99
		unset SC_IN_CONTAINER SC_TOOLBOX_BUILD SC_TOOLBOX_REQUIRE_PULL SC_ALLOW_ROOT SC_BLACKSTART_DIR SSH_AUTH_SOCK
		# shellcheck disable=SC2086 # _e is a space-separated list of NAME=value words
		env PATH="$STUBBIN:$PATH" HOME="${WRAP_HOME:-$TMP/home}" SC_CONTAINER_ENGINE="${ENGINE:-docker}" $_e "$_d/bootstrap" "$@"
	) >"$TMP/out" 2>"$TMP/err" </dev/null || RC=$?
	OUT=$(cat "$TMP/out")
	ERR=$(cat "$TMP/err")
	unset STUB_RUN_EXIT STUB_ROOTLESS STUB_BUILD_FAIL STUB_PULL_FAIL ENGINE
}
log_all() { cat "$STUB_LOG"; }
runs() { grep -Ec '^(run|container run) ' "$STUB_LOG" || true; }
pulls() { grep -Ec '^(pull|image pull) ' "$STUB_LOG" || true; }
saves() { grep -E '^(save|image save) ' "$STUB_LOG" || true; }
# expect_absent NAME NEEDLE TEXT: fixed-string needle must not occur; empty text fails (nothing to inspect).
expect_absent() {
	if [ -z "$3" ]; then bad "$1" "nothing recorded to inspect"; return; fi
	if printf '%s\n' "$3" | grep -Fq -- "$2"; then bad "$1" "found [$2] in: $3"; else ok "$1"; fi
}
# mount_re SRC DST MODE: regex for a bind mount in a run line (-v, --volume or --mount forms).
mount_re() {
	_m=
	[ -z "$3" ] || _m=:$3
	printf '(-v|--volume)[ =]%s:%s%s( |$)|--mount[ =][^ ]*(src|source)=%s,[^ ]*(dst|destination|target)=%s' "$1" "$2" "$_m" "$1" "$2"
}
# image.lock fixture: lock DIR INPUTS_ID TOOLBOX_DIGEST CI_DIGEST
write_lock() {
	cat >"$1/toolbox/image.lock" <<EOT
apiVersion: strange.company/v1alpha1
kind: ToolboxImageLock
inputsId: $2
toolboxImage: ghcr.io/example-org/strange-company-toolbox
toolboxDigest: $3
ciImage: ghcr.io/example-org/strange-company-ci
ciDigest: $4
sourceCommit: 0123456789abcdef0123456789abcdef01234567
publishedAt: 2026-10-13T12:00:00Z
EOT
}
