# generic-paid plan fixtures

source: synthetic. There is no real module behind `test_server`; the documents follow the shape of
`tofu show -json` at format_version 1.2 (OpenTofu 1.13.1): `configuration.provider_config`,
`resource_changes[]` with mode, type, address, provider_name, `change.actions`, `before`, `after`,
`after_unknown` and `after_sensitive`. They were generated once by a throwaway script (not committed) and
are edited by hand. Plan 03 section 22 lists the fixture classes; each file name is one class.
The guard files here are test inputs in the `TofuPlanGuard` shape; they are not the guards of a real module.
Test expectations: region `REG-1`, plan code `plan-small`, ssh key `sc-prod-key`, two nodes.
OVH fixtures are derived separately (CD-71).
