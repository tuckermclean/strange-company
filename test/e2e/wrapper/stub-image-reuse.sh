#!/bin/sh
# 02-AC-17: image reuse and inputs id, with the stub engine.
. "$(dirname "$0")/lib.sh"
echo "stub-image-reuse (02-AC-17)"

fx=$TMP/clone1
mkrepo "$fx"
git clone -q "$fx" "$TMP/clone2"

id1=$(ref_inputs_id "$fx")
id2=$(ref_inputs_id "$TMP/clone2")
sanity "$id1" "$id2" "reference id equal across clones"
tag1=localhost/strange-company-ci:in-$(id12 "$id1")

# First run: nothing built yet, so exactly one build, tagged with the inputs id.
reset_log
wrap "$fx" exec --image ci -- true
expect_eq "02-AC-17 first exec exits 0" 0 "$RC"
expect_eq "02-AC-17 first exec records one build" 1 "$(build_calls)"
expect_match "02-AC-17 build tag is in-<inputs12> of the reference inputs id" "(^|[ =])$tag1( |\$)" "$(grep -E '^(build|image build|buildx build) ' "$STUB_LOG")"
expect_match "02-AC-17 build carries the full inputs id (label)" "$id1" "$(grep -E '^(build|image build|buildx build) ' "$STUB_LOG")"
expect_match "02-AC-17 run uses the built tag" "$tag1" "$(run_line)"

# Second run: reuse.
reset_log
wrap "$fx" exec --image ci -- true
expect_eq "02-AC-17 second exec exits 0" 0 "$RC"
expect_eq "02-AC-17 second exec exits 0 and records no build" "0:0" "$RC:$(build_calls)"
expect_match "02-AC-17 second exec ran a container" "." "$(run_line)"
expect_match "02-AC-17 second exec still runs the same tag" "$tag1" "$(run_line)"

# The other clone computes the same tag (fresh engine state).
: >"$STUB_STATE"
reset_log
wrap "$TMP/clone2" exec --image ci -- true
expect_match "02-AC-17 second clone builds the same tag" "$tag1" "$(grep -E '^(build|image build|buildx build) ' "$STUB_LOG")"

# Changes outside the inputs change nothing.
reset_log
echo more >>"$fx/plans/p.md"
echo more >>"$fx/src/main_test.go"
echo more >>"$fx/src/testdata/x"
echo more >>"$fx/toolbox/image.lock"
wrap "$fx" exec --image ci -- true
expect_eq "02-AC-17 plans/, _test.go, testdata and image.lock edits: exit 0, no build" "0:0" "$RC:$(build_calls)"
expect_match "02-AC-17 ... and still run the existing tag" "$tag1" "$(run_line)"
sanity "$id1" "$(ref_inputs_id "$fx")" "reference id unchanged by non-input edits"

# A change under toolbox/ changes the id and builds again.
reset_log
echo '# changed' >>"$fx/toolbox/Containerfile"
id3=$(ref_inputs_id "$fx")
[ "$id3" != "$id1" ] || bad "harness sanity: reference id changes with toolbox/"
wrap "$fx" exec --image ci -- true
expect_eq "02-AC-17 toolbox/ change records one build" 1 "$(build_calls)"
expect_match "02-AC-17 new build uses the new inputs12 tag" "localhost/strange-company-ci:in-$(id12 "$id3")" "$(grep -E '^(build|image build|buildx build) ' "$STUB_LOG")"

# --toolbox-build forces a build even when the tag exists (plan 02 14.2 6a, 02-AC-14).
reset_log
wrap "$fx" --toolbox-build exec --image ci -- true
expect_eq "02-AC-14 --toolbox-build forces a build when the image exists" 1 "$(build_calls)"
expect_match "02-AC-14 build targets the ci stage of toolbox/Containerfile" "--target[ =]ci" "$(grep -E '^(build|image build|buildx build) ' "$STUB_LOG")"
expect_match "02-AC-14 build uses toolbox/Containerfile" "toolbox/Containerfile" "$(grep -E '^(build|image build|buildx build) ' "$STUB_LOG")"

finish
