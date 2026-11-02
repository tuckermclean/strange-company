# OVH API fixtures (derived, CD-71)

source: derived from OVH's published API schema, not recorded. There is no OVH account yet (CD-71). Field names,
types, enums and nesting are the real ones from the public schema documents fetched on 2026-11-01 from
`https://eu.api.ovh.com/1.0/vps.json`, `.../me.json` and `.../auth.json`; **every value is invented**
(documentation addresses 203.0.113.0/24 and 2001:db8::/32, made-up service names, ids and keys).
All fixtures are re-recorded with `SC-CI-OVH-READONLY` before GATE-P02 (plan 04 section 22); keep the file names.

| File | Follows (schema document, model or operation) |
|---|---|
| `vps/vps-get.json` | `vps.json` `GET /vps/{serviceName}` -> `vps.VPSWithIAM` (with `vps.Model`, `iam.ResourceMetadata`); `state: running` |
| `vps/vps-get-installing.json` | the same model with `state: installing` (`vps.VpsStateEnum`), no `iam` |
| `vps/vps-ips.json` | `GET /vps/{serviceName}/ips` -> `ip[]` (strings; one IPv4 and one IPv6) |
| `vps/vps-ips-v4-only.json` | the same operation, IPv4 only |
| `auth/current-credential-ok.json` | `auth.json` `GET /auth/currentCredential` -> `auth.ApiCredential` with `auth.AccessRule[]` rules; the rules are the plan 04 section 10 consumer-key list, one rule per method and path |
| `auth/current-credential-too-broad.json` | the same model with rules `{GET,/*}` and `{POST,/*}` |
| `orphan/*.json` | one file per orphan-scan scenario, a map `"GET <path>" -> response body` served by a test HTTP server under `/1.0` |

The `orphan/*.json` bodies follow: `GET /vps` -> `string[]` (service names); `GET /vps/{serviceName}` as above;
`me.json` `GET /me/order` -> `long[]` (query `date.from`, `date.to`); `GET /me/order/{orderId}` -> `billing.Order`;
`GET /me/order/{orderId}/status` -> `billing.order.OrderStatusEnum` (a JSON string: `delivering`, `delivered`,
`cancelled`, ...); `GET /me/order/{orderId}/details` -> `long[]`; `GET /me/order/{orderId}/details/{orderDetailId}` ->
`billing.OrderDetail` (`domain` is the service name once delivered, empty before); `.../details/{id}/extension` ->
`billing.ItemDetail` whose `order.plan.code` is the plan code. Scenarios: `delivered-orphan` (named VPS not in
state), `pending-order` (order `delivering`, no VPS yet), `foreign-vps` (displayName not `sc-<cluster>-node-NN`),
`none`, `delivered-unnamed-order` (BOOT-28: delivered order whose VPS has the default display name),
`known-in-state` (VPS in state, a cancelled order and a non-VPS order; nothing to report).

Fidelity (F09 review): `lockStatus` is the `vps.LockStatus` object (`{locked, reason}`, here `{"locked": false}`), and every
non-null field of `vps.VPSWithIAM` (`cluster`, `monitoringIpBlocks`) and `billing.Order` (`password`, `pdfUrl`) is present,
as the schema documents fetched on 2026-11-01 define them.
`lockStatus.reason` is omitted when the VPS is unlocked (its enum, `vps.LockReasonEnum`, only has `abuse`). `vps.Model`
carries `availableOptions`, `datacenter`, `disk`, `diskLocationType`, `maximumAdditionnalIp`, `memory`, `name`, `offer`,
`vcore` and `version`; `monitoringIpBlocks` belongs only at the top level of `vps.VPSWithIAM`.

Open (verify, S-1): that the plan code is carried by `.../extension` for VPS orders, and that `domain` holds the
service name; this is the plan 04 section 14.4 "verify field names" item.

## Signing known-answer vectors

OVH documents the algorithm (`$1$` + hex SHA-1 of `AS+"+"+CK+"+"+METHOD+"+"+QUERY+"+"+BODY+"+"+TSTAMP`, `QUERY` being
the full URL) but publishes no worked digest for published keys. The vectors in
`src/internal/provider/ovh/sign_test.go` are therefore **computed from the documented formula**, not copied from OVH:
each digest comes from `printf '%s' "<AS>+<CK>+<METHOD>+<URL>+<BODY>+<TS>" | sha1sum`, cross-checked with Python
`hashlib.sha1`. The keys (`AS=EgBjgXkQ0aWoLh9q`, `CK=Pv3tR8nYc2WzKd6U`) are invented.
