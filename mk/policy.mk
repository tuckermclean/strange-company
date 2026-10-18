# Policy check (plan 01 section 8, plan 22 section 18, plan 26 section 9.2). `sc-policy` (Go, built from source)
# validates every file under policy/ against its schema by kind. POLICY_DIR=<dir> checks another tree or a
# fixture instead of the committed policy.
.PHONY: policy-check

POLICY_DIR ?=

policy-check:
	@cd src && go run ./cmd/sc-policy check --root ..$(if $(POLICY_DIR), --dir "$(abspath $(POLICY_DIR))",)
