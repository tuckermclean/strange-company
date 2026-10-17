#!/bin/sh
# F03 S3 image resolution with the stub engine (plan 02 section 14.2 steps 6(b), 6(c), 7; sections 15, 16):
# image.lock branch (b), pull retries, built-fallback, SC_TOOLBOX_REQUIRE_PULL, the OCI archive for run/expand/recover.
. "$(dirname "$0")/lib-s3.sh"
echo "stub-s3-resolve (02-R-1, 02-AC-4 wrapper side, plan 02 14.2 step 6-7, 15, 16)"

fx=$TMP/repo
mkrepo "$fx"
REPO=$(cd -P "$fx" && pwd -P)
id=$(ref_inputs_id "$fx")
btag=localhost/strange-company-toolbox:in-$(id12 "$id")
a64=$(printf '%064d' 0 | tr 0 a)
b64=$(printf '%064d' 0 | tr 0 b)
TREF=ghcr.io/example-org/strange-company-toolbox@sha256:$a64
CREF=ghcr.io/example-org/strange-company-ci@sha256:$b64
write_lock "$fx" "$id" "sha256:$a64" "sha256:$b64"
sanity "$id" "$(ref_inputs_id "$fx")" "image.lock edits do not change the inputs id"
builds() { grep -E '^(build|image build|buildx build) ' "$STUB_LOG" || true; }
fresh() { : >"$STUB_LOG"; : >"$STUB_STATE"; : >"$STUB_PULLED"; : >"$STUB_PULL_COUNT"; : >"$SLEEP_LOG"; }

# --- (b) matching inputsId and a non-empty digest: pull <image>@<digest>, source pulled, no build
fresh
wrapx "$fx" "" exec --image toolbox -- true
expect_eq "02 (b) matching image.lock: exit 0" 0 "$RC"
expect_match "02 (b) the pull names <image>@<digest>" "^(pull|image pull) .*$TREF( |\$)" "$(grep -E '^(pull|image pull) ' "$STUB_LOG" | head -n 1)"
expect_eq "02 (b) one pull attempt when it succeeds" 1 "$(pulls)"
expect_eq "02 (b) no build" 0 "$(build_calls)"
L=$(run_line)
expect_match "02 (b) the container runs <image>@<digest>" " $TREF( |\$)" "$L"
expect_match "02 (b) SC_TOOLBOX_SOURCE=pulled" "(-e|--env)[ =]SC_TOOLBOX_SOURCE=pulled( |\$)" "$L"
expect_match "02 (b) SC_TOOLBOX_IMAGE_REF=<image>@sha256:<digest>" "(-e|--env)[ =]SC_TOOLBOX_IMAGE_REF=$TREF( |\$)" "$L"

# the ci image uses ciImage/ciDigest
fresh
wrapx "$fx" "" exec --image ci -- true
expect_match "02 (b) --image ci pulls ciImage@ciDigest" "^(pull|image pull) .*$CREF( |\$)" "$(grep -E '^(pull|image pull) ' "$STUB_LOG" | head -n 1)"
expect_match "02 (b) --image ci runs ciImage@ciDigest" " $CREF( |\$)" "$(run_line)"

# image already local: no pull
fresh
echo "$TREF" >"$STUB_PULLED"
wrapx "$fx" "" exec --image toolbox -- true
expect_eq "02 (b) image present locally: no pull" 0 "$(pulls)"
expect_eq "02 (b) image present locally: no build" 0 "$(build_calls)"
expect_match "02 (b) image present locally: still runs <image>@<digest>" " $TREF( |\$)" "$(run_line)"

# --- pull retries: 3 attempts, waits of 10 s and 30 s (plan 02 section 16)
fresh
STUB_PULL_FAIL=2 wrapx "$fx" "" exec --image toolbox -- true
expect_eq "02 16 two failed pulls then success: exit 0" 0 "$RC"
expect_eq "02 16 three pull attempts" 3 "$(pulls)"
expect_eq "02 16 the waits between attempts are 10 s then 30 s" "10 30" "$(grep -E '^(10|30)$' "$SLEEP_LOG" | tr '\n' ' ' | sed 's/ $//')"
expect_eq "02 16 a late success does not build" 0 "$(build_calls)"
expect_match "02 16 a late success is still source pulled" "SC_TOOLBOX_SOURCE=pulled( |\$)" "$(run_line)"

# --- all attempts fail: built-fallback with a notice
fresh
STUB_PULL_FAIL=always wrapx "$fx" "" exec --image toolbox -- true
expect_eq "02 15 pull failing three times falls back to a build: exit 0" 0 "$RC"
expect_eq "02 15 exactly three pull attempts before the fallback" 3 "$(pulls)"
expect_eq "02 15 the fallback builds once" 1 "$(build_calls)"
expect_match "02 15 the fallback builds the local in-<inputs12> tag" "(^|[ =])$btag( |\$)" "$(builds)"
L=$(run_line)
expect_match "02 15 SC_TOOLBOX_SOURCE=built-fallback" "(-e|--env)[ =]SC_TOOLBOX_SOURCE=built-fallback( |\$)" "$L"
expect_match "02 15 the fallback runs the local tag" " $btag( |\$)" "$L"
expect_match "02 15 SC_TOOLBOX_IMAGE_REF is the local tag" "SC_TOOLBOX_IMAGE_REF=$btag(@[^ ]*)?( |\$)" "$L"
expect_match "02 15 stderr carries a notice about the failed pull" "[Pp]ull" "$ERR"

# --- SC_TOOLBOX_REQUIRE_PULL=1: no build fallback
fresh
STUB_PULL_FAIL=always wrapx "$fx" "SC_TOOLBOX_REQUIRE_PULL=1" exec --image toolbox -- true
expect_eq "02 15 SC_TOOLBOX_REQUIRE_PULL=1 and a failing pull exits 3" 3 "$RC"
expect_match "02 15 ... with WRAPPER_TOOLBOX_PULL_FAILED" "WRAPPER_TOOLBOX_PULL_FAILED" "$ERR"
expect_eq "02 15 ... without a build" 0 "$(build_calls)"
expect_eq "02 15 ... without running a container" 0 "$(runs)"
expect_eq "02 15 ... after three pull attempts" 3 "$(pulls)"
fresh
wrapx "$fx" "SC_TOOLBOX_REQUIRE_PULL=1" exec --image toolbox -- true
expect_eq "02 15 SC_TOOLBOX_REQUIRE_PULL=1 with a working pull exits 0" 0 "$RC"

# --- (a) --toolbox-build wins over a matching lock
fresh
wrapx "$fx" "" --toolbox-build exec --image toolbox -- true
expect_eq "02 (a) --toolbox-build with a matching image.lock: one build" 1 "$(build_calls)"
expect_eq "02 (a) --toolbox-build with a matching image.lock: no pull" 0 "$(pulls)"
expect_match "02 (a) source is built" "SC_TOOLBOX_SOURCE=built( |\$)" "$(run_line)"

# --- (c) inputsId differs (development checkout): local tag, no pull
write_lock "$fx" 4b825dc642cb6eb9a060e54bf8d69288fbee4904 "sha256:$a64" "sha256:$b64"
fresh
wrapx "$fx" "" exec --image toolbox -- true
expect_eq "02 (c) a different inputsId: no pull" 0 "$(pulls)"
expect_eq "02 (c) a different inputsId: one build of the local tag" 1 "$(build_calls)"
expect_match "02 (c) source is built" "SC_TOOLBOX_SOURCE=built( |\$)" "$(run_line)"
fresh
wrapx "$fx" "" exec --image toolbox -- true
wrapx "$fx" "" exec --image toolbox -- true
expect_eq "02 (c) the second run reuses the local tag" 1 "$(build_calls)"

# --- empty digests (before the first release): always build, never pull
write_lock "$fx" "$id" "" ""
fresh
wrapx "$fx" "" exec --image toolbox -- true
expect_eq "02 9.3 empty toolboxDigest: no pull" 0 "$(pulls)"
expect_eq "02 9.3 empty toolboxDigest: build" 1 "$(build_calls)"
expect_match "02 9.3 empty toolboxDigest: source built" "SC_TOOLBOX_SOURCE=built( |\$)" "$(run_line)"
write_lock "$fx" "$id" "sha256:$a64" "sha256:$b64"

# --- step 7: the OCI archive for run / expand / recover when the source is built* and the target toolbox
oci=$REPO/.bootstrap/toolbox/$id.oci.tar
write_lock "$fx" "" "" ""
for verb in run expand recover; do
	rm -rf "$REPO/.bootstrap"
	fresh
	ENGINE=podman STUB_ROOTLESS=1 wrapx "$fx" "" "$verb"
	expect_eq "02 14.2.7 $verb (built): exit 0" 0 "$RC"
	S=$(saves)
	expect_match "02 14.2.7 $verb (built, podman): the engine saves the image to .bootstrap/toolbox/<inputs-id>.oci.tar" "$oci" "$S"
	expect_match "02 14.2.7 $verb (built, podman): as an oci-archive" "oci-archive" "$S"
	expect_match "02 14.2.7 $verb (built, podman): the saved image is the local tag" "$btag" "$S"
	fresh
	ENGINE=podman STUB_ROOTLESS=1 wrapx "$fx" "" "$verb"
	expect_eq "02 14.2.7 $verb: the archive is not rewritten when it exists" 0 "$(printf '%s' "$(saves)" | grep -c .)"
done
rm -rf "$REPO/.bootstrap"
fresh
wrapx "$fx" "" run
S=$(saves)
expect_match "02 14.2.7 run (built, docker): docker save to the same path" "$oci" "$S"
expect_nomatch "02 14.2.7 run (built, docker): no oci-archive format flag" "oci-archive" "$S"
rm -rf "$REPO/.bootstrap"
fresh
wrapx "$fx" "" preflight
expect_eq "02 14.2.7 preflight does not save an archive" 0 "$(printf '%s' "$(saves)" | grep -c .)"
fresh
wrapx "$fx" "" exec -- true
expect_eq "02 14.2.7 exec does not save an archive" 0 "$(printf '%s' "$(saves)" | grep -c .)"
fresh
wrapx "$fx" "" exec --image ci -- true
expect_eq "02 14.2.7 the ci target saves no archive" 0 "$(printf '%s' "$(saves)" | grep -c .)"
write_lock "$fx" "$id" "sha256:$a64" "sha256:$b64"
fresh
wrapx "$fx" "" run
expect_eq "02 14.2.7 a pulled image needs no archive" 0 "$(printf '%s' "$(saves)" | grep -c .)"

finish
