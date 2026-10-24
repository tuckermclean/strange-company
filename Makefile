# Strange Company Makefile (plan 01 section 8.1).
SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DELETE_ON_ERROR:
MAKEFLAGS += --no-builtin-rules --no-print-directory
.DEFAULT_GOAL := verify

ifneq ($(filter toolbox,$(MAKECMDGOALS)),)
# `make toolbox` runs on the host (plan 01 section 8.1, CD-64): it drives the container engine and builds the ci
# image itself, so it is the one target that is not re-executed in the ci image. Run it alone.
ifneq ($(MAKECMDGOALS),toolbox)
$(error make toolbox must be run alone: it builds the images on the host, so it cannot share a command line with other goals)
endif
ifeq ($(SC_IN_CONTAINER),1)
$(error make toolbox must be run alone on the host, not inside the container: it drives the container engine, which is never mounted into the images)
endif
include mk/toolbox.mk
else ifneq ($(filter harness-image-test test-worker-component,$(MAKECMDGOALS)),)
# The tier-E container suite (mk/worker.mk) also runs on the host: test/e2e/worker/run drives the container engine and
# starts the ci image through ./bootstrap itself. Like make toolbox it is not re-executed in the ci image.
ifeq ($(SC_IN_CONTAINER),1)
$(error make $(MAKECMDGOALS) must run on the host, not inside the container: it drives the container engine, which is never mounted into the images)
endif
include mk/worker.mk
else ifneq ($(SC_IN_CONTAINER),1)
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
