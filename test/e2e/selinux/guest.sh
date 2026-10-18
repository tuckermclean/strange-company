#!/bin/sh
# Runs INSIDE the Fedora VM as the unprivileged user `sc`, from the repository copy (see test/e2e/selinux/run).
# 02-AC-5, rootless Podman with SELinux enforcing: asserts the environment, then runs the 02-AC-5 probe.
set -eu
cd "$(dirname "$0")/../../.."
echo "guest: $(. /etc/os-release && echo "$PRETTY_NAME"), kernel $(uname -r), user $(id -un) uid $(id -u)"
echo "guest: getenforce=$(getenforce)"
[ "$(getenforce)" = Enforcing ] || { echo "guest: SELinux is not enforcing" >&2; exit 1; }
echo "guest: podman $(podman --version), rootless=$(podman info --format '{{.Host.Security.Rootless}}')"
[ "$(id -u)" != 0 ] || { echo "guest: must not run as root" >&2; exit 1; }
SC_CONTAINER_ENGINE=podman SC_REQUIRE_SELINUX=1 exec sh test/e2e/wrapper/ac5-probe.sh
