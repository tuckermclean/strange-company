#!/bin/sh
# Tier-E evidence for 02-AC-5, the engine matrix leg (plan 02 section 22): the literal criterion command, the owner and
# mode checks, `id -un`, `ssh -V`, and on an SELinux enforcing host the `ls -Z` labels of $REPO and ~/.ssh before versus
# after. NOT part of run.sh. Run by test/e2e/selinux/run inside the Fedora VM, and usable on any host:
#   SC_CONTAINER_ENGINE=podman sh test/e2e/wrapper/ac5-probe.sh
#   SC_REQUIRE_SELINUX=1 requires `getenforce` to print Enforcing (the Fedora leg sets it); without it a non-SELinux host
#   skips the label comparison.
# Needs the toolbox image (built by the first ./bootstrap exec when missing; ~15 min cold, ~10 GiB disk).
. "$(dirname "$0")/lib.sh"
echo "ac5-probe (02-AC-5) engine=${SC_CONTAINER_ENGINE:-auto}"

cd "$SC_ROOT" || exit 1
B=./bootstrap
unset SC_IN_CONTAINER SC_TOOLBOX_BUILD SC_TOOLBOX_REQUIRE_PULL
mkdir -p out
eng=${SC_CONTAINER_ENGINE:-$(command -v docker >/dev/null 2>&1 && echo docker || echo podman)}
export SC_CONTAINER_ENGINE=$eng
command -v "$eng" >/dev/null 2>&1 || { bad "$eng is installed"; finish; exit $?; }

selinux=0
if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce 2>/dev/null)" = Enforcing ]; then selinux=1; fi
if [ "${SC_REQUIRE_SELINUX:-}" = 1 ]; then
	expect_eq "02-AC-5 harness: SELinux is enforcing on this host" 1 "$selinux"
	[ "$selinux" = 1 ] || { finish; exit 1; }
fi
rootless=
if [ "$eng" = podman ]; then
	rootless=$(podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null || true)
	expect_eq "02-AC-5 harness: Podman runs rootless" true "$rootless"
fi
mkdir -p "$HOME/.ssh"
chmod 700 "$HOME/.ssh"
# A real default key, so that the key mount is exercised without any relabeling.
if [ ! -e "$HOME/.ssh/id_ed25519" ]; then
	ssh-keygen -q -t ed25519 -N "" -f "$HOME/.ssh/id_ed25519" >/dev/null 2>&1 || bad "02-AC-5 harness: a default key was created"
fi

# Build or reuse the image first, so the label snapshot below covers only the 02-AC-5 command itself.
RC=0
$B exec -- true >"$TMP/first.out" 2>&1 || RC=$?
expect_eq "02-AC-5 setup: ./bootstrap exec -- true exits 0" 0 "$RC"
[ "$RC" -eq 0 ] || { tail -n 20 "$TMP/first.out" | sed 's/^/       /'; finish; exit 1; }

labels() { ls -Zd "$SC_ROOT" "$HOME/.ssh" 2>&1; ls -Z "$SC_ROOT" "$HOME/.ssh" 2>&1; }
sel_before=
[ "$selinux" != 1 ] || sel_before=$(labels)

rm -f out/own-probe
RC=0
OUT=$($B exec -- sh -c 'touch $SC_REPO_ROOT/out/own-probe && stat -c "%a %u" /run/sc && id -un' 2>"$TMP/ac5.err") || RC=$?
expect_eq "02-AC-5 the literal command exits 0" 0 "$RC"
[ "$RC" -eq 0 ] || sed 's/^/       /' "$TMP/ac5.err"
if [ -e out/own-probe ]; then expect_eq "02-AC-5 host stat -c %u out/own-probe equals id -u" "$(id -u)" "$(stat -c %u out/own-probe)"; else bad "02-AC-5 out/own-probe was created"; fi
rm -f out/own-probe
cuid=$(id -u)
if [ "$eng" = docker ] && docker info --format '{{.SecurityOptions}}' 2>/dev/null | grep -q rootless; then cuid=0; fi
expect_eq "02-AC-5 /run/sc is mode 700 and owned by the container uid" "700 $cuid" "$(printf '%s\n' "$OUT" | sed -n 1p)"
case $(printf '%s\n' "$OUT" | sed -n 2p) in
sc-operator | "$(id -un)") ok "02-AC-5 id -un prints sc-operator (or the host user name under keep-id)" ;;
*) bad "02-AC-5 id -un prints sc-operator (or the host user name under keep-id)" "got [$(printf '%s\n' "$OUT" | sed -n 2p)]" ;;
esac
RC=0
$B exec -- ssh -V >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-5 ./bootstrap exec -- ssh -V exits 0" 0 "$RC"

if [ "$selinux" = 1 ]; then
	sel_after=$(labels)
	expect_eq "02-AC-5 SELinux: ls -Z labels of \$REPO and ~/.ssh are unchanged before versus after" "$sel_before" "$sel_after"
	expect_nomatch "02-AC-5 SELinux: the repository carries no container_file_t label (no :z relabel happened)" 'container_file_t' "$sel_after"
else
	echo "  SKIP 02-AC-5 SELinux label comparison (this host is not SELinux enforcing)"
fi
finish
