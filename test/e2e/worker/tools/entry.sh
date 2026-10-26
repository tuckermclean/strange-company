#!/bin/sh
# Entry point of the infra container (run with `sh /opt/l2/entry.sh`; mounts: /repo read-only, /opt/l2 read-only).
# Prepares the keys, the fixture and the packets, then runs sshd (port 22) and the stub Runner/Gateway.
set -eu
cd /state
ssh-keygen -q -t ed25519 -N '' -f /state/deploy-key -C sc-l2-deploy-key
ssh-keygen -q -t ed25519 -N '' -f /state/ssh_host_ed25519_key -C sc-l2-host-key
cp /state/deploy-key.pub /state/authorized_keys
printf 'forgejo-ssh.forgejo.svc.cluster.local %s\n' "$(cut -d' ' -f1,2 /state/ssh_host_ed25519_key.pub)" >/state/known_hosts
cd /repo/src/internal/worker
SC_L2_STATE=/state /opt/l2-bin/worker.test -test.run '^TestLayer2Prepare$' -test.count=1
mkdir -p /sc-e2e
cp -a /state/fixture/sc-e2e/. /sc-e2e/
chown -R git:git /sc-e2e
/usr/sbin/sshd -f /opt/l2/sshd_config -D -e &
SC_L2_STATE=/state exec /opt/l2-bin/worker.test -test.run '^TestLayer2Serve$' -test.count=1 -test.timeout 0
