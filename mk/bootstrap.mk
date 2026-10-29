# Bootstrap configuration documentation (plan 50 section 9.2, 50-AC-12): bootstrap.env.example is generated from the
# variable table in src/internal/bootstrap/config/vars.go; config-docs-check fails when the committed file drifts.
.PHONY: config-docs-check config-docs-generate
config-docs-check:
	@cd src && go run ./cmd/sc-lint config-docs --root ..

config-docs-generate:
	@cd src && go run ./cmd/sc-lint config-docs --root .. --write

# Tier E (plan 01 section 7.1, plan 50): the bootstrap orchestrator against the fake provider and the S3 emulator, with
# crash injection at every journal point (50-AC-4, 50-AC-9, 50-AC-19). JUnit lands in out/e2e/.
.PHONY: e2e-bootstrap-fake
e2e-bootstrap-fake:
	@$(MAKE) --no-print-directory test GO_TEST_PKGS=./src/internal/bootstrap/crash/... JUNIT=out/e2e/bootstrap-fake.xml
