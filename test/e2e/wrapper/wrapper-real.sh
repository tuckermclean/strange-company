#!/bin/sh
# Tier-E real-engine evidence for F03 S3: 02-AC-5 (local rootless Podman leg; the engine matrix is S4), 02-AC-6,
# 02-AC-7, 02-AC-10, 02-AC-11, 02-AC-13, with the criteria's literal commands (no --image: toolbox is the default).
# Needs Docker or Podman, network for the first build (~15 min cold), ~10 GiB free disk. NOT part of run.sh.
#   SC_CONTAINER_ENGINE=podman sh test/e2e/wrapper/wrapper-real.sh
#   SC_CONTAINER_ENGINE=docker sh test/e2e/wrapper/wrapper-real.sh
. "$(dirname "$0")/lib.sh"
echo "wrapper-real (02-AC-5, 02-AC-6, 02-AC-7, 02-AC-10, 02-AC-11, 02-AC-13) engine=${SC_CONTAINER_ENGINE:-auto}"

cd "$SC_ROOT" || exit 1
B=./bootstrap
unset SC_IN_CONTAINER SC_TOOLBOX_BUILD SC_TOOLBOX_REQUIRE_PULL
mkdir -p out
eng=${SC_CONTAINER_ENGINE:-$(command -v docker >/dev/null 2>&1 && echo docker || echo podman)}
export SC_CONTAINER_ENGINE=$eng
REAL=$(command -v "$eng") || { bad "$eng is installed"; finish; exit $?; }

# The wrapper builds the toolbox image when it is missing; everything below runs the criteria's literal commands.
RC=0
$B exec -- true >"$TMP/first.out" 2>&1 || RC=$?
expect_eq "setup: ./bootstrap exec -- true (default image toolbox) exits 0" 0 "$RC"
[ "$RC" -eq 0 ] || { tail -n 20 "$TMP/first.out" | sed 's/^/       /'; finish; exit 1; }

# --- 02-AC-5 (rootless Podman leg locally)
sel_before=
if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce 2>/dev/null)" = Enforcing ]; then
	sel_before=$(ls -Zd "$SC_ROOT" "$HOME/.ssh" 2>/dev/null)
fi
rm -f out/own-probe
RC=0
OUT=$($B exec -- sh -c 'touch $SC_REPO_ROOT/out/own-probe && stat -c "%a %u" /run/sc && id -un' 2>"$TMP/ac5.err") || RC=$?
expect_eq "02-AC-5 the literal command exits 0" 0 "$RC"
[ "$RC" -eq 0 ] || sed 's/^/       /' "$TMP/ac5.err"
if [ -e out/own-probe ]; then expect_eq "02-AC-5 host stat -c %u out/own-probe equals id -u" "$(id -u)" "$(stat -c %u out/own-probe)"; else bad "02-AC-5 out/own-probe was created"; fi
rm -f out/own-probe
mode_line=$(printf '%s\n' "$OUT" | sed -n 1p)
uname_line=$(printf '%s\n' "$OUT" | sed -n 2p)
cuid=$(id -u)
if [ "$eng" = docker ] && docker info --format '{{.SecurityOptions}}' 2>/dev/null | grep -q rootless; then cuid=0; fi
expect_eq "02-AC-5 /run/sc is mode 700 and owned by the container uid" "700 $cuid" "$mode_line"
case $uname_line in
sc-operator | "$(id -un)") ok "02-AC-5 id -un prints sc-operator (or the host user name under keep-id)" ;;
*) bad "02-AC-5 id -un prints sc-operator (or the host user name under keep-id)" "got [$uname_line]" ;;
esac
if [ -n "$sel_before" ]; then
	sel_after=$(ls -Zd "$SC_ROOT" "$HOME/.ssh" 2>/dev/null)
	expect_eq "02-AC-5 SELinux: ls -Z labels of \$REPO and ~/.ssh are unchanged" "$sel_before" "$sel_after"
else
	echo "  SKIP 02-AC-5 SELinux label comparison (host is not SELinux enforcing; the Fedora leg is S4)"
fi

# --- 02-AC-7 pipes and exit codes
RC=0
$B exec -- sh -c 'exit 7' >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-7 ./bootstrap exec -- sh -c 'exit 7'; echo \$? prints 7" 7 "$RC"
RC=0
OUT=$(echo hi | $B exec -- cat 2>"$TMP/ac7.err") || RC=$?
expect_eq "02-AC-7 echo hi | ./bootstrap exec -- cat prints hi" hi "$OUT"
expect_eq "02-AC-7 ... and exits 0" 0 "$RC"
expect_nomatch "02-AC-7 ... with no TTY error" "input device is not a TTY|[Nn]ot a tty" "$(cat "$TMP/ac7.err") x"

# --- 02-AC-13 no engine socket
RC=0
OUT=$($B exec -- sh -c 'ls /var/run/docker.sock /run/podman/podman.sock 2>/dev/null' 2>/dev/null) || true
expect_eq "02-AC-13 the literal command prints nothing" "" "$OUT"

# --- 02-AC-6 credentials are not exposed
C1=CANARY-S3SECRET-$$-7f3a91
C2=CANARY-PASSPHRASE-$$-b2c4d8
C3=CANARY-AGENT-$$-0e7d11
cat >"$TMP/canary.env" <<EOT
CLUSTER_NAME=canary
S3_ENDPOINT=https://s3.canary.invalid
S3_REGION=canary-1
S3_ACCESS_KEY_ID=$C3
S3_SECRET_ACCESS_KEY=$C1
RECOVERY_PASSPHRASE=$C2
EOT
chmod 600 "$TMP/canary.env"
SHIM=$TMP/shim
ARGLOG=$TMP/engine-args.log
mkdir -p "$SHIM"
: >"$ARGLOG"
for e in docker podman; do
	cat >"$SHIM/$e" <<EOT
#!/bin/sh
printf '%s\n' "\$*" >>"$ARGLOG"
exec "$REAL" "\$@"
EOT
	chmod +x "$SHIM/$e"
done
PATH="$SHIM:$PATH" $B --env-file "$TMP/canary.env" exec -- sleep 30 >"$TMP/sleep.out" 2>&1 &
bg=$!
name=
i=0
while [ -z "$name" ] && [ "$i" -lt 240 ]; do
	name=$("$REAL" ps --format '{{.Names}}' 2>/dev/null | grep '^sc-toolbox-' | head -n 1 || true)
	[ -n "$name" ] || sleep 0.5
	i=$((i + 1))
done
if [ -n "$name" ]; then
	"$REAL" inspect "$name" >"$TMP/inspect.json" 2>&1
	for c in "$C1" "$C2" "$C3"; do
		if grep -Fq -- "$c" "$TMP/inspect.json"; then bad "02-AC-6 $eng inspect output does not contain canary $c"; else ok "02-AC-6 $eng inspect output does not contain canary $c"; fi
		if grep -Fq -- "$c" "$ARGLOG"; then bad "02-AC-6 the engine argument log does not contain canary $c"; else ok "02-AC-6 the engine argument log does not contain canary $c"; fi
	done
	[ -s "$ARGLOG" ] && ok "02-AC-6 the engine argument log is not empty" || bad "02-AC-6 the engine argument log is not empty"
	"$REAL" kill "$name" >/dev/null 2>&1 || true
else
	bad "02-AC-6 the long-running ./bootstrap exec -- sleep 30 container started" "$(cat "$TMP/sleep.out")"
fi
kill "$bg" 2>/dev/null
wait "$bg" 2>/dev/null
RC=0
OUT=$($B --env-file "$TMP/canary.env" exec -- sh -c 'env' 2>/dev/null) || RC=$?
expect_eq "02-AC-6 ./bootstrap exec -- sh -c 'env' exits 0" 0 "$RC"
[ -n "$OUT" ] || bad "02-AC-6 env printed something"
for c in "$C1" "$C2" "$C3"; do
	if printf '%s\n' "$OUT" | grep -Fq -- "$c"; then bad "02-AC-6 env inside the container does not contain canary $c"; else ok "02-AC-6 env inside the container does not contain canary $c"; fi
done
# S3 review 2 N6: the s3 scope is active in this very run (so the canary check above is not satisfied vacuously).
expect_match "02-AC-6 AWS_CONFIG_FILE is set in the exec environment (the s3 scope is active)" '^AWS_CONFIG_FILE=/' "$OUT"
expect_match "02-AC-6 AWS_SHARED_CREDENTIALS_FILE is set in the exec environment" '^AWS_SHARED_CREDENTIALS_FILE=/' "$OUT"
RC=0
OUT=$($B --env-file "$TMP/canary.env" exec -- sh -c 'cat /run/sc-input/bootstrap.env' 2>/dev/null) || RC=$?
expect_match "02-AC-6 the env file itself is available to sc-bootstrap at /run/sc-input/bootstrap.env" "S3_SECRET_ACCESS_KEY=$C1" "$OUT"
RC=0
$B --env-file "$TMP/canary.env" exec -- sh -c ': >/run/sc-input/bootstrap.env' >/dev/null 2>&1 || RC=$?
[ "$RC" -ne 0 ] && ok "02 /run/sc-input/bootstrap.env is read-only" || bad "02 /run/sc-input/bootstrap.env is read-only" "write succeeded"

# --- 02-AC-11 env-file safety (literal command from the criterion)
rm -f /tmp/pwned
printf 'SSH_PRIVATE_KEY_PATH=$(touch /tmp/pwned)\n' >"$TMP/pwn.env"
RC=0
OUT=$($B --env-file "$TMP/pwn.env" preflight 2>&1) || RC=$?
expect_eq "02-AC-11 preflight with the fixture exits 3 (PF-06 fails)" 3 "$RC"
expect_match "02-AC-11 PF-06 fails and names the literal path" 'PF-06.*\$\(touch /tmp/pwned\)' "$OUT"
[ ! -e /tmp/pwned ] && ok "02-AC-11 /tmp/pwned does not exist on the host" || { bad "02-AC-11 /tmp/pwned does not exist on the host"; rm -f /tmp/pwned; }

# --- PF-06 with a key that exists on the host only (the container sees it as the mounted key; S1 review N1/N2/N4)
mkdir -p "$TMP/keys"
RC=0
$B exec -- sh -c 'rm -f "$SC_REPO_ROOT/out/s3-key" "$SC_REPO_ROOT/out/s3-key.pub"; ssh-keygen -q -t ed25519 -N "" -f "$SC_REPO_ROOT/out/s3-key" && cat "$SC_REPO_ROOT/out/s3-key"' >"$TMP/keys/k" 2>/dev/null || RC=$?
cp out/s3-key.pub "$TMP/keys/k.pub" 2>/dev/null
rm -f out/s3-key out/s3-key.pub
chmod 600 "$TMP/keys/k"
printf 'SSH_PRIVATE_KEY_PATH=%s\nSSH_PUBLIC_KEY_PATH=%s\n' "$TMP/keys/k" "$TMP/keys/k.pub" >"$TMP/key.env"
RC=0
OUT=$($B --env-file "$TMP/key.env" preflight 2>&1) || RC=$?
expect_match "PF-06 passes with a key that only the mount makes visible (sc-bootstrap runs with the container defaults)" "^PF-06 pass" "$OUT"
printf 'CLUSTER_NAME=x\n' >"$TMP/nokey.env"
RC=0
OUT=$(HOME=$TMP/nohome $B --env-file "$TMP/nokey.env" preflight 2>&1) || RC=$?
expect_nomatch "PF-06 output shows the key path as the operator writes it, never the container home /run/sc/home" "/run/sc/home" "$OUT"

# --- 02-AC-10 stale-lock detection: an image whose embedded lock differs from the checkout's lock
fx=$TMP/stale
mkdir -p "$fx"
git ls-files -z | tar --null -T - -cf - | tar -xf - -C "$fx"
git -C "$fx" init -q . && git -C "$fx" checkout -q -b main 2>/dev/null
printf '\n# changed after the image was built\n' >>"$fx/toolbox/tools.lock.yaml"
git -C "$fx" add -A && git_ -C "$fx" commit -q -m fixture
sid=$(ref_inputs_id "$fx")
stag=localhost/strange-company-toolbox:in-$(id12 "$sid")
ttag=localhost/strange-company-toolbox:in-$(id12 "$(ref_inputs_id "$SC_ROOT")")
cg=
if [ "$eng" = podman ] && [ ! -S "${XDG_RUNTIME_DIR:-/nonexistent}/bus" ]; then cg=--cgroup-manager=cgroupfs; fi
# The derived image is the checkout's good toolbox image under the fixture's inputs id: same content, older lock.
printf 'FROM %s\nLABEL strange.company/toolbox-inputs-id=%s\n' "$ttag" "$sid" >"$TMP/Containerfile.stale"
RC=0
# shellcheck disable=SC2086
"$eng" build $cg -t "$stag" -f "$TMP/Containerfile.stale" "$TMP" >"$TMP/stale-build.log" 2>&1 || RC=$?
expect_eq "02-AC-10 harness: the stale-lock image was derived" 0 "$RC"
if [ "$RC" -eq 0 ]; then
	RC=0
	OUT=$(cd "$fx" && ./bootstrap preflight 2>&1) || RC=$?
	expect_eq "02-AC-10 ./bootstrap preflight with an image built from a different tools.lock.yaml exits 3" 3 "$RC"
	expect_match "02-AC-10 ... printing PF-01 TOOLBOX_STALE" "PF-01 TOOLBOX_STALE" "$OUT"
	"$eng" rmi -f "$stag" >/dev/null 2>&1 || true
else
	tail -n 10 "$TMP/stale-build.log" | sed 's/^/       /'
fi

finish
