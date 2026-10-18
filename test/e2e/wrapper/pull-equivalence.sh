#!/bin/sh
# Tier-E evidence for 02-AC-4 (ADR-0013, F03 decision D3): pull/build equivalence with a local OCI registry standing in
# for GHCR. NOT part of run.sh. Needs Docker or Podman, jq, curl, network (registry image, first build), ~15 GiB free
# disk and about 30 to 45 minutes cold (two builds, the second with --no-cache). Run it on any engine:
#   SC_CONTAINER_ENGINE=docker sh test/e2e/wrapper/pull-equivalence.sh
#   SC_CONTAINER_ENGINE=podman sh test/e2e/wrapper/pull-equivalence.sh
# What it does, in a temporary copy of the repository (the real toolbox/image.lock is never touched):
#   1. runs `./bootstrap --toolbox-build exec -- ...` and records the selfcheck tool rows and sha256 of sc-bootstrap;
#   2. builds the toolbox image again, independently, with --no-cache, and pushes it to a local registry;
#   3. writes the fixture toolbox/image.lock (inputsId = this checkout's, digest = the pushed one), removes every local
#      copy of the image, and runs `SC_TOOLBOX_REQUIRE_PULL=1 ./bootstrap exec -- ...` (so the wrapper must pull);
#   4. compares: the selfcheck --json tool rows and `sha256sum /opt/sc/bin/sc-bootstrap` must be identical.
# Pinned external artifact: the registry is the CNCF Distribution image docker.io/library/registry:3.0.0, by index
# digest (read from the Docker Hub registry API, tag 3.0.0, 2026-10-17). Docker treats a 127.0.0.0/8 registry as
# insecure by default; for Podman the script writes a registries.conf fragment (CONTAINERS_REGISTRIES_CONF) that marks
# localhost:<port> insecure and also pushes with --tls-verify=false.
#   SC_REGISTRY_PORT (default 5055), SC_EQUIV_KEEP=1 keeps the registry and fixture for debugging.
. "$(dirname "$0")/lib.sh"
echo "pull-equivalence (02-AC-4) engine=${SC_CONTAINER_ENGINE:-auto}"

REGISTRY_IMAGE=docker.io/library/registry@sha256:6c5666b861f3505b116bb9aa9b25175e71210414bd010d92035ff64018f9457e
port=${SC_REGISTRY_PORT:-5055}
eng=${SC_CONTAINER_ENGINE:-$(command -v docker >/dev/null 2>&1 && echo docker || echo podman)}
export SC_CONTAINER_ENGINE=$eng
command -v "$eng" >/dev/null 2>&1 || { bad "$eng is installed"; finish; exit $?; }
for t in jq curl git tar; do command -v "$t" >/dev/null 2>&1 || bad "$t is installed"; done
[ "$T_FAILS" -eq 0 ] || { finish; exit 1; }
unset SC_IN_CONTAINER SC_TOOLBOX_BUILD SC_TOOLBOX_REQUIRE_PULL
cd "$SC_ROOT" || exit 1

reg_name=sc-equiv-registry-$$
regdir=$TMP/registry
mkdir -p "$regdir"
chmod 777 "$regdir"
if [ "$eng" = podman ]; then
	cat >"$TMP/registries.conf" <<EOT
[[registry]]
location = "localhost:$port"
insecure = true
EOT
	export CONTAINERS_REGISTRIES_CONF=$TMP/registries.conf
	tlsflag=--tls-verify=false
else
	tlsflag=
fi
cleanup() {
	[ "${SC_EQUIV_KEEP:-}" = 1 ] || {
		"$eng" rm -f "$reg_name" >/dev/null 2>&1 || true
		for r in $created_refs; do "$eng" rmi -f "$r" >/dev/null 2>&1 || true; done
	}
}
created_refs=
trap 'cleanup; rm -rf "$TMP"' EXIT INT TERM

# --- the local registry (stands in for GHCR)
secopt=
[ "$eng" != podman ] || secopt="--security-opt label=disable"
# shellcheck disable=SC2086
RC=0
"$eng" run -d --rm --name "$reg_name" $secopt -p "127.0.0.1:$port:5000" -v "$regdir:/var/lib/registry" "$REGISTRY_IMAGE" >"$TMP/reg.out" 2>&1 || RC=$?
expect_eq "02-AC-4 harness: the local registry container started" 0 "$RC"
[ "$RC" -eq 0 ] || { sed 's/^/       /' "$TMP/reg.out"; finish; exit 1; }
i=0
until curl -fsS "http://127.0.0.1:$port/v2/" >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -lt 60 ] || { bad "02-AC-4 harness: the registry answers on 127.0.0.1:$port"; finish; exit 1; }
	sleep 1
done
ok "02-AC-4 harness: the registry answers on 127.0.0.1:$port"

# --- a temporary copy of the repository (tracked and untracked, not ignored files) with its own Git history
fx=$TMP/fx
mkdir -p "$fx"
git ls-files -co --exclude-standard -z | tar --null -T - -cf - | tar -xf - -C "$fx"
git -C "$fx" init -q . && git -C "$fx" checkout -q -b main 2>/dev/null
git_ -C "$fx" add -A && git_ -C "$fx" commit -q -m fixture
id=$(ref_inputs_id "$SC_ROOT")
expect_eq "02-AC-4 harness: the copy has the same inputs id as this checkout" "$id" "$(ref_inputs_id "$fx")"
tag=localhost/strange-company-toolbox:in-$(id12 "$id")
repo_name=localhost:$port/strange-company-toolbox
created_refs="$repo_name:equiv $tag"

# rows FILE: the selfcheck --json tool rows, canonical
rows() { jq -S -c '.tools' "$1"; }

# --- 1. the --toolbox-build image
RC=0
# One --toolbox-build exec collects both the selfcheck rows and the sha256, so a fixture lock naming a published digest
# can never turn the "built" side into a pull.
(cd "$fx" && ./bootstrap --toolbox-build exec -- sh -c 'sc-bootstrap toolbox selfcheck --json || exit $?; sha256sum /opt/sc/bin/sc-bootstrap >&2') >"$TMP/built.json" 2>"$TMP/built.err" || RC=$?
expect_eq "02-AC-4 the --toolbox-build image: selfcheck --json exits 0" 0 "$RC"
[ "$RC" -eq 0 ] || { sed 's/^/       /' "$TMP/built.err" | tail -n 15; finish; exit 1; }
RC=0
jq -e '(.tools | length) > 0 and all(.tools[]; .ok)' "$TMP/built.json" >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-4 the --toolbox-build image: non-empty tool rows, all ok" 0 "$RC"
grep -E '^[0-9a-f]{64}  /opt/sc/bin/sc-bootstrap$' "$TMP/built.err" >"$TMP/built.sha" || true
expect_match "02-AC-4 the --toolbox-build image: sha256sum of sc-bootstrap printed" '^[0-9a-f]{64}  /opt/sc/bin/sc-bootstrap$' "$(cat "$TMP/built.sha")"

# --- 2. an independent --no-cache build, pushed to the registry
RC=0
(cd "$fx" && ./bootstrap build --no-cache --target toolbox) >"$TMP/nocache.log" 2>&1 || RC=$?
expect_eq "02-AC-4 the independent --no-cache build exits 0" 0 "$RC"
[ "$RC" -eq 0 ] || { tail -n 15 "$TMP/nocache.log" | sed 's/^/       /'; finish; exit 1; }
"$eng" tag "$tag" "$repo_name:equiv"
RC=0
# shellcheck disable=SC2086
"$eng" push $tlsflag "$repo_name:equiv" >"$TMP/push.log" 2>&1 || RC=$?
expect_eq "02-AC-4 the image is pushed to the local registry" 0 "$RC"
[ "$RC" -eq 0 ] || { tail -n 15 "$TMP/push.log" | sed 's/^/       /'; finish; exit 1; }
digest=$(curl -fsSI -H 'Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json' \
	"http://127.0.0.1:$port/v2/strange-company-toolbox/manifests/equiv" | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: *//p')
expect_match "02-AC-4 the registry reports the pushed manifest digest" '^sha256:[0-9a-f]{64}$' "$digest"
[ -n "$digest" ] || { finish; exit 1; }

# --- 3. the fixture lock names the pushed digest and the current inputs id; no local copy remains, so the wrapper must pull
cat >"$fx/toolbox/image.lock" <<EOT
apiVersion: strange.company/v1alpha1
kind: ToolboxImageLock
inputsId: $id
toolboxImage: $repo_name
toolboxDigest: $digest
ciImage: ""
ciDigest: ""
platforms: linux/amd64
EOT
for r in "$repo_name:equiv" "$tag"; do "$eng" rmi -f "$r" >/dev/null 2>&1 || true; done
if "$eng" image inspect "$repo_name@$digest" >/dev/null 2>&1; then
	bad "02-AC-4 harness: no local copy of $repo_name@$digest remains before the pull"
else
	ok "02-AC-4 harness: no local copy of $repo_name@$digest remains before the pull"
fi
if "$eng" image inspect "$tag" >/dev/null 2>&1; then
	bad "02-AC-4 harness: no local build tag remains (a build would hide a failed pull)"
else
	ok "02-AC-4 harness: no local build tag remains (a build would hide a failed pull)"
fi
created_refs="$created_refs $repo_name@$digest"

# --- the wrapper pulls it
RC=0
OUT=$(cd "$fx" && SC_TOOLBOX_REQUIRE_PULL=1 ./bootstrap exec -- sh -c 'echo "$SC_TOOLBOX_SOURCE $SC_TOOLBOX_IMAGE_REF"' 2>"$TMP/pull.err") || RC=$?
expect_eq "02-AC-4 SC_TOOLBOX_REQUIRE_PULL=1 ./bootstrap exec -- ... exits 0 (the pull worked)" 0 "$RC"
[ "$RC" -eq 0 ] || sed 's/^/       /' "$TMP/pull.err" | tail -n 15
expect_eq "02-AC-4 SC_TOOLBOX_SOURCE=pulled and the image ref is <registry image>@<digest>" "pulled $repo_name@$digest" "$OUT"
if "$eng" image inspect "$tag" >/dev/null 2>&1; then bad "02-AC-4 the wrapper did not build a local image"; else ok "02-AC-4 the wrapper did not build a local image"; fi

# --- 4. equivalence
RC=0
(cd "$fx" && SC_TOOLBOX_REQUIRE_PULL=1 ./bootstrap exec -- sc-bootstrap toolbox selfcheck --json) >"$TMP/pulled.json" 2>"$TMP/pulled.err" || RC=$?
expect_eq "02-AC-4 the pulled image: selfcheck --json exits 0" 0 "$RC"
(cd "$fx" && SC_TOOLBOX_REQUIRE_PULL=1 ./bootstrap exec -- sha256sum /opt/sc/bin/sc-bootstrap) >"$TMP/pulled.sha" 2>/dev/null
expect_eq "02-AC-4 selfcheck --json tool rows are identical (pulled vs --toolbox-build)" "$(rows "$TMP/built.json")" "$(rows "$TMP/pulled.json")"
RC=0
jq -e '(.tools | length) > 0' "$TMP/pulled.json" >/dev/null 2>&1 || RC=$?
expect_eq "02-AC-4 the pulled image lists tools (the comparison is not vacuous)" 0 "$RC"
expect_eq "02-AC-4 sha256sum /opt/sc/bin/sc-bootstrap is identical (pulled vs --toolbox-build)" "$(cat "$TMP/built.sha")" "$(cat "$TMP/pulled.sha")"

finish
