# OpenTofu state targets (plan 06 sections 14.2 and 22, 06-AC-1, 06-AC-9).
# tofu-validate is tier V: offline static checks, part of VERIFY_TARGETS (mk/verify.mk).
# test-integration is tier E and NOT part of VERIFY_TARGETS: it starts the SeaweedFS `weed` emulator as a subprocess
# and runs the real `tofu` (the ci image has both), and writes out/integration/s3-conformance.json.
.PHONY: tofu-validate test-integration

tofu-validate:
	@cd src && go run ./cmd/sc-lint tofu-validate --root ..
	@tofu fmt -check -recursive infra/tofu

test-integration:
	@mkdir -p out/integration
	cd src && go test -count=1 -tags integration -run Integration ./internal/tofustate/...
