#!/bin/sh
# F09 S9b (stub engine, plan 02 section 7.1, plan 05 section 14.6, 05-AC-6): `./bootstrap exec --image ci --kvm` passes the
# host's /dev/kvm into the container (`--device <path>`); without --kvm no device is passed; --kvm on a host without
# the device fails with a clear WRAPPER_NO_KVM error (exit 2) and starts no container. The device path is
# $SC_KVM_DEVICE (default /dev/kvm) so the test can use a stand-in file.
. "$(dirname "$0")/lib-s3.sh"
echo "stub-f09-kvm (05-AC-6 wrapper side, 05-AC-8 no silent fallback)"

fx=$TMP/repo
mkrepo "$fx"
dev=$TMP/fake-kvm
: >"$dev"

reset_log
wrapx "$fx" "SC_KVM_DEVICE=$dev" exec --image ci --kvm --network host -- true
expect_eq "--kvm with the device present: exit 0" 0 "$RC"
L=$(run_line)
expect_match "--kvm passes --device <kvm device>" "--device[ =]$dev( |:|\$)" "$L"
expect_match "--kvm keeps --network host" "--network[ =]host( |\$)" "$L"
expect_match "--kvm keeps --cap-drop ALL" "--cap-drop[ =]ALL" "$L"
expect_nomatch "--kvm does not run privileged" "--privileged" "$L"
expect_eq "--kvm starts one container" 1 "$(runs)"

reset_log
wrapx "$fx" "SC_KVM_DEVICE=$dev" exec --kvm --image ci -- true
expect_eq "--kvm before --image: exit 0" 0 "$RC"
expect_match "--kvm before --image passes --device" "--device[ =]$dev( |:|\$)" "$(run_line)"

reset_log
wrapx "$fx" "SC_KVM_DEVICE=$dev" exec --image ci -- true
expect_eq "no --kvm: exit 0" 0 "$RC"
expect_nomatch "no --kvm: no --device" "--device" "$(run_line) x"

reset_log
wrapx "$fx" "SC_KVM_DEVICE=$TMP/absent-kvm" exec --image ci --kvm -- true
expect_eq "--kvm without the device: exit 2" 2 "$RC"
expect_match "--kvm without the device: a clear WRAPPER_NO_KVM error naming the device" "WRAPPER_NO_KVM.*absent-kvm|absent-kvm.*WRAPPER_NO_KVM" "$ERR"
expect_eq "--kvm without the device: no container is started" 0 "$(runs)"

# The default device is /dev/kvm.
reset_log
wrapx "$fx" "" exec --image ci --kvm -- true
if [ -e /dev/kvm ]; then
	expect_match "--kvm defaults to /dev/kvm" "--device[ =]/dev/kvm( |:|\$)" "$(run_line)"
else
	expect_eq "--kvm defaults to /dev/kvm (absent on this host): exit 2" 2 "$RC"
	expect_match "... and names /dev/kvm" "WRAPPER_NO_KVM.*/dev/kvm" "$ERR"
fi
finish
