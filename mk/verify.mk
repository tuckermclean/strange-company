# Tier V (plan 01 section 7.1): verify and the targets it runs, in the catalogue order. Each step is bracketed
# by vstep-begin/vstep-end so out/verify/summary.json records target, result, started_at and duration_ms.
# A step that fails stays recorded as "fail" and later steps as "skipped" (52-AC-5: the file is identical
# between runs once duration_ms and started_at are deleted).
VERIFY_TARGETS := gitops-cache fmt-check lint layout-check locks-check secrets-scan licenses-check contracts policy-check test build toolbox-check dag-check quarantine-check
VERIFY_DIR := out/verify
.NOTPARALLEL:
.PHONY: verify toolbox-check vstep-init $(VERIFY_TARGETS)

verify: vstep-init $(foreach t,$(VERIFY_TARGETS),vstep-begin/$(t) $(t) vstep-end/$(t))
	@echo "verify: $(VERIFY_TARGETS): pass"

# vsummary rewrites summary.json from the per-step rows (result, started_at, duration_ms).
define vsummary
{ printf '{"targets":['; sep=''; \
for t in $(VERIFY_TARGETS); do \
  r=skipped; s=''; d=0; \
  if [ -f $(VERIFY_DIR)/steps/$$t.row ]; then IFS=$$'\t' read -r r s d < $(VERIFY_DIR)/steps/$$t.row; fi; \
  printf '%s\n  {"target":"%s","result":"%s","started_at":"%s","duration_ms":%s}' "$$sep" "$$t" "$$r" "$$s" "$$d"; sep=','; \
done; printf '\n]}\n'; } > $(VERIFY_DIR)/summary.json
endef

vstep-init:
	@rm -rf $(VERIFY_DIR)/steps; mkdir -p $(VERIFY_DIR)/steps
	@$(vsummary)

vstep-begin/%:
	@printf '%s\t%s\t%s\n' fail "$$(date -u +%Y-%m-%dT%H:%M:%SZ)" 0 > $(VERIFY_DIR)/steps/$*.row
	@date +%s%N > $(VERIFY_DIR)/steps/$*.ns
	@$(vsummary)

vstep-end/%:
	@IFS=$$'\t' read -r _ s _ < $(VERIFY_DIR)/steps/$*.row; \
	read -r t0 < $(VERIFY_DIR)/steps/$*.ns; t1=$$(date +%s%N); \
	printf '%s\t%s\t%s\n' pass "$$s" $$(( (t1 - t0) / 1000000 )) > $(VERIFY_DIR)/steps/$*.row
	@$(vsummary)

toolbox-check:
	@toolbox/bin/toolbox-check
	@sh test/e2e/wrapper/run.sh
