#!/bin/sh
# Real-engine checks for the seed ci image and ./bootstrap exec (needs Docker or Podman, network, ~10 min cold).
# Not part of run.sh. Run on Docker rootful (CI) and separately on rootless Podman:
#   SC_CONTAINER_ENGINE=docker sh test/e2e/wrapper/real-engine.sh
#   SC_CONTAINER_ENGINE=podman sh test/e2e/wrapper/real-engine.sh
# Covers 02-AC-14, 02-AC-15 (build side), 02-AC-16.
. "$(dirname "$0")/lib.sh"
echo "real-engine (02-AC-14, 02-AC-15, 02-AC-16) engine=${SC_CONTAINER_ENGINE:-auto}"

cd "$SC_ROOT" || exit 1
B=./bootstrap
if [ ! -x "$B" ]; then bad "./bootstrap exists and is executable"; finish; exit $?; fi
unset SC_IN_CONTAINER
mkdir -p out

# --- 02-AC-14 seed build and version checks
RC=0
OUT=$($B --toolbox-build exec --image ci -- /opt/sc/bin/fetch-pinned --check 2>&1) || RC=$?
expect_eq "02-AC-14 fetch-pinned --check exits 0 after a build" 0 "$RC"
want=$(sed -n 's/^toolchain[[:space:]]*\(go[0-9][0-9.]*\).*/\1/p' src/go.mod | head -n 1)
[ -n "$want" ] || bad "src/go.mod has a toolchain directive"
RC=0
OUT=$($B exec --image ci -- go env GOVERSION 2>/dev/null) || RC=$?
expect_eq "02-AC-14 go env GOVERSION equals the src/go.mod toolchain" "$want" "$OUT"
OUT=$($B exec --image ci -- sh -c 'echo $SC_IN_CONTAINER' 2>/dev/null) || true
expect_eq "02-AC-14 SC_IN_CONTAINER=1 inside the image" 1 "$OUT"

# --- 02-AC-16 exec contract
RC=0
$B exec --image ci -- sh -c 'exit 7' >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-16 exit code 7 is returned" 7 "$RC"
RC=0
OUT=$(echo hi | $B exec --image ci -- cat 2>"$TMP/err") || RC=$?
expect_eq "02-AC-16 stdin pipe: prints hi" hi "$OUT"
expect_eq "02-AC-16 stdin pipe: exit 0" 0 "$RC"
expect_nomatch "02-AC-16 stdin pipe: no TTY error" "input device is not a TTY|[Nn]ot a tty" "$(cat "$TMP/err") x"
rm -f out/own-probe
$B exec --image ci -- sh -c 'touch "$SC_REPO_ROOT/out/own-probe"' >/dev/null 2>&1 || true
if [ -e out/own-probe ]; then expect_eq "02-AC-16 written file owned by the host user" "$(id -u)" "$(stat -c %u out/own-probe)"; else bad "02-AC-16 out/own-probe was created"; fi
rm -f out/own-probe
RC=0
OUT=$($B exec --image ci -- sh -c 'ls /var/run/docker.sock /run/podman/podman.sock 2>/dev/null; echo done' 2>/dev/null) || RC=$?
expect_eq "02-AC-16 no container socket visible (ls prints nothing)" "done" "$OUT"
RC=0
$B exec --image ci --network none -- curl -sS -m 5 https://proxy.golang.org >/dev/null 2>&1 || RC=$?
[ "$RC" -ne 0 ] && [ "$RC" -ne 127 ] && [ "$RC" -ne 2 ] && [ "$RC" -ne 3 ] && ok "02-AC-16 --network none: curl fails (exit $RC)" || bad "02-AC-16 --network none: curl fails with a curl error" "exit $RC"

# --- 02-AC-15 checksum enforcement (build side)
fx=$TMP/bad-lock
mkdir -p "$fx"
git ls-files -z | tar --null -T - -cf - | tar -xf - -C "$fx"
git -C "$fx" init -q . 2>/dev/null
perl -0pi -e 's/(sha256:\s*\{?\s*(?:[a-z0-9_]+:\s*)?"?)[0-9a-f]{64}/${1}0000000000000000000000000000000000000000000000000000000000000000/' "$fx/toolbox/tools.lock.yaml"
git -C "$fx" add -A >/dev/null 2>&1
git_ -C "$fx" commit -q -m fixture >/dev/null 2>&1 || true
if cmp -s "$SC_ROOT/toolbox/tools.lock.yaml" "$fx/toolbox/tools.lock.yaml"; then bad "fixture lock has a wrong sha256" "no sha256 entry mutated"; fi
rid=$(ref_inputs_id "$fx")
RC=0
(cd "$fx" && ./bootstrap --toolbox-build exec --image ci -- true) >"$TMP/b.out" 2>&1 || RC=$?
LOGS=$(cat "$TMP/b.out"; cat "$fx"/.bootstrap/toolbox/build-*.log 2>/dev/null || true)
[ "$RC" -ne 0 ] && ok "02-AC-15 build with a wrong sha256 fails" || bad "02-AC-15 build with a wrong sha256 fails" "exit 0"
expect_match "02-AC-15 output names TOOLBOX_CHECKSUM_MISMATCH:<tool>" "TOOLBOX_CHECKSUM_MISMATCH:[A-Za-z0-9._-]+" "$LOGS"
eng=${SC_CONTAINER_ENGINE:-$(command -v docker >/dev/null 2>&1 && echo docker || echo podman)}
tag=localhost/strange-company-ci:in-$(id12 "$rid")
if "$eng" image inspect "$tag" >/dev/null 2>&1; then bad "02-AC-15 no $tag image exists afterwards" "image present"; else ok "02-AC-15 no ci image tagged for the failed inputs id"; fi

finish
