#!/bin/sh
# Wrapper unit tests with the stub engine (plan 02 section 22, F01 subset) plus 01-AC-5.
# Needs git, sh and GNU make; no container engine, no network, no Go.
# Run by `make toolbox-check`; also runnable directly: sh test/e2e/wrapper/run.sh
d=$(cd "$(dirname "$0")" && pwd -P)
rc=0
for t in stub-image-reuse.sh stub-exec-contract.sh stub-unpinned-from.sh makefile-reexec.sh cache-env.sh verify-script.sh; do
	sh "$d/$t" || rc=1
done
[ "$rc" -eq 0 ] && echo "wrapper tests: PASS" || echo "wrapper tests: FAIL"
exit "$rc"
