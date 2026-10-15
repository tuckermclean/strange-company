# Tier V (plan 01 section 7.1): verify and the targets it runs, in order. Later F01 slices replace the
# placeholders and add targets here (layout-check S2, locks-check S3, test/build/... S4).
VERIFY_TARGETS := toolbox-check layout-check locks-check lint
.NOTPARALLEL:
.PHONY: verify $(VERIFY_TARGETS)

verify: $(VERIFY_TARGETS)
	@echo "verify: $(VERIFY_TARGETS): pass"

toolbox-check:
	@toolbox/bin/toolbox-check
	@sh test/e2e/wrapper/run.sh

layout-check:
	@echo "layout-check: not yet implemented (F01 slice S2)" >&2; exit 1

locks-check:
	@echo "locks-check: not yet implemented (F01 slice S3)" >&2; exit 1

SH_FILES := bootstrap toolbox/bin/sc-entrypoint
lint:
	@shellcheck $(SH_FILES) test/e2e/wrapper/*.sh
	@shfmt -d $(SH_FILES)
