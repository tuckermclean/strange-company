#!/bin/sh
# F03 S3: toolbox/bin/sc-entrypoint (plan 02 section 14.5), run on the host with a fake sc-bootstrap on PATH.
# Contract used by the test: the entrypoint reads SC_RUN_DIR (default /run/sc) and SC_INPUT_DIR (default
# /run/sc-input), as sc-bootstrap does (plan 02 section 14.4 fallback, plan 02 section 7.2).
. "$(dirname "$0")/lib.sh"
echo "stub-s3-entrypoint (02-AC-7, 02-AC-12 in-cluster mode, plan 02 14.5)"

EP=$SC_ROOT/toolbox/bin/sc-entrypoint
EB=$TMP/ebin
RUN=$TMP/run
IN=$TMP/in
CALLS=$TMP/calls
BASHLOG=$TMP/bashlog
mkdir -p "$EB" "$RUN" "$IN"
cat >"$EB/sc-bootstrap" <<'EOT'
#!/bin/sh
printf '%s\n' "$*" >>"$CALLS"
exit "${FAKE_SC_EXIT:-0}"
EOT
cat >"$EB/tool" <<'EOT'
#!/bin/sh
printf 'args=%s\n' "$*"
printf 'SSH_AUTH_SOCK=%s\n' "${SSH_AUTH_SOCK:-}"
exit "${TOOL_EXIT:-0}"
EOT
cat >"$EB/bash" <<'EOT'
#!/bin/sh
printf '%s\n' "$*" >>"$BASHLOG"
EOT
chmod +x "$EB/sc-bootstrap" "$EB/tool" "$EB/bash"

# ep ENV... -- ARGS...: run the entrypoint in a clean environment; sets RC, OUT, ERR.
ep() {
	_e=
	while [ "$1" != -- ]; do
		_e="$_e $1"
		shift
	done
	shift
	RC=0
	# shellcheck disable=SC2086
	env -i PATH="$EB:/usr/bin:/bin" CALLS="$CALLS" BASHLOG="$BASHLOG" $_e "$EP" "$@" >"$TMP/out" 2>"$TMP/err" </dev/null || RC=$?
	OUT=$(cat "$TMP/out")
	ERR=$(cat "$TMP/err")
}
clear() { rm -f "$CALLS" "$BASHLOG"; }
calls() { cat "$CALLS" 2>/dev/null || true; }

# --- in-cluster mode: no SC_REPO_ROOT, the entrypoint just execs its arguments (plan 02 14.5, 02-AC-12)
clear
ep SC_RUN_DIR="$RUN" SC_INPUT_DIR="$IN" -- sc-bootstrap version
expect_eq "02-AC-12 in-cluster: sc-bootstrap version runs" 0 "$RC"
expect_eq "02-AC-12 in-cluster: the arguments are exec'd as they are (no init, no --env-file)" "version" "$(calls)"
clear
TOOL_EXIT=5 ep SC_RUN_DIR="$RUN" SC_INPUT_DIR="$IN" TOOL_EXIT=5 -- tool a b
expect_eq "02-AC-12 in-cluster: the exit code of the command is returned" 5 "$RC"
expect_match "02-AC-12 in-cluster: other commands are exec'd with their arguments" "^args=a b\$" "$OUT"

# --- operator mode: init first, then the dispatch
for verb in run plan preflight status verify expand destroy recover report rotate state unlock version; do
	clear
	ep SC_REPO_ROOT="$TMP" SC_RUN_DIR="$RUN" SC_INPUT_DIR="$IN" -- "$verb" --x y
	expect_eq "02 14.5 $verb: exit 0" 0 "$RC"
	expect_eq "02 14.5 $verb: sc-bootstrap toolbox init runs first" "toolbox init" "$(calls | sed -n 1p)"
	expect_match "02 14.5 $verb: then sc-bootstrap --env-file <input>/bootstrap.env $verb --x y" "^--env-file /[^ ]*bootstrap\.env $verb --x y\$" "$(calls | sed -n 2p)"
done
clear
FAKE_SC_EXIT=3 ep SC_REPO_ROOT="$TMP" SC_RUN_DIR="$RUN" SC_INPUT_DIR="$IN" FAKE_SC_EXIT=3 -- preflight
expect_eq "02 14.5 the exit code of sc-bootstrap is returned (3)" 3 "$RC"

# --- exec: runs the command after init; stdout/exit code pass through (02-AC-7)
clear
ep SC_REPO_ROOT="$TMP" SC_RUN_DIR="$RUN" SC_INPUT_DIR="$IN" -- exec -- tool x y
expect_eq "02-AC-7 exec -- tool: exit 0" 0 "$RC"
expect_match "02-AC-7 exec -- tool runs the command with its arguments" "^args=x y\$" "$OUT"
expect_eq "02-AC-7 exec: init ran first" "toolbox init" "$(calls | sed -n 1p)"
clear
ep SC_REPO_ROOT="$TMP" SC_RUN_DIR="$RUN" SC_INPUT_DIR="$IN" TOOL_EXIT=7 -- exec -- tool
expect_eq "02-AC-7 exec: the exit code of the command is returned (7)" 7 "$RC"

# --- agent socket (plan 02 14.5 step 2)
rm -f "$IN/ssh-agent.sock"
clear
ep SC_REPO_ROOT="$TMP" SC_RUN_DIR="$RUN" SC_INPUT_DIR="$IN" -- exec -- tool
expect_match "02 14.5 no agent socket: SSH_AUTH_SOCK is not set" "^SSH_AUTH_SOCK=\$" "$OUT"
python3 -I -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])' "$IN/ssh-agent.sock" 2>/dev/null || : >"$IN/ssh-agent.sock"
clear
ep SC_REPO_ROOT="$TMP" SC_RUN_DIR="$RUN" SC_INPUT_DIR="$IN" -- exec -- tool
expect_match "02 14.5 with <input>/ssh-agent.sock: SSH_AUTH_SOCK points at it" "^SSH_AUTH_SOCK=/.*ssh-agent\.sock\$" "$OUT"

# --- shell (plan 02 14.6)
clear
ep SC_REPO_ROOT="$TMP" SC_RUN_DIR="$RUN" SC_INPUT_DIR="$IN" -- shell
expect_eq "02 14.6 shell starts bash with the shellrc, interactive" "--rcfile /usr/share/strange-company/shellrc -i" "$(cat "$BASHLOG" 2>/dev/null)"
expect_eq "02 14.6 shell runs init first" "toolbox init" "$(calls | sed -n 1p)"

finish
