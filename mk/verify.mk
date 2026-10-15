# Tier V (plan 01 section 7.1): verify and the targets it runs, in order. Later F01 slices add targets here
# (test/build/... S4).
VERIFY_TARGETS := toolbox-check layout-check dag-check contracts locks-check secrets-scan quarantine-check lint
.NOTPARALLEL:
.PHONY: verify $(VERIFY_TARGETS)

verify: $(VERIFY_TARGETS)
	@echo "verify: $(VERIFY_TARGETS): pass"

toolbox-check:
	@toolbox/bin/toolbox-check
	@sh test/e2e/wrapper/run.sh

SH_FILES := bootstrap toolbox/bin/sc-entrypoint
lint:
	@shellcheck $(SH_FILES)
	@shfmt -d $(SH_FILES)
	@actionlint .github/workflows/*.yaml
