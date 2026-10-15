# Contract targets (plan 01 section 8.3). F01 validates the repository's own instance files with the
# Stage-0 venv of the ci image; F02 replaces this with the Go sc-contracts validator.
.PHONY: contracts

STAGE0_PYTHON := /opt/sc/stage0-venv/bin/python

contracts:
	@$(STAGE0_PYTHON) -I test/contracts/validate_instances.py
