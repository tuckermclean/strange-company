# State-machine check (plan 29 section 8.2, 29-AC-1). `sc-statemachine` (Go, built from source) validates
# contracts/state-machine.yaml against its schema and every loader invariant and prints the per-mode
# reachability report. STATE_MACHINE_TABLE=<file> checks another table.
.PHONY: state-machine-check

STATE_MACHINE_TABLE ?=

state-machine-check:
	@cd src && go run ./cmd/sc-statemachine check --contracts ../contracts$(if $(STATE_MACHINE_TABLE), --table "$(abspath $(STATE_MACHINE_TABLE))",)
