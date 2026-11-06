# generic-paid plan fixtures

source: synthetic. There is no real module behind `test_server`; the documents follow the shape of
`tofu show -json` at format_version 1.2 (OpenTofu 1.13.1): `configuration.provider_config`,
`resource_changes[]` with mode, type, address, provider_name, `change.actions`, `before`, `after`,
`after_unknown` and `after_sensitive`. They were generated once by a throwaway script (not committed) and
are edited by hand. Plan 03 section 22 lists the fixture classes; each file name is one class.
The guard files here are test inputs in the `TofuPlanGuard` shape; they are not the guards of a real module.
Test expectations: region `REG-1`, plan code `plan-small`, ssh key `sc-prod-key`, two nodes.
OVH fixtures are derived separately (CD-71).

CD-72 hardening fixtures (all source: synthetic, hand-written compact JSON in the same shape): `mode-missing`,
`mode-unknown` (resource-change mode not managed/data), `data-create`, `data-delete` (data entries with a
non-read action), `destroy-with-data-read` (delete plus a data read), `expand-prior-state` (two no-op instances
plus one create), `selector-valid` and `selector-unknown` (a `plan[0].configuration` list with the region and
ssh key entries not at index 0, for `[?label=="..."]` selector paths).

CD-72 review-2 fixtures (source: synthetic, derived from `selector-valid.json` by a throwaway script, same shape;
unknown encoding follows OpenTofu: unknown object keys are omitted from `after`, unknown list elements are null,
`after_unknown` carries `true`): `selector-unknown-key-omitted` (an element whose label is unknown and omitted),
`selector-unknown-null-element` (a null unknown list element), `selector-unknown-list-omitted` and
`selector-unknown-list` (the whole `configuration` list unknown, absent or present in `after`), and
`selector-unrelated-unknown` (only an unrelated key of an unrelated element unknown; must pass).
