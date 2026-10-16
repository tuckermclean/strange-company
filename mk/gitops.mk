# GitOps targets (plan 01 section 7.1). F01 pins no charts, so gitops-cache has nothing to pull and
# succeeds offline; plan 18 replaces the recipe with the digest-pinned chart pull into .cache/charts/.
.PHONY: gitops-cache

gitops-cache:
	@echo "gitops-cache: no pinned charts at F01 (nothing to pull)"
