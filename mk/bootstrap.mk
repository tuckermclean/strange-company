# Bootstrap configuration documentation (plan 50 section 9.2, 50-AC-12): bootstrap.env.example is generated from the
# variable table in src/internal/bootstrap/config/vars.go; config-docs-check fails when the committed file drifts.
.PHONY: config-docs-check config-docs-generate
config-docs-check:
	@cd src && go run ./cmd/sc-lint config-docs --root ..

config-docs-generate:
	@cd src && go run ./cmd/sc-lint config-docs --root .. --write
