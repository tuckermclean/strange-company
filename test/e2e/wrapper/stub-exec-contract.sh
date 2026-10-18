#!/bin/sh
# 02-AC-16 (wrapper side), 02-AC-15 (build failure side), plan 02 14.4/14.5/7.1, with the stub engine.
. "$(dirname "$0")/lib.sh"
echo "stub-exec-contract (02-AC-16, 02-AC-15)"

fx=$TMP/repo
mkrepo "$fx"
REPO=$(cd -P "$fx" && pwd -P)
uid=$(id -u)
gid=$(id -g)

# Docker rootful.
reset_log
STUB_RUN_EXIT=7 ENGINE=docker wrap "$fx" exec --image ci -- sh -c 'exit 7'
expect_eq "02-AC-16 exit code of the command is returned (7)" 7 "$RC"
L=$(run_line)
expect_match "02-AC-16 docker rootful: --user uid:gid" "--user[ =]$uid:$gid( |\$)" "$L"
expect_match "02-AC-16 common flag --rm" " --rm( |\$)" "$L"
expect_match "02-AC-16 common flag -i" " -i( |\$)|-[a-z]*i[a-z]* " "$L"
expect_nomatch "02-AC-16 no TTY allocated when stdin is not a TTY" " (-t|-it|-ti|--tty)( |\$)" "$L"
expect_match "02-AC-16 --cap-drop ALL" "--cap-drop[ =]ALL" "$L"
expect_match "02-AC-16 no-new-privileges" "--security-opt[ =]no-new-privileges" "$L"
expect_match "02-AC-16 network defaults to bridge" "--network[ =]bridge" "$L"
expect_match "02-AC-16 hostname sc-toolbox" "--hostname[ =]sc-toolbox" "$L"
expect_match "02-AC-16 container name sc-toolbox-<pid>" "--name[ =]sc-toolbox-[0-9]+" "$L"
expect_match "02-AC-16 repository mounted at its host path" "(-v|--volume)[ =]$REPO:$REPO|src=$REPO,.*dst=$REPO|source=$REPO,.*target=$REPO" "$L"
expect_match "02-AC-16 workdir is the host cwd" "(-w|--workdir)[ =]$REPO( |\$)" "$L"
expect_match "02-AC-16 /run/sc tmpfs mode 0700" "--tmpfs[ =]/run/sc:[^ ]*mode=0700" "$L"
expect_match "02-AC-16 /tmp tmpfs" "--tmpfs[ =]/tmp[: ]" "$L"
expect_match "02-AC-16 SC_REPO_ROOT passed" "(-e|--env)[ =]SC_REPO_ROOT=$REPO( |\$)" "$L"
expect_match "02-AC-16 command follows the image, unchanged" "localhost/strange-company-ci:in-[0-9a-f]{12} sh -c exit 7\$" "$L"
expect_nomatch "02-AC-16 no container socket is mounted" "docker\.sock|podman\.sock" "$L"
expect_nomatch "02-AC-16 no privileged run" "--privileged" "$L"

# --network none.
reset_log
wrap "$fx" exec --image ci --network none -- true
expect_match "02-AC-16 --network none is passed to the engine" "--network[ =]none" "$(run_line)"

# Working directory below the repo root.
mkdir -p "$fx/src"
reset_log
WRAP_CWD=$fx/src wrap "$fx" exec --image ci -- true
expect_match "02-AC-16 workdir is a subdirectory when invoked there" "(-w|--workdir)[ =]$REPO/src( |\$)" "$(run_line)"

# Docker rootless.
reset_log
STUB_ROOTLESS=1 ENGINE=docker wrap "$fx" exec --image ci -- true
expect_match "02-AC-16 docker-rootless: --user 0:0" "--user[ =]0:0( |\$)" "$(run_line)"

# Podman rootless, selected by the --engine flag.
reset_log
STUB_ROOTLESS=1 wrap "$fx" --engine podman exec --image ci -- true
L=$(run_line)
expect_match "02-AC-16 podman-rootless: --userns=keep-id (CD-49)" "--userns[ =]keep-id( |\$)" "$L"
expect_match "02-AC-16 podman-rootless: --user uid:gid as well (CD-49)" "--user[ =]$uid:$gid( |\$)" "$L"
expect_match "02-AC-16 podman-rootless: /run/sc tmpfs owned by the container user (U)" "--tmpfs[ =]/run/sc:[^ ]*(U|uid=)" "$L"

# Podman rootful: --user uid:gid, no keep-id (plan 02 14.4).
reset_log
STUB_ROOTLESS=0 wrap "$fx" --engine podman exec --image ci -- true
L=$(run_line)
expect_match "02-AC-16 podman-rootful: --user uid:gid" "--user[ =]$uid:$gid( |\$)" "$L"
expect_nomatch "02-AC-16 podman-rootful: no --userns keep-id" "--userns" "$L"

# /tmp tmpfs must be executable: Docker mounts tmpfs noexec unless "exec" is given, and the toolbox runs
# go-build outputs and scratch scripts from /tmp (plan 02 14.3 mount table, 14.5 run flags). Every engine mode.
tmp_opts() { # run-line -> comma list of the /tmp tmpfs options, one per line
	printf '%s\n' "$1" | tr ' ' '\n' | awk '
		prev == "--tmpfs" && $0 ~ /^\/tmp(:|$)/ { sub(/^\/tmp:?/, ""); print; exit }
		$0 ~ /^--tmpfs=\/tmp(:|$)/ { sub(/^--tmpfs=\/tmp:?/, ""); print; exit }
		{ prev = $0 }' | tr ',' '\n'
}
for mode in "docker rootful|docker|0|" "docker rootless|docker|1|" "podman rootless|podman|1|" "podman rootful|podman|0|"; do
	label=${mode%%|*}
	rest=${mode#*|}
	eng=${rest%%|*}
	rest=${rest#*|}
	rl=${rest%%|*}
	reset_log
	STUB_ROOTLESS=$rl ENGINE=$eng wrap "$fx" exec --image ci -- true
	opts=$(tmp_opts "$(run_line)")
	for o in exec nosuid nodev size=1g; do
		if printf '%s\n' "$opts" | grep -Fxq -- "$o"; then ok "02-AC-16 $label: /tmp tmpfs option $o"; else bad "02-AC-16 $label: /tmp tmpfs option $o" "options: $(printf '%s' "$opts" | tr '\n' ',')"; fi
	done
	expect_nomatch "02-AC-16 $label: /tmp tmpfs is not noexec" "^noexec\$" "${opts:-none}"
done

# TTY allocation: -t only when stdin and stdout are both TTYs (plan 02 7.1), via script(1).
if command -v script >/dev/null 2>&1 && script -qec true /dev/null >/dev/null 2>&1 </dev/null; then
	reset_log
	cat >"$TMP/tty-run.sh" <<EOT
#!/bin/sh
cd "$fx" || exit 99
unset SC_IN_CONTAINER SC_TOOLBOX_BUILD SC_TOOLBOX_REQUIRE_PULL SC_ALLOW_ROOT
PATH="$STUBBIN:\$PATH" HOME="$TMP/home" SC_CONTAINER_ENGINE=docker exec "$fx/bootstrap" exec --image ci -- true
EOT
	chmod +x "$TMP/tty-run.sh"
	script -qec "$TMP/tty-run.sh" /dev/null >/dev/null 2>&1 </dev/null
	expect_match "02-AC-16 -t allocated when stdin and stdout are TTYs" " (-t|-it|-ti|--tty)( |\$)" "$(run_line)"
else
	echo "  SKIP 02-AC-16 -t allocated when stdin and stdout are TTYs (script(1) unavailable or unusable)"
fi

# Signals (plan 02 14.6: exit code and signals propagate): SIGTERM to the wrapper reaches the engine; nothing is orphaned.
reset_log
rm -f "$TMP/sig.pid" "$TMP/sig.got" "$TMP/wrap.pid" "$TMP/sig.child"
(
	cd "$fx" || exit 99
	unset SC_IN_CONTAINER SC_TOOLBOX_BUILD SC_TOOLBOX_REQUIRE_PULL SC_ALLOW_ROOT
	export PATH="$STUBBIN:$PATH" HOME="$TMP/home" SC_CONTAINER_ENGINE=docker
	export STUB_RUN_SLEEP=1 STUB_SIG_PID="$TMP/sig.pid" STUB_SIG_GOT="$TMP/sig.got" STUB_SIG_CHILD="$TMP/sig.child"
	sh -c 'echo $$ >"$1"; shift; exec "$@"' sh "$TMP/wrap.pid" "$fx/bootstrap" exec --image ci -- sleep 60
) >/dev/null 2>&1 </dev/null &
bg=$!
i=0
while [ ! -s "$TMP/sig.pid" ] && [ "$i" -lt 100 ]; do
	sleep 0.1
	i=$((i + 1))
done
if [ -s "$TMP/sig.pid" ] && [ -s "$TMP/wrap.pid" ]; then
	kill -TERM "$(cat "$TMP/wrap.pid")" 2>/dev/null
	i=0
	while [ ! -s "$TMP/sig.got" ] && [ "$i" -lt 50 ]; do
		sleep 0.1
		i=$((i + 1))
	done
	expect_eq "02-AC-16 SIGTERM to the wrapper reaches the engine client" TERM "$(cat "$TMP/sig.got" 2>/dev/null)"
	sleep 0.3
	if kill -0 "$(cat "$TMP/sig.pid")" 2>/dev/null; then bad "02-AC-16 no engine process is left running after SIGTERM" "engine pid $(cat "$TMP/sig.pid") alive"; else ok "02-AC-16 no engine process is left running after SIGTERM"; fi
else
	bad "02-AC-16 SIGTERM to the wrapper reaches the engine client" "stub engine run never started"
fi
kill -KILL "$(cat "$TMP/sig.pid" 2>/dev/null)" "$(cat "$TMP/sig.child" 2>/dev/null)" 2>/dev/null
kill -KILL "$(cat "$TMP/wrap.pid" 2>/dev/null)" "$bg" 2>/dev/null
wait "$bg" 2>/dev/null

# Usage and repository errors (plan 02 7.1).
# (exec without --image exited 2 until F03 S3; toolbox is now the default image, see stub-s3-wrapper.sh)
mkdir -p "$TMP/notrepo"
cp -p "$fx/bootstrap" "$TMP/notrepo/bootstrap"
wrap "$TMP/notrepo" exec --image ci -- true
expect_eq "02 not a git repository exits 2" 2 "$RC"
expect_match "02 not a git repository reports WRAPPER_NOT_A_REPO" "WRAPPER_NOT_A_REPO" "$ERR$OUT"

# 02-AC-15 wrapper side: a failed build exits 3, runs nothing and leaves no tag.
: >"$STUB_STATE"
reset_log
STUB_BUILD_FAIL=1 wrap "$fx" --toolbox-build exec --image ci -- true
expect_eq "02-AC-15 failed build exits 3" 3 "$RC"
expect_match "02-AC-15 failed build reports WRAPPER_TOOLBOX_BUILD_FAILED" "WRAPPER_TOOLBOX_BUILD_FAILED" "$ERR$OUT"
if [ "$RC" -eq 3 ]; then
	expect_eq "02-AC-15 failed build runs no container" 0 "$(grep -Ec '^(run|container run) ' "$STUB_LOG" || true)"
	expect_eq "02-AC-15 failed build leaves no image tag" 0 "$(wc -l <"$STUB_STATE" | tr -d ' ')"
fi

finish
