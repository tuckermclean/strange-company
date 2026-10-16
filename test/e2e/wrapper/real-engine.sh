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
# The wrapper has no --no-cache flag (plan 02 7.1), so the criterion's "after a --no-cache build" is encoded by
# building the ci image with the engine's own --no-cache, tagged and labelled exactly as the wrapper's image-reuse
# lookup expects (plan 02 14.2). The wrapper then finds it and runs on it without rebuilding (asserted below).
eng=${SC_CONTAINER_ENGINE:-$(command -v docker >/dev/null 2>&1 && echo docker || echo podman)}
inputs_id=$(ref_inputs_id "$SC_ROOT")
nc_tag=localhost/strange-company-ci:in-$(id12 "$inputs_id")
cg=
if [ "$eng" = podman ] && [ ! -S "${XDG_RUNTIME_DIR:-/nonexistent}/bus" ]; then cg=--cgroup-manager=cgroupfs; fi
RC=0
# shellcheck disable=SC2086 # cg is empty or one flag
"$eng" build $cg --no-cache --target ci -f toolbox/Containerfile -t "$nc_tag" \
	--label "strange.company/toolbox-inputs-id=$inputs_id" --build-arg "SC_INPUTS_ID=$inputs_id" . >"$TMP/nc-build.log" 2>&1 || RC=$?
expect_eq "02-AC-14 engine build --no-cache --target ci succeeds" 0 "$RC"
tail -n 20 "$TMP/nc-build.log" | sed 's/^/       /' | { [ "$RC" -eq 0 ] || cat; }
RC=0
OUT=$($B exec --image ci -- /opt/sc/bin/fetch-pinned --check 2>&1) || RC=$?
expect_eq "02-AC-14 fetch-pinned --check exits 0 after the --no-cache build" 0 "$RC"
expect_nomatch "02-AC-14 the wrapper reused the --no-cache image (no rebuild)" "toolbox: building" "$OUT x"
expect_nomatch "02-AC-14 reuse run printed no bootstrap: toolbox: building either" "^bootstrap: toolbox: building " "$OUT x"
# The criterion's literal command: the wrapper itself builds (--toolbox-build), then fetch-pinned --check exits 0.
RC=0
OUT=$($B --toolbox-build exec --image ci -- /opt/sc/bin/fetch-pinned --check 2>&1) || RC=$?
expect_eq "02-AC-14 ./bootstrap --toolbox-build exec --image ci -- /opt/sc/bin/fetch-pinned --check exits 0" 0 "$RC"
expect_match "02-AC-14 stderr shows the wrapper built (bootstrap: toolbox: building)" "bootstrap: toolbox: building " "$OUT"
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

# --- 01-AC-6 / 52-AC-5 go caches default to the repo-local .cache in every ci container (plan 01 8.1)
RC=0
OUT=$($B exec --image ci -- go env GOMODCACHE GOCACHE 2>/dev/null) || RC=$?
expect_eq "01-AC-6 go env GOMODCACHE GOCACHE print the repo-local .cache paths" "$SC_ROOT/.cache/go/mod
$SC_ROOT/.cache/go/build" "$OUT"
OUT=$($B exec --image ci -- env GOMODCACHE=/tmp/m GOCACHE=/tmp/b go env GOMODCACHE GOCACHE 2>/dev/null) || true
expect_eq "01-AC-6 a caller override of the go caches wins" "/tmp/m
/tmp/b" "$OUT"

# --- 02-AC-15 checksum enforcement (build side)
fx=$TMP/bad-lock
mkdir -p "$fx"
git ls-files -z | tar --null -T - -cf - | tar -xf - -C "$fx"
# Corrupt every sha256 value of spec.tools[0] (the first entry under "tools:"), whatever else the lock holds.
cat >"$TMP/mutate.py" <<'EOP'
import re, sys
path = sys.argv[1]
lines = open(path).read().split("\n")
i = next(k for k, l in enumerate(lines) if re.match(r"^  tools:\s*$", l))
start = next(k for k in range(i + 1, len(lines)) if re.match(r"^\s+- name:", lines[k]))
indent = len(re.match(r"^(\s+)- name:", lines[start]).group(1))
name = re.match(r"^\s+- name:\s*[\"']?([^\"'\s#]+)", lines[start]).group(1)
end = start + 1
while end < len(lines) and not re.match(r"^\s{0,%d}\S" % indent, lines[end]):
    end += 1
n = 0
for k in range(start, end):
    lines[k], c = re.subn(r"(?<![0-9a-f])[0-9a-f]{64}(?![0-9a-f])", "0" * 64, lines[k])
    n += c
if n == 0:
    sys.exit("no sha256 in tools[0]")
open(path, "w").write("\n".join(lines))
print(name)
EOP
tool=$(python3 -I "$TMP/mutate.py" "$fx/toolbox/tools.lock.yaml") || tool=
[ -n "$tool" ] || bad "fixture lock: tools[0] sha256 mutated"
git -C "$fx" init -q . 2>/dev/null
git -C "$fx" add -A >/dev/null 2>&1
git_ -C "$fx" commit -q -m fixture >/dev/null 2>&1 || true
if cmp -s "$SC_ROOT/toolbox/tools.lock.yaml" "$fx/toolbox/tools.lock.yaml"; then bad "fixture lock has a wrong sha256" "no sha256 entry mutated"; fi
rid=$(ref_inputs_id "$fx")
RC=0
(cd "$fx" && ./bootstrap --toolbox-build exec --image ci -- true) >"$TMP/b.out" 2>&1 || RC=$?
LOGS=$(cat "$TMP/b.out"; cat "$fx"/.bootstrap/toolbox/build-*.log 2>/dev/null || true)
[ "$RC" -ne 0 ] && ok "02-AC-15 build with a wrong sha256 fails" || bad "02-AC-15 build with a wrong sha256 fails" "exit 0"
expect_match "02-AC-15 output names TOOLBOX_CHECKSUM_MISMATCH:$tool" "TOOLBOX_CHECKSUM_MISMATCH:$tool([^A-Za-z0-9._-]|\$)" "$LOGS"
tag=localhost/strange-company-ci:in-$(id12 "$rid")
if "$eng" image inspect "$tag" >/dev/null 2>&1; then bad "02-AC-15 no $tag image exists afterwards" "image present"; else ok "02-AC-15 no ci image tagged for the failed inputs id"; fi

finish
