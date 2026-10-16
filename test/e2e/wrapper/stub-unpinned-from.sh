#!/bin/sh
# Plan 02 02-R-1 / 14.2 / 14.9 / 21 with the stub engine: an unpinned base image is refused only when the wrapper is
# about to build, with the existing code WRAPPER_TOOLBOX_BUILD_FAILED (exit 3), and wrapper messages carry "bootstrap:".
# Criteria: 02-AC-14 (build path), 02-R-1.
. "$(dirname "$0")/lib.sh"
echo "stub-unpinned-from (02-R-1, 02-AC-14, plan 02 section 21)"

DIG=$(printf 'a%.0s' $(seq 1 64))
fixture() { # name Containerfile-body: a committed repo copy with that Containerfile
	_r=$TMP/$1
	mkrepo "$_r"
	printf '%s\n' "$2" >"$_r/toolbox/Containerfile"
	git_ -C "$_r" commit -qam containerfile
}
refuse_check() { # label repo expected-line expected-ref
	: >"$STUB_STATE"
	reset_log
	wrap "$2" exec --image ci -- true
	expect_eq "$1 exits 3" 3 "$RC"
	expect_match "$1 reports WRAPPER_TOOLBOX_BUILD_FAILED" "WRAPPER_TOOLBOX_BUILD_FAILED" "$ERR$OUT"
	expect_match "$1 names toolbox/Containerfile:$3" "toolbox/Containerfile:$3([^0-9]|\$)" "$ERR$OUT"
	expect_match "$1 names the FROM reference" "$4" "$ERR$OUT"
	expect_nomatch "$1 does not use WRAPPER_TOOLBOX_UNPINNED (not a plan code)" "WRAPPER_TOOLBOX_UNPINNED" "$ERR$OUT"
	expect_eq "$1 records no engine build call" 0 "$(build_calls)"
	expect_eq "$1 runs no container" 0 "$(grep -Ec '^(run|container run) ' "$STUB_LOG" || true)"
}

# (a) unpinned tag, no existing image.
fixture ua "# base
# pins matter
FROM docker.io/library/golang:1.26 AS ci"
refuse_check "02-R-1 unpinned FROM, no image" "$TMP/ua" 3 "docker.io/library/golang:1.26"

# (b) short digest is unpinned.
fixture ub "# base
# pins matter
FROM docker.io/library/golang@sha256:abc AS ci"
refuse_check "02-R-1 short digest @sha256:abc" "$TMP/ub" 3 "golang@sha256:abc"

# (b2) 63 and 65 hex characters are unpinned too.
h63=$(printf 'b%.0s' $(seq 1 63))
fixture ub2 "# base
# pins matter
FROM docker.io/library/golang@sha256:$h63 AS ci"
refuse_check "02-R-1 63-hex digest" "$TMP/ub2" 3 "sha256:$h63"
h65=$(printf 'b%.0s' $(seq 1 65))
fixture ub3 "# base
# pins matter
FROM docker.io/library/golang@sha256:$h65 AS ci"
refuse_check "02-R-1 65-hex digest" "$TMP/ub3" 3 "sha256:$h65"

# (c) unpinned FROM but the inputs-id image already exists: no refusal on a reusing exec.
tag=localhost/strange-company-ci:in-$(id12 "$(ref_inputs_id "$TMP/ua")")
printf '%s\t%s\n' "$tag" "$(ref_inputs_id "$TMP/ua")" >"$STUB_STATE"
reset_log
wrap "$TMP/ua" exec --image ci -- true
expect_eq "02-R-1 unpinned FROM with an existing image: exec exits 0" 0 "$RC"
expect_nomatch "02-R-1 existing image: no refusal message" "WRAPPER_TOOLBOX_(BUILD_FAILED|UNPINNED)" "$ERR$OUT x"
expect_eq "02-R-1 existing image: no build" 0 "$(build_calls)"
expect_match "02-R-1 existing image: the container ran" "^(run|container run) " "$(run_line)"

# (c2) ...but --toolbox-build is about to build, so it refuses.
reset_log
wrap "$TMP/ua" --toolbox-build exec --image ci -- true
expect_eq "02-R-1 unpinned FROM with --toolbox-build exits 3" 3 "$RC"
expect_match "02-R-1 --toolbox-build refusal is WRAPPER_TOOLBOX_BUILD_FAILED" "WRAPPER_TOOLBOX_BUILD_FAILED" "$ERR$OUT"
expect_eq "02-R-1 --toolbox-build refusal records no build call" 0 "$(build_calls)"

# (d) multi-stage: pinned stage, then FROM <stage>, plus scratch, builds.
fixture ud "# base
FROM docker.io/library/golang@sha256:$DIG AS x
FROM x AS y
FROM scratch AS z
FROM y AS ci"
: >"$STUB_STATE"
reset_log
wrap "$TMP/ud" exec --image ci -- true
expect_eq "02-R-1 multi-stage pinned/stage/scratch FROM exits 0" 0 "$RC"
expect_eq "02-R-1 multi-stage builds exactly once" 1 "$(build_calls)"
expect_nomatch "02-R-1 multi-stage: no refusal" "WRAPPER_TOOLBOX_(BUILD_FAILED|UNPINNED)" "$ERR$OUT x"

# Plan 02 section 21: the build message carries the bootstrap: prefix.
: >"$STUB_STATE"
reset_log
wrap "$TMP/ud" exec --image ci -- true
expect_match "plan 02 s21 build message is 'bootstrap: toolbox: building ...'" "^bootstrap: toolbox: building " "$ERR"

# 02-R-1 / plan 02 section 21 (S1 review L1): a missing or unreadable toolbox/Containerfile, with a build needed,
# is WRAPPER_TOOLBOX_BUILD_FAILED (exit 3) naming the file, never a silent exit 2.
fixture um "FROM scratch AS ci"
rm -f "$TMP/um/toolbox/Containerfile"
: >"$STUB_STATE"
reset_log
wrap "$TMP/um" exec --image ci -- true
expect_eq "02-R-1 missing Containerfile exits 3" 3 "$RC"
expect_match "02-R-1 missing Containerfile reports 'bootstrap: WRAPPER_TOOLBOX_BUILD_FAILED:'" "^bootstrap: WRAPPER_TOOLBOX_BUILD_FAILED: " "$ERR"
expect_match "02-R-1 missing Containerfile message names toolbox/Containerfile" "toolbox/Containerfile" "$ERR"
expect_eq "02-R-1 missing Containerfile records no build call" 0 "$(build_calls)"

if [ "$(id -u)" -ne 0 ]; then
	fixture uu "FROM scratch AS ci"
	chmod 000 "$TMP/uu/toolbox/Containerfile"
	: >"$STUB_STATE"
	reset_log
	wrap "$TMP/uu" exec --image ci -- true
	chmod 644 "$TMP/uu/toolbox/Containerfile"
	expect_eq "02-R-1 unreadable Containerfile exits 3" 3 "$RC"
	expect_match "02-R-1 unreadable Containerfile reports 'bootstrap: WRAPPER_TOOLBOX_BUILD_FAILED:'" "^bootstrap: WRAPPER_TOOLBOX_BUILD_FAILED: " "$ERR"
	expect_match "02-R-1 unreadable Containerfile message names toolbox/Containerfile" "toolbox/Containerfile" "$ERR"
	expect_eq "02-R-1 unreadable Containerfile records no build call" 0 "$(build_calls)"
fi

# 02-R-1 (S1 review L2): stage names are case-insensitive (Dockerfile stage names are lowercased), so
# FROM base after AS Base is a stage reference, not an unpinned image.
fixture uc "FROM docker.io/library/golang@sha256:$DIG AS Base
FROM base AS ci"
: >"$STUB_STATE"
reset_log
wrap "$TMP/uc" exec --image ci -- true
expect_eq "02-R-1 'AS Base' then 'FROM base' exits 0" 0 "$RC"
expect_nomatch "02-R-1 case-insensitive stage name: no refusal" "WRAPPER_TOOLBOX_(BUILD_FAILED|UNPINNED)" "$ERR$OUT x"
expect_eq "02-R-1 case-insensitive stage name builds exactly once" 1 "$(build_calls)"

finish
