#!/bin/sh
# Tier-E real-engine evidence for F03 S2: 02-AC-1, 02-AC-2, 02-AC-3 (build side), 02-AC-8, 02-AC-9.
# Needs Docker or Podman, network, jq, ~10 GiB free disk and ~15 min cold. NOT part of run.sh / the RED check.
# Run on Docker rootful (CI) and on rootless Podman:
#   SC_CONTAINER_ENGINE=podman sh test/e2e/wrapper/toolbox-image.sh
# Until S3 makes the toolbox image the wrapper default, the criteria's literal `./bootstrap exec -- ...` commands
# need `--image toolbox`; SC_IMG_FLAG is that flag and S3 sets SC_IMG_FLAG= (empty) to run the literal commands.
. "$(dirname "$0")/lib.sh"
echo "toolbox-image (02-AC-1, 02-AC-2, 02-AC-3, 02-AC-8, 02-AC-9) engine=${SC_CONTAINER_ENGINE:-auto}"

cd "$SC_ROOT" || exit 1
B=./bootstrap
unset SC_IN_CONTAINER
mkdir -p out
IMG=${SC_IMG_FLAG---image toolbox}
eng=${SC_CONTAINER_ENGINE:-$(command -v docker >/dev/null 2>&1 && echo docker || echo podman)}
inputs_id=$(ref_inputs_id "$SC_ROOT")
ttag=localhost/strange-company-toolbox:in-$(id12 "$inputs_id")
ctag=localhost/strange-company-ci:in-$(id12 "$inputs_id")
command -v jq >/dev/null 2>&1 || bad "jq is installed on the host (the criteria pipe into it)"

# --- 02-AC-1 make toolbox: both targets, --no-cache, linux/amd64
RC=0
make toolbox >"$TMP/make-toolbox.log" 2>&1 || RC=$?
expect_eq "02-AC-1 make toolbox exits 0" 0 "$RC"
[ "$RC" -eq 0 ] || tail -n 30 "$TMP/make-toolbox.log" | sed 's/^/       /'
# The wrapper builds the image it runs (--toolbox-build), tagged in-<inputs12>.
# shellcheck disable=SC2086 # IMG is empty or two words
RC=0
$B --toolbox-build exec $IMG -- sc-bootstrap toolbox selfcheck --json >"$TMP/selfcheck-toolbox.json" 2>"$TMP/sc.err" || RC=$?
expect_eq "02-AC-1 selfcheck --json in the toolbox image exits 0" 0 "$RC"
RC=0
jq -e 'all(.tools[]; .ok)' "$TMP/selfcheck-toolbox.json" >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-1 jq -e 'all(.tools[]; .ok)' exits 0 (toolbox)" 0 "$RC"
RC=0
jq -e '(.tools | length) > 0' "$TMP/selfcheck-toolbox.json" >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-1 the toolbox selfcheck lists at least one tool (not vacuous)" 0 "$RC"
RC=0
$B --toolbox-build exec --image ci -- sc-bootstrap toolbox selfcheck --json >"$TMP/selfcheck-ci.json" 2>"$TMP/sc.err" || RC=$?
expect_eq "02-AC-1 selfcheck --json in the ci image exits 0" 0 "$RC"
RC=0
jq -e 'all(.tools[]; .ok) and (.tools | length) > 0' "$TMP/selfcheck-ci.json" >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-1 all ci tool rows ok" 0 "$RC"
RC=0
jq -e '[.tools[].tool] | index("actionlint") != null and index("weed") != null' "$TMP/selfcheck-ci.json" >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-1 the ci selfcheck covers set: ci tools (image-info target: ci)" 0 "$RC"
RC=0
OUT=$($B exec $IMG -- cat /usr/share/strange-company/image-info.json 2>/dev/null) || RC=$?
expect_match "02-AC-1 toolbox image-info.json says target toolbox" '"target": *"toolbox"' "$OUT"
OUT=$($B exec --image ci -- cat /usr/share/strange-company/image-info.json 2>/dev/null) || true
expect_match "02-AC-1 ci image-info.json says target ci" '"target": *"ci"' "$OUT"

# --- 02-AC-2 the literal tool loop
# shellcheck disable=SC2086
RC=0
$B exec $IMG -- sh -c 'for t in tofu ansible-playbook kubectl helm flux sops age age-keygen ssh ssh-keygen ssh-keyscan curl jq yq git aws kubectl-cnpg etcdutl cmctl gitleaks cosign syft trivy sc-bootstrap; do command -v $t >/dev/null || exit 1; done' >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-2 every runtime tool is present in the toolbox image" 0 "$RC"
RC=0
$B exec --image ci -- sh -c 'for t in tofu ansible-playbook kubectl helm flux sops age age-keygen ssh ssh-keygen ssh-keyscan curl jq yq git aws kubectl-cnpg etcdutl cmctl gitleaks cosign syft trivy sc-bootstrap go actionlint woodpecker-cli kubeconform k3d promtool weed; do command -v $t >/dev/null || { echo "missing $t" >&2; exit 1; }; done' >/dev/null 2>"$TMP/err" || RC=$?
expect_eq "02-AC-2 runtime plus ci tools are present in the ci image" 0 "$RC"
[ "$RC" -eq 0 ] || sed 's/^/       /' "$TMP/err"
RC=0
$B exec --image ci -- sh -c 'helm plugin list | grep -q unittest && helm unittest --help >/dev/null' >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-2 helm unittest works in the ci image" 0 "$RC"
RC=0
$B exec --image ci -- sh -c 'test -n "$KUBEBUILDER_ASSETS" && test -x "$KUBEBUILDER_ASSETS/kube-apiserver" && test -x "$KUBEBUILDER_ASSETS/etcd"' >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-2 envtest assets are under \$KUBEBUILDER_ASSETS in the ci image" 0 "$RC"
RC=0
$B exec $IMG -- sh -c 'test "$AWS_REQUEST_CHECKSUM_CALCULATION" = when_required && test "$AWS_RESPONSE_CHECKSUM_VALIDATION" = when_required' >/dev/null 2>&1 || RC=$?
expect_eq "plan 02 s9.4 AWS checksum env defaults are set" 0 "$RC"

# --- 02-AC-3 a fixture lock with one wrong sha256: the build fails, nothing is tagged
fx=$TMP/ac3
mkdir -p "$fx"
git ls-files -z | tar --null -T - -cf - | tar -xf - -C "$fx"
git -C "$fx" init -q . && git -C "$fx" checkout -q -b main 2>/dev/null
awk '/^    - name: /{hit = ($3 == "jq")} hit && /amd64:/ && !done {sub(/"[0-9a-f]{64}"/, "\"" sprintf("%064d", 0) "\""); done = 1} {print}' \
	toolbox/tools.lock.yaml >"$fx/toolbox/tools.lock.yaml"
if cmp -s toolbox/tools.lock.yaml "$fx/toolbox/tools.lock.yaml"; then bad "harness: the fixture lock differs from the real lock (a jq entry with amd64 sha256 exists)"; fi
git -C "$fx" add -A && git_ -C "$fx" commit -q -m fixture
fid=$(ref_inputs_id "$fx")
ftag=localhost/strange-company-toolbox:in-$(id12 "$fid")
"$eng" image rm -f "$ftag" >/dev/null 2>&1 || true
RC=0
OUT=$(cd "$fx" && ./bootstrap --toolbox-build exec $IMG -- true 2>&1) || RC=$?
[ "$RC" -ne 0 ] && ok "02-AC-3 the build with a wrong sha256 fails" || bad "02-AC-3 the build with a wrong sha256 fails" "exit 0"
expect_match "02-AC-3 the failure names TOOLBOX_CHECKSUM_MISMATCH:jq" "TOOLBOX_CHECKSUM_MISMATCH:jq" "$OUT"
RC=0
"$eng" image inspect "$ftag" >/dev/null 2>&1 || RC=$?
[ "$RC" -ne 0 ] && ok "02-AC-3 no image is tagged $ftag after the failed build" || bad "02-AC-3 no image is tagged $ftag after the failed build"

# --- 02-AC-8 size budgets (uncompressed, amd64)
tsz=$("$eng" image inspect --format '{{.Size}}' "$ttag" 2>/dev/null || echo 0)
csz=$("$eng" image inspect --format '{{.Size}}' "$ctag" 2>/dev/null || echo 0)
printf '       toolbox=%s ci=%s bytes\n' "$tsz" "$csz"
[ "$tsz" -gt 0 ] && [ "$tsz" -le 1717986918 ] && ok "02-AC-8 toolbox image <= 1.6 GiB" || bad "02-AC-8 toolbox image <= 1.6 GiB" "size $tsz"
[ "$csz" -gt 0 ] && [ "$csz" -le 3758096384 ] && ok "02-AC-8 ci image <= 3.5 GiB" || bad "02-AC-8 ci image <= 3.5 GiB" "size $csz"

# --- 02-AC-9 gitleaks over the exported filesystem, and the image Env
for pair in "toolbox:$ttag" "ci:$ctag"; do
	nm=${pair%%:*}
	ref=${pair#*:}
	RC=0
	"$eng" image inspect --format '{{json .Config.Env}}' "$ref" >"$TMP/env-$nm.json" 2>/dev/null || RC=$?
	expect_eq "02-AC-9 $nm: image config readable" 0 "$RC"
	RC=0
	jq -e 'length > 0 and all(.[]; test("(secret|token|password|key)=";"i") | not)' "$TMP/env-$nm.json" >/dev/null 2>&1 || RC=$?
	expect_eq "02-AC-9 $nm: no Env entry matches (?i)(secret|token|password|key)=" 0 "$RC"
done
dir=$SC_ROOT/out/toolbox-fs
rm -rf "$dir"
mkdir -p "$dir"
cname=sc-toolbox-export-$$
"$eng" rm -f "$cname" >/dev/null 2>&1 || true
RC=0
"$eng" create --name "$cname" "$ttag" >/dev/null 2>&1 || RC=$?
if [ "$RC" -eq 0 ]; then
	"$eng" export "$cname" | tar -x --no-same-owner --exclude='dev/*' --exclude='proc/*' --exclude='sys/*' -C "$dir" 2>/dev/null || true
	"$eng" rm -f "$cname" >/dev/null 2>&1 || true
fi
[ -d "$dir/opt/sc" ] && ok "02-AC-9 the toolbox filesystem was exported" || bad "02-AC-9 the toolbox filesystem was exported" "no $dir/opt/sc"
# S2 review N1: the scan config is passed explicitly; the image must not carry its own /.gitleaks.toml.
[ ! -e "$dir/.gitleaks.toml" ] && ok "02-AC-9 the exported toolbox filesystem has no /.gitleaks.toml" || bad "02-AC-9 the exported toolbox filesystem has no /.gitleaks.toml" "found $dir/.gitleaks.toml"
RC=0
# shellcheck disable=SC2086
OUT=$($B exec $IMG -- gitleaks dir --no-banner --redact --exit-code 1 --config "$SC_ROOT/toolbox/gitleaks-image.toml" "$dir" 2>&1) || RC=$?
expect_eq "02-AC-9 gitleaks dir over the exported toolbox filesystem finds nothing" 0 "$RC"
[ "$RC" -eq 0 ] || printf '%s\n' "$OUT" | tail -n 15 | sed 's/^/       /'
rm -rf "$dir"

finish
