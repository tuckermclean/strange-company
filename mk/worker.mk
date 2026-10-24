# Tier-E container-level suite for the worker and the harness images (F07 brief D3 Layer 2, plan 28 section 13.1).
# Not part of VERIFY_TARGETS. Both targets run on the host (Makefile host-only branch): the script drives the
# container engine itself and starts the ci image through ./bootstrap. SC_ENGINE (docker|podman) is inherited.
.PHONY: harness-image-test test-worker-component

harness-image-test:
	@test -n "$(IMAGE)" || { echo "harness-image-test: IMAGE is required, e.g. make harness-image-test IMAGE=harness-fake" >&2; exit 2; }
	test/e2e/worker/run image-test $(IMAGE)

test-worker-component:
	test/e2e/worker/run component
