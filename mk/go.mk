# Go-based targets (plan 01 section 7.1): layout-check, dag-check, locks-check, secrets-scan, quarantine-check.
.PHONY: layout-check dag-check locks-check secrets-scan quarantine-check

SC_LINT := cd src && go run ./cmd/sc-lint

layout-check:
	@$(SC_LINT) layout --root ..

dag-check:
	@$(SC_LINT) dag --root ..

locks-check:
	@$(SC_LINT) locks --root ..

secrets-scan:
	@$(SC_LINT) secrets --root ..

quarantine-check:
	@$(SC_LINT) quarantine --root ..
