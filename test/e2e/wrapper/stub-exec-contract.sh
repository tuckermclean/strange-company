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
expect_match "02-AC-16 docker rootful: --user uid:gid" "--user $uid:$gid( |\$)" "$L"
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
expect_match "02-AC-16 docker-rootless: --user 0:0" "--user 0:0( |\$)" "$(run_line)"

# Podman rootless, selected by the --engine flag.
reset_log
STUB_ROOTLESS=1 wrap "$fx" --engine podman exec --image ci -- true
L=$(run_line)
expect_match "02-AC-16 podman-rootless: --userns=keep-id" "--userns[ =]keep-id" "$L"
expect_nomatch "02-AC-16 podman-rootless: no --user uid:gid" "--user $uid:$gid" "$L"
expect_match "02-AC-16 podman-rootless: /run/sc tmpfs owned by the container user (U)" "--tmpfs[ =]/run/sc:[^ ]*(U|uid=)" "$L"

# Usage and repository errors (plan 02 7.1).
reset_log
wrap "$fx" exec -- true
expect_eq "02 exec without --image ci exits 2 (usage)" 2 "$RC"
[ "$RC" -ne 2 ] || expect_eq "02 exec without --image ci runs no container" 0 "$(grep -Ec '^(run|container run) ' "$STUB_LOG" || true)"
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
