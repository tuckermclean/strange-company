# Go-based targets (plan 01 section 7.1): layout-check, dag-check, test.
.PHONY: layout-check dag-check

SC_LINT := cd src && go run ./cmd/sc-lint

layout-check:
	@$(SC_LINT) layout --root ..

dag-check:
	@$(SC_LINT) dag --root ..
