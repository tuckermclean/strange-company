# Strange Company Makefile (plan 01 section 8.1).
SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DELETE_ON_ERROR:
MAKEFLAGS += --no-builtin-rules --no-print-directory
.DEFAULT_GOAL := verify

ifneq ($(SC_IN_CONTAINER),1)
# Container re-exec: every target runs as `make <goals>` in the ci image. The recipe uses a literal
# `make`, not $(MAKE), so `make -n` prints it instead of recursing.
GOALS := $(if $(MAKECMDGOALS),$(MAKECMDGOALS),$(.DEFAULT_GOAL))
.PHONY: $(GOALS)
# Command-line overrides travel as one single-quoted argv element each, with the user's literal value
# (MAKEOVERRIDES holds `$` doubled and splits on spaces, so it is not used).
sq = '$(subst ','\'',$(1))'
# The names are stripped; the values and the recipe line are not, so whitespace inside a value survives.
OVERRIDE_NAMES := $(strip $(foreach v,$(filter-out SC_IN_CONTAINER,$(.VARIABLES)),$(if $(filter command line,$(origin $(v))),$(v))))
OVERRIDE_ARGS := $(foreach v,$(OVERRIDE_NAMES),$(call sq,$(v)=$(value $(v))))
$(firstword $(GOALS)):
	@./bootstrap exec --image ci -- make $(GOALS)$(if $(OVERRIDE_NAMES), $(OVERRIDE_ARGS))
$(wordlist 2,$(words $(GOALS)),$(GOALS)):
	@:
else
include mk/*.mk
endif
