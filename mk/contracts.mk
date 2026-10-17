# Contract targets (plan 01 section 8.3). `sc-contracts` (Go, built from source) checks the schemas, the OpenAPI
# documents and every governed instance file of the section 8.3 glob table.
.PHONY: contracts

contracts:
	@cd src && go run ./cmd/sc-contracts --root ..
