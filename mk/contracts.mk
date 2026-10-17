# Contract targets (plan 01 section 8.3). `sc-contracts` (Go, built from source) checks the schemas, the OpenAPI
# documents and the version-lock instances. Until slice F02-S2 moves the remaining instance globs into it, the
# Stage-0 venv step (F01, 01-AC-11) still validates `.strange-company/*.yaml` and `policy/ci.yaml`; both steps
# always run and either failing fails the target.
.PHONY: contracts

STAGE0_PYTHON := /opt/sc/stage0-venv/bin/python

contracts:
	@rc=0; \
	(cd src && go run ./cmd/sc-contracts --root ..) || rc=1; \
	$(STAGE0_PYTHON) -I test/contracts/validate_instances.py || rc=1; \
	exit $$rc
