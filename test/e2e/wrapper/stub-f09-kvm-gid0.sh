#!/bin/sh
# F09 S9b review note (plan 02 section 7.1, 05-AC-6): `--kvm` never adds `--group-add 0`. A device whose group is root
# (gid 0) would hand the container user the root group, so the wrapper fails closed: WRAPPER_NO_KVM, exit 2, the error
# names the device and says its group is root, and no container is started. A non-root device group still works.
# A stand-in file cannot be chowned to gid 0 as a non-root user, so a `stat` shim in the stub PATH answers the %g query
# with $SC_STUB_GID (0 or another value) and leaves every other stat call to the real one.
. "$(dirname "$0")/lib-s3.sh"
echo "stub-f09-kvm-gid0 (05-AC-6 wrapper side, fail closed on a root-owned device group)"

cat >"$STUBBIN/stat" <<'EOT'
#!/bin/sh
case "$*" in
*%g*) echo "${SC_STUB_GID:-0}" ;;
*) exec /usr/bin/stat "$@" ;;
esac
EOT
chmod +x "$STUBBIN/stat"

fx=$TMP/repo
mkrepo "$fx"
dev=$TMP/fake-kvm
: >"$dev"

reset_log
ENGINE=docker wrapx "$fx" "SC_KVM_DEVICE=$dev SC_STUB_GID=0" exec --image ci --kvm -- true
expect_eq "docker --kvm, device gid 0: exit 2" 2 "$RC"
expect_match "docker --kvm, device gid 0: WRAPPER_NO_KVM" "WRAPPER_NO_KVM" "$ERR"
expect_match "docker --kvm, device gid 0: the error names the device" "$dev" "$ERR"
expect_match "docker --kvm, device gid 0: the error says the group is root" "root" "$ERR"
expect_eq "docker --kvm, device gid 0: no container is started" 0 "$(runs)"

reset_log
ENGINE=podman wrapx "$fx" "SC_KVM_DEVICE=$dev SC_STUB_GID=0" exec --image ci --kvm -- true
expect_eq "rootful podman --kvm, device gid 0: exit 2" 2 "$RC"
expect_match "rootful podman --kvm, device gid 0: WRAPPER_NO_KVM" "WRAPPER_NO_KVM" "$ERR"
expect_eq "rootful podman --kvm, device gid 0: no container is started" 0 "$(runs)"

reset_log
ENGINE=docker wrapx "$fx" "SC_KVM_DEVICE=$dev SC_STUB_GID=994" exec --image ci --kvm -- true
expect_eq "docker --kvm, device gid 994: exit 0" 0 "$RC"
expect_match "docker --kvm, device gid 994: --group-add 994" "--group-add[ =]994( |\$)" "$(run_line)"
expect_nomatch "docker --kvm, device gid 994: no --group-add 0" "--group-add[ =]0( |\$)" "$(run_line)"
finish
