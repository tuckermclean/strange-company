#!/bin/sh
# F09 S9b attempt 2 (stub engine, plan 02 section 7.1, plan 05 section 14.6, 05-AC-6, 05-AC-8): `--kvm` adds the device
# AND the device's group, so the unprivileged container user can open /dev/kvm. Docker and rootful Podman get
# `--group-add <gid of the device>`; rootless Podman (--userns=keep-id) gets `--group-add keep-groups`, because the
# host gid is not mapped into the user namespace. `--kvm` is for the ci image only and for `exec` only; a
# SC_KVM_DEVICE in the host:container form is refused. Every refusal is exit 2 and starts no container.
. "$(dirname "$0")/lib-s3.sh"
echo "stub-f09-kvm-group (05-AC-6 wrapper side, 05-AC-8 no silent fallback)"

fx=$TMP/repo
mkrepo "$fx"
dev=$TMP/fake-kvm
: >"$dev"
gid=$(stat -c %g "$dev")

reset_log
ENGINE=docker wrapx "$fx" "SC_KVM_DEVICE=$dev" exec --image ci --kvm -- true
expect_eq "docker --kvm: exit 0" 0 "$RC"
L=$(run_line)
expect_match "docker --kvm: --group-add <gid of the device>" "--group-add[ =]$gid( |\$)" "$L"
expect_nomatch "docker --kvm: no keep-groups" "keep-groups" "$L"
expect_match "docker --kvm: still --device <kvm device>" "--device[ =]$dev( |:|\$)" "$L"

reset_log
ENGINE=podman wrapx "$fx" "SC_KVM_DEVICE=$dev" exec --image ci --kvm -- true
expect_eq "rootful podman --kvm: exit 0" 0 "$RC"
L=$(run_line)
expect_match "rootful podman --kvm: --group-add <gid of the device>" "--group-add[ =]$gid( |\$)" "$L"
expect_nomatch "rootful podman --kvm: no keep-groups" "keep-groups" "$L"

reset_log
ENGINE=podman STUB_ROOTLESS=1 wrapx "$fx" "SC_KVM_DEVICE=$dev" exec --image ci --kvm -- true
expect_eq "rootless podman --kvm: exit 0" 0 "$RC"
L=$(run_line)
expect_match "rootless podman --kvm: --group-add keep-groups" "--group-add[ =]keep-groups( |\$)" "$L"
expect_match "rootless podman --kvm: keeps --userns=keep-id" "--userns[ =]keep-id( |\$)" "$L"
expect_match "rootless podman --kvm: still --device <kvm device>" "--device[ =]$dev( |:|\$)" "$L"

reset_log
wrapx "$fx" "SC_KVM_DEVICE=$dev" exec --image ci -- true
expect_eq "no --kvm: exit 0" 0 "$RC"
expect_nomatch "no --kvm: no --group-add" "--group-add" "$(run_line) x"

reset_log
wrapx "$fx" "SC_KVM_DEVICE=$dev" exec --image toolbox --kvm -- true
expect_eq "--kvm with --image toolbox: usage error, exit 2" 2 "$RC"
expect_match "--kvm with --image toolbox: the error says ci image only" "WRAPPER_USAGE.*ci|ci.*WRAPPER_USAGE" "$ERR"
expect_eq "--kvm with --image toolbox: no container is started" 0 "$(runs)"

reset_log
wrapx "$fx" "SC_KVM_DEVICE=$dev" exec --kvm -- true
expect_eq "--kvm with the default image (toolbox): exit 2" 2 "$RC"
expect_eq "--kvm with the default image: no container is started" 0 "$(runs)"

reset_log
wrapx "$fx" "SC_KVM_DEVICE=$dev" shell --kvm
expect_eq "shell --kvm: usage error, exit 2" 2 "$RC"
expect_eq "shell --kvm: no container is started" 0 "$(runs)"

reset_log
wrapx "$fx" "SC_KVM_DEVICE=$dev" shell --image ci --kvm
expect_eq "shell --image ci --kvm: usage error, exit 2" 2 "$RC"
expect_eq "shell --image ci --kvm: no container is started" 0 "$(runs)"

# The path exists, so only an explicit refusal of the host:container form can produce the exit 2.
colon=$TMP/fake-kvm:dev-kvm
: >"$colon"
reset_log
wrapx "$fx" "SC_KVM_DEVICE=$colon" exec --image ci --kvm -- true
expect_eq "SC_KVM_DEVICE host:container form: exit 2" 2 "$RC"
expect_match "SC_KVM_DEVICE host:container form: the error names SC_KVM_DEVICE" "SC_KVM_DEVICE" "$ERR"
expect_eq "SC_KVM_DEVICE host:container form: no container is started" 0 "$(runs)"
finish
