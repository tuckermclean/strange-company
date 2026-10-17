# `make toolbox` (plan 02 section 22, 02-AC-1, CD-64): build the toolbox and ci images from scratch for linux/amd64.
# It runs on the host (Makefile exemption from the ci-image re-exec): it drives the container engine, which is
# never mounted into the images (plan 02 section 13). The wrapper selects the engine, computes the inputs id and
# tags localhost/strange-company-<target>:in-<inputs12>.
.PHONY: toolbox

toolbox:
	@./bootstrap build --no-cache --platform linux/amd64 -f toolbox/Containerfile --target toolbox
	@./bootstrap build --no-cache --platform linux/amd64 -f toolbox/Containerfile --target ci
