# Contract targets (plan 01 section 8.3). `sc-contracts` (Go, built from source) checks the schemas, the OpenAPI
# documents and every governed instance file of the section 8.3 glob table; test/contracts/crosscheck.py then
# recomputes the validation verdicts with Python jsonschema and requires them to match. Both always run; the
# target fails when either fails. A stale report is removed first so that a failed Go run cannot feed an old
# report to the crosscheck.
.PHONY: contracts

CONTRACTS_PYTHON ?= $(firstword $(wildcard /opt/sc/stage0-venv/bin/python) python3)

contracts:
	@rm -f out/contracts/report.json; \
	rc=0; (cd src && go run ./cmd/sc-contracts --root ..) || rc=1; \
	$(CONTRACTS_PYTHON) -I test/contracts/crosscheck.py . || rc=1; \
	exit $$rc
