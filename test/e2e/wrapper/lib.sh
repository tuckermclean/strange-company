# shellcheck shell=sh
# Shared helpers for the wrapper tests (plan 02 section 22). POSIX sh, no Go needed.
# Sourced by the test scripts; never executed directly.

SC_ROOT=$(cd "$(dirname "$0")/../../.." && pwd -P)
HERE=$SC_ROOT/test/e2e/wrapper
T_FAILS=0
T_CHECKS=0
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

ok() { T_CHECKS=$((T_CHECKS + 1)); printf '  ok   %s\n' "$1"; }
bad() {
	T_CHECKS=$((T_CHECKS + 1))
	T_FAILS=$((T_FAILS + 1))
	printf '  FAIL %s\n' "$1"
	[ -z "${2:-}" ] || printf '       %s\n' "$2" | head -n 12
}
expect_eq() { # name expected actual
	if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected [$2] got [$3]"; fi
}
expect_match() { # name ERE text
	if printf '%s\n' "$3" | grep -Eq -- "$2"; then ok "$1"; else bad "$1" "pattern [$2] not found in: $3"; fi
}
expect_nomatch() { # name ERE text; an empty text fails (no vacuous passes)
	if [ -z "$3" ]; then bad "$1" "nothing recorded to inspect"; return; fi
	if printf '%s\n' "$3" | grep -Eq -- "$2"; then bad "$1" "unexpected pattern [$2] in: $3"; else ok "$1"; fi
}
sanity() { # harness self-check: silent when true, a failure otherwise
	[ "$1" = "$2" ] || bad "harness sanity: $3" "[$1] != [$2]"
}
finish() {
	printf '%s: %s checks, %s failed\n' "$(basename "$0")" "$T_CHECKS" "$T_FAILS"
	[ "$T_FAILS" -eq 0 ]
}

# Reference implementation of plan 02 section 14.2 step 5, independent of ./bootstrap.
ref_inputs_id() { # repo-dir
	(
		cd "$1" || exit 1
		git ls-files -co --exclude-standard -- toolbox src .dockerignore tools/stage0/requirements.txt |
			grep -v -e '^toolbox/image\.lock$' -e '/testdata/' -e '_test\.go$' |
			LC_ALL=C sort |
			while IFS= read -r f; do printf '%s %s\n' "$(git hash-object "$f")" "$f"; done |
			git hash-object --stdin
	)
}
id12() { printf '%s' "$1" | cut -c1-12; }

# A stub engine on PATH that records its arguments (plan 02 section 22).
STUBBIN=$TMP/stubbin
mkdir -p "$STUBBIN"
cp "$HERE/engine-stub.sh" "$STUBBIN/docker"
cp "$HERE/engine-stub.sh" "$STUBBIN/podman"
chmod +x "$STUBBIN/docker" "$STUBBIN/podman"
STUB_LOG=$TMP/engine.log
STUB_STATE=$TMP/engine.state
: >"$STUB_LOG"
: >"$STUB_STATE"
export STUB_LOG STUB_STATE

git_() { git -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false "$@"; }

# mkrepo DIR: a committed fixture repository holding the real ./bootstrap and a tiny toolbox/src.
mkrepo() {
	mkdir -p "$1/toolbox/bin" "$1/src/testdata" "$1/plans" "$1/tools/stage0"
	(
		cd "$1" || exit 1
		git init -q . && git checkout -q -b main 2>/dev/null
		if [ -f "$SC_ROOT/bootstrap" ]; then
			cp -p "$SC_ROOT/bootstrap" bootstrap
		else
			printf '#!/bin/sh\necho "bootstrap: missing from repository" >&2\nexit 127\n' >bootstrap
		fi
		chmod +x bootstrap
		printf 'FROM scratch AS ci\n' >toolbox/Containerfile
		printf 'kind: ToolboxLock\n' >toolbox/tools.lock.yaml
		printf 'kind: ToolboxImageLock\n' >toolbox/image.lock
		printf 'module example.invalid/sc\n\ngo 1.26\n\ntoolchain go1.26.1\n' >src/go.mod
		printf 'package x\n' >src/main_test.go
		printf 'data\n' >src/testdata/x
		printf '# plan\n' >plans/p.md
		printf 'pyyaml==6.0 --hash=sha256:00\n' >tools/stage0/requirements.txt
		printf '*\n!toolbox/**\n!src/**\n' >.dockerignore
		git add -A && git_ commit -q -m fixture
	)
}

# wrap DIR ARGS...: run DIR/bootstrap with the stub engine. Sets RC, OUT, ERR.
# Environment knobs: ENGINE (docker|podman, default docker), STUB_ROOTLESS, STUB_RUN_EXIT, STUB_BUILD_FAIL.
wrap() {
	_d=$1
	shift
	mkdir -p "$TMP/home"
	RC=0
	(
		cd "${WRAP_CWD:-$_d}" || exit 99
		unset SC_IN_CONTAINER SC_TOOLBOX_BUILD SC_TOOLBOX_REQUIRE_PULL SC_ALLOW_ROOT
		PATH="$STUBBIN:$PATH" HOME="$TMP/home" SC_CONTAINER_ENGINE="${ENGINE:-docker}" \
			"$_d/bootstrap" "$@"
	) >"$TMP/out" 2>"$TMP/err" </dev/null || RC=$?
	OUT=$(cat "$TMP/out")
	ERR=$(cat "$TMP/err")
	unset STUB_RUN_EXIT STUB_ROOTLESS STUB_BUILD_FAIL ENGINE
}
reset_log() { : >"$STUB_LOG"; }
build_calls() { grep -Ec '^(build|image build|buildx build)( |$)' "$STUB_LOG" || true; }
run_line() { grep -E '^(run|container run) ' "$STUB_LOG" | tail -n 1 || true; }
