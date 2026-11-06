# Tier-E lab smoke (plan 05 section 14.6, plan 52 section 8.1, 05-AC-6): `make e2e-lab` runs test/e2e/lab/run, which
# starts the QEMU lab with sc-lab, runs sc-bootstrap against it in test mode and always tears the lab down. It needs
# /dev/kvm inside the ci container: ./bootstrap exec --image ci --kvm --network host -- make e2e-lab ...
.PHONY: e2e-lab

LAB_SUBSTRATE ?= lab-small
LAB_OS ?= debian-13
LAB_STOP_AFTER ?= provision

e2e-lab:
	LAB_SUBSTRATE='$(LAB_SUBSTRATE)' LAB_OS='$(LAB_OS)' LAB_STOP_AFTER='$(LAB_STOP_AFTER)' test/e2e/lab/run
