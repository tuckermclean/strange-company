# Bootstrap configuration documentation (plan 50 section 9.2, 50-AC-12): bootstrap.env.example is generated from the
# variable table in src/internal/bootstrap/config; config-docs-check fails when the committed file drifts.
.PHONY: config-docs-check config-docs-generate
config-docs-check:
	@cd src && go run ./cmd/sc-bootstrap config-docs-check ../bootstrap.env.example

config-docs-generate:
	@cd src && go run ./cmd/sc-bootstrap config-docs-generate > ../bootstrap.env.example
