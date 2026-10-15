# Strange Company Makefile (plan 01 section 8.1).
SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DELETE_ON_ERROR:
MAKEFLAGS += --no-builtin-rules
.DEFAULT_GOAL := verify

ifneq ($(SC_IN_CONTAINER),1)
# Container re-exec: every target runs as `make <goals>` in the ci image. The recipe uses a literal
# `make`, not $(MAKE), so `make -n` prints it instead of recursing.
GOALS := $(if $(MAKECMDGOALS),$(MAKECMDGOALS),$(.DEFAULT_GOAL))
.PHONY: $(GOALS)
$(firstword $(GOALS)):
	@./bootstrap exec --image ci -- make $(GOALS) $(MAKEOVERRIDES)
$(wordlist 2,$(words $(GOALS)),$(GOALS)):
	@:
else
include mk/*.mk
endif
